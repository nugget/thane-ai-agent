package main

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// wake describes the input the harness will feed to the providers.
// It mirrors the inputs that agent.Loop.Run reconstructs at the top
// of every turn: the channel binding (for subject derivation), the
// extra subjects an outer wake bridge may have set, and the user
// message that drives the archive provider's fallback query.
type wake struct {
	ConversationID string
	UserMessage    string
	Binding        *memory.ChannelBinding
	ExtraSubjects  []string // simulating subjects an outer wake bridge would inject
}

// providerResult captures everything the harness wants to surface
// per-provider: which subjects it actually saw, what query it ran,
// how many hits it returned, and the rendered output bytes. Output
// is the literal string the provider returned.
type providerResult struct {
	Name        string   `json:"name"`
	Subjects    []string `json:"subjects"`
	Query       string   `json:"query,omitempty"`
	QuerySource string   `json:"query_source,omitempty"`
	HitCount    int      `json:"hit_count"`
	OutputBytes int      `json:"output_bytes"`
	Output      string   `json:"output,omitempty"`
}

// runResult is the harness's structured report for a single wake.
type runResult struct {
	ConversationID string                 `json:"conversation_id"`
	UserMessage    string                 `json:"user_message"`
	Binding        *memory.ChannelBinding `json:"channel_binding,omitempty"`
	Subjects       []string               `json:"subjects"`
	Providers      []providerResult       `json:"providers"`
}

// runProviders builds the ctx + ContextRequest the way the agent
// loop does, then calls each prewarm provider in turn. The user can
// override the provider knobs (maxResults, maxBytes, minScore) via
// the global flags before invoking; this function does not own those
// knobs, it just runs the providers it is handed.
func runProviders(
	ctx context.Context,
	w wake,
	archive *memory.ArchiveContextProvider,
	subjectProvider *knowledge.SubjectContextProvider,
) (runResult, error) {
	subjects := buildSubjects(w)
	if len(subjects) > 0 {
		ctx = knowledge.WithSubjects(ctx, subjects)
	}
	req := agentctx.ContextRequest{
		UserMessage:   w.UserMessage,
		IncludeAlways: true,
	}

	out := runResult{
		ConversationID: w.ConversationID,
		UserMessage:    w.UserMessage,
		Binding:        w.Binding,
		Subjects:       subjects,
	}

	if subjectProvider != nil {
		body, err := subjectProvider.TagContext(ctx, req)
		if err != nil {
			return out, err
		}
		out.Providers = append(out.Providers, summarizeSubject(subjects, body))
	}
	if archive != nil {
		body, err := archive.TagContext(ctx, req)
		if err != nil {
			return out, err
		}
		out.Providers = append(out.Providers, summarizeArchive(subjects, w.UserMessage, body))
	}
	return out, nil
}

// buildSubjects mirrors agent.withRequestSubjects: contact:<ContactID>
// and contact:<Address> from the binding, on top of whatever the
// outer wake bridge already set (the simulated ExtraSubjects).
//
// Duplication is intentional — the harness should not have to swap
// its inputs every time the agent's subject-derivation policy
// evolves; instead a code-review reader can compare the two
// definitions side by side. If divergence drift becomes a real
// concern, promote both to a shared helper in internal/state/...
func buildSubjects(w wake) []string {
	var subjects []string
	subjects = append(subjects, w.ExtraSubjects...)
	if cb := w.Binding; cb != nil {
		if cb.ContactID != "" {
			subjects = appendUnique(subjects, "contact:"+cb.ContactID)
		}
		if cb.Address != "" {
			subjects = appendUnique(subjects, "contact:"+cb.Address)
		}
	}
	return subjects
}

func appendUnique(subjects []string, s string) []string {
	for _, existing := range subjects {
		if existing == s {
			return subjects
		}
	}
	return append(subjects, s)
}

// summarizeSubject parses the "### Subject-Keyed Facts" output to
// extract the fact count and reports a structured summary. Empty
// output (the provider returned "" because no facts matched) is
// reported as 0 hits.
func summarizeSubject(subjects []string, body string) providerResult {
	pr := providerResult{
		Name:        "SubjectContextProvider",
		Subjects:    append([]string{}, subjects...),
		OutputBytes: len(body),
		Output:      body,
	}
	if body == "" {
		return pr
	}
	const heading = "### Subject-Keyed Facts\n\n"
	payload := strings.TrimPrefix(body, heading)
	var env struct {
		Facts []map[string]any `json:"facts"`
	}
	if err := json.Unmarshal([]byte(payload), &env); err == nil {
		pr.HitCount = len(env.Facts)
	}
	return pr
}

// summarizeArchive parses the "### Past Experience" output, reports
// the hit count and reconstructs the query the provider would have
// run (mirroring archive_provider.go's buildQuery so the harness can
// surface "subjects" vs "message_fallback" vs "none" without
// duplicating provider state).
func summarizeArchive(subjects []string, userMessage, body string) providerResult {
	pr := providerResult{
		Name:        "ArchiveContextProvider",
		Subjects:    append([]string{}, subjects...),
		OutputBytes: len(body),
		Output:      body,
	}
	pr.Query, pr.QuerySource = reconstructArchiveQuery(subjects, userMessage)
	if body == "" {
		return pr
	}
	const heading = "### Past Experience\n\n"
	payload := strings.TrimPrefix(body, heading)
	// Count both archive output shapes. The pre-#983 provider emitted a
	// single results[] envelope; after #983 ArchiveContextProvider
	// renders distilled hits across messages[], sessions[], and
	// working_memory[] instead. A given payload carries one shape or the
	// other, so summing across all four keys reports the true hit count
	// during the bridge window without double-counting — the search-
	// efficacy signal this harness exists to surface.
	var env struct {
		Results       []map[string]any `json:"results"`
		Messages      []map[string]any `json:"messages"`
		Sessions      []map[string]any `json:"sessions"`
		WorkingMemory []map[string]any `json:"working_memory"`
	}
	if err := json.Unmarshal([]byte(payload), &env); err == nil {
		pr.HitCount = len(env.Results) + len(env.Messages) +
			len(env.Sessions) + len(env.WorkingMemory)
	}
	return pr
}

// reconstructArchiveQuery mirrors
// memory.ArchiveContextProvider.buildQuery so the harness can label
// which input path the provider would have taken without holding a
// reference to its internals. Subject keys are stripped of their
// "type:" prefix; long or multi-line messages fall through to "".
func reconstructArchiveQuery(subjects []string, userMessage string) (string, string) {
	const maxUserMessageLen = 100
	seen := make(map[string]bool)
	var terms []string
	for _, s := range subjects {
		t := s
		if i := strings.IndexByte(s, ':'); i >= 0 {
			t = s[i+1:]
		}
		if t != "" && !seen[t] {
			seen[t] = true
			terms = append(terms, t)
		}
	}
	if len(terms) > 0 {
		return strings.Join(terms, " "), "subjects"
	}
	msg := strings.TrimSpace(userMessage)
	if msg != "" && len(msg) <= maxUserMessageLen && !strings.ContainsAny(msg, "\n\r") {
		return msg, "message_fallback"
	}
	return "", ""
}
