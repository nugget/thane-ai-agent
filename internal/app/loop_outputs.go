package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

const (
	loopOutputContentBytes = 16 * 1024
	loopOutputRecentBytes  = 8 * 1024
)

type loopOutputContext struct {
	Outputs []loopOutputContextEntry `json:"outputs"`
}

type loopOutputContextEntry struct {
	Name             string             `json:"name"`
	Type             string             `json:"type"`
	Mode             string             `json:"mode"`
	Ref              string             `json:"ref"`
	Purpose          string             `json:"purpose,omitempty"`
	ToolName         string             `json:"tool_name"`
	Interface        string             `json:"interface"`
	Policy           string             `json:"policy"`
	Exists           bool               `json:"exists"`
	Title            string             `json:"title,omitempty"`
	UpdatedDelta     string             `json:"updated_delta,omitempty"`
	Content          string             `json:"content,omitempty"`
	RecentContent    string             `json:"recent_content,omitempty"`
	Truncated        bool               `json:"truncated,omitempty"`
	BytesShown       int                `json:"bytes_shown,omitempty"`
	BytesTotal       int                `json:"bytes_total,omitempty"`
	UnavailableError string             `json:"unavailable_error,omitempty"`
	Journal          *loopOutputJournal `json:"journal,omitempty"`
	Tiers            []string           `json:"tiers,omitempty"`
	Audience         string             `json:"audience,omitempty"`
}

type loopOutputJournal struct {
	Window     string `json:"window,omitempty"`
	MaxWindows int    `json:"max_windows,omitempty"`
}

// hydrateLoopOutputs is the universal "make this spec runtime-ready"
// pass. Despite the name (preserved for call-site stability), it now
// wires both declared document outputs and per-loop focus-tag runtime
// tools (watch_entity / unwatch_entity). Either step is a no-op when
// the corresponding metadata is absent, so the same call works for
// every spec the registry produces.
func (a *App) hydrateLoopOutputs(spec looppkg.Spec) (looppkg.Spec, error) {
	if len(spec.Outputs) > 0 {
		if a == nil || a.documentStore == nil {
			return looppkg.Spec{}, fmt.Errorf("loop %q declares outputs but managed document roots are not configured", spec.Name)
		}
		outputs := cloneLoopOutputs(spec.Outputs)
		spec.RuntimeTools = append(spec.RuntimeTools, buildLoopOutputTools(a.documentStore, outputs)...)
		spec.OutputContextBuilder = func(ctx context.Context, _ []looppkg.OutputSpec) (string, error) {
			return renderLoopOutputContextWithNow(ctx, a.documentStore, outputs, time.Now())
		}
	}
	return a.hydrateLoopFocusTools(spec)
}

func buildLoopOutputTools(store *documents.Store, outputs []looppkg.OutputSpec) []looppkg.RuntimeTool {
	if store == nil || len(outputs) == 0 {
		return nil
	}
	out := make([]looppkg.RuntimeTool, 0, len(outputs))
	notes := findWorkingNotesOutput(outputs)
	for _, output := range outputs {
		output := output
		if output.IsTiered() {
			// A tiered output's interface is a set of typed projections,
			// so it gets the publish tool instead of a body-blob replace.
			out = append(out, buildTieredPublishTool(store, output, notes))
			continue
		}
		switch output.EffectiveMode() {
		case looppkg.OutputModeReplace:
			out = append(out, looppkg.RuntimeTool{
				Name:               output.ToolName(),
				Description:        replaceOutputDescription(output),
				SkipContentResolve: true,
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"body": map[string]any{
							"type":        "string",
							"description": "Complete markdown body for this output. This replaces the document body as the current authoritative state.",
						},
					},
					"required": []string{"body"},
				},
				Handler: func(ctx context.Context, args map[string]any) (string, error) {
					// Rename guard: this tool's parameter was unified with the
					// document tools' body. A loop mid-conversation may replay
					// the old key from its own history — teach, don't eat.
					if _, hasContent := args["content"]; hasContent {
						return "", fmt.Errorf("this tool's markdown parameter is %q (renamed from %q for consistency with the doc tools) — re-call with body", "body", "content")
					}
					content, _ := args["body"].(string)
					if strings.TrimSpace(content) == "" {
						return "", fmt.Errorf("body is required")
					}
					result, err := store.Write(ctx, documents.WriteArgs{
						Ref:  output.Ref,
						Body: &content,
						// Stamped on every write, not only the first: the
						// exclusion in search and tagged-guidance injection
						// reads this key off the document, so a rewrite that
						// dropped it would quietly publish the loop's private
						// thinking.
						Frontmatter: loopOutputAudienceFrontmatter(output),
					})
					if err != nil {
						return "", err
					}
					return marshalLoopOutputToolResult(result)
				},
			})
		case looppkg.OutputModeAppend:
			out = append(out, looppkg.RuntimeTool{
				Name:               output.ToolName(),
				Description:        appendOutputDescription(output),
				SkipContentResolve: true,
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"entry": map[string]any{
							"type":        "string",
							"description": "Journal entry content to append.",
						},
					},
					"required": []string{"entry"},
				},
				Handler: func(ctx context.Context, args map[string]any) (string, error) {
					entry, _ := args["entry"].(string)
					if strings.TrimSpace(entry) == "" {
						return "", fmt.Errorf("entry is required")
					}
					result, err := store.JournalUpdate(ctx, documents.JournalUpdateArgs{
						Ref:         output.Ref,
						Entry:       entry,
						Window:      output.JournalWindow,
						MaxWindows:  output.MaxWindows,
						Frontmatter: loopOutputAudienceFrontmatter(output),
					})
					if err != nil {
						return "", err
					}
					return marshalLoopOutputToolResult(result)
				},
			})
		}
	}
	return out
}

func renderLoopOutputContextWithNow(ctx context.Context, store *documents.Store, outputs []looppkg.OutputSpec, now time.Time) (string, error) {
	if store == nil || len(outputs) == 0 {
		return "", nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	payload := loopOutputContext{Outputs: make([]loopOutputContextEntry, 0, len(outputs))}
	for _, output := range outputs {
		entry := loopOutputContextEntry{
			Name:      output.Name,
			Type:      string(output.Type),
			Mode:      loopOutputContextMode(output),
			Ref:       output.Ref,
			Purpose:   output.Purpose,
			ToolName:  output.ToolName(),
			Policy:    "Write only through the generated output tool. The managed document root handles path safety, indexing, provenance, and signature policy.",
			Interface: outputInterfaceDescription(output),
			Audience:  string(output.EffectiveAudience()),
		}
		for _, field := range output.TierFields() {
			entry.Tiers = append(entry.Tiers, field.Key)
		}
		if output.Type == looppkg.OutputTypeJournalDocument {
			entry.Journal = &loopOutputJournal{
				Window:     output.JournalWindow,
				MaxWindows: output.MaxWindows,
			}
		}
		doc, err := store.Read(ctx, output.Ref)
		if err != nil {
			if strings.Contains(err.Error(), "document not found") || errors.Is(err, os.ErrNotExist) {
				entry.Exists = false
				payload.Outputs = append(payload.Outputs, entry)
				continue
			}
			entry.UnavailableError = err.Error()
			payload.Outputs = append(payload.Outputs, entry)
			continue
		}
		entry.Exists = true
		entry.Title = doc.Title
		entry.UpdatedDelta = loopOutputUpdatedDelta(doc, now)
		switch output.Type {
		case looppkg.OutputTypeMaintainedDocument:
			entry.Content, entry.Truncated, entry.BytesShown, entry.BytesTotal = truncateLoopOutputText(doc.Body, loopOutputContentBytes, false)
		case looppkg.OutputTypeWorkingNotes:
			// The head, not the tail: a loop rewriting its current
			// thinking needs to see what it is replacing.
			entry.Content, entry.Truncated, entry.BytesShown, entry.BytesTotal = truncateLoopOutputText(doc.Body, loopOutputContentBytes, false)
		case looppkg.OutputTypeJournalDocument:
			entry.RecentContent, entry.Truncated, entry.BytesShown, entry.BytesTotal = truncateLoopOutputText(doc.Body, loopOutputRecentBytes, true)
		}
		payload.Outputs = append(payload.Outputs, entry)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal loop output context: %w", err)
	}
	return "## Declared Durable Outputs\n\nThese are this loop's official durable document outputs. Use the generated output tools below instead of generic file tools; write policy belongs to the document root, not to the prompt.\n\n```json\n" + string(data) + "\n```", nil
}

func loopOutputUpdatedDelta(doc *documents.DocumentRecord, now time.Time) string {
	if doc == nil {
		return ""
	}
	for _, key := range []string{"updated", "updated_at"} {
		for _, value := range doc.Frontmatter[key] {
			if delta := loopOutputDelta(value, now); delta != "" {
				return delta
			}
		}
	}
	return loopOutputDelta(doc.ModifiedAt, now)
}

func loopOutputDelta(value string, now time.Time) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	ts, err := database.ParseTimestamp(value)
	if err != nil {
		return ""
	}
	return promptfmt.FormatDeltaOnly(ts, now)
}

func outputInterfaceDescription(output looppkg.OutputSpec) string {
	if output.IsTiered() {
		keys := make([]string, 0, 4)
		for _, field := range output.TierFields() {
			keys = append(keys, field.Key)
		}
		return "Call " + output.ToolName() + " with every projection in one call (" + strings.Join(keys, ", ") + "). Headings are rendered for you; each projection has its own size budget."
	}
	switch output.EffectiveMode() {
	case looppkg.OutputModeReplace:
		return "Call " + output.ToolName() + " with complete replacement markdown content for this maintained document."
	case looppkg.OutputModeAppend:
		return "Call " + output.ToolName() + " with one new journal entry; do not rewrite old entries."
	default:
		return "Use the generated output tool for this declaration."
	}
}

// loopOutputContextMode reports the write interface the model actually
// has for this output. A tiered output's spec-level mode is still
// replace — tiers are the only declaration, so there is no authorable
// publish mode to contradict — but its generated tool takes projections
// rather than a document body. Reporting the spec mode here would pair
// "publish_output_*" with "mode: replace" in the same context block and
// leave the model to guess which one describes the call it should make.
func loopOutputContextMode(output looppkg.OutputSpec) string {
	if output.IsTiered() {
		return "publish"
	}
	return string(output.EffectiveMode())
}

// replaceOutputDescription frames a replace-mode output. Working notes
// and a published document are both rewritten wholesale, but what
// belongs in each is opposite, so they do not share framing.
func replaceOutputDescription(output looppkg.OutputSpec) string {
	if output.Type == looppkg.OutputTypeWorkingNotes {
		return workingNotesDescription(output)
	}
	return fmt.Sprintf("Replace the loop-declared maintained document output %q at %s. Pass the complete markdown body for the new current document state; root policy and indexing are handled by Thane.", output.Name, output.Ref)
}

// workingNotesDescription frames the loop's private thinking. What
// belongs here is what a reader should never see, and a model that
// thinks it is writing another public surface will not write it. The
// instruction to rewrite rather than add is the load-bearing part:
// notes that accumulate stop being a current view and become a history
// the loop has to interpret.
func workingNotesDescription(output looppkg.OutputSpec) string {
	return fmt.Sprintf("Rewrite this loop's working notes %q at %s — its private thinking, carried from turn to turn. Hold what you currently believe: working theories, what an experiment is showing so far, what you expect to happen next, what you are unsure of and what would settle it. Replace the whole body each time so it stays a current view rather than a log; drop what you no longer think and keep what still holds. No consumer surface reads it — it stays out of search results and out of other loops' context — so write what would clutter or mislead a published document.", output.Name, output.Ref)
}

// appendOutputDescription frames an append-mode output for the model.
func appendOutputDescription(output looppkg.OutputSpec) string {
	return fmt.Sprintf("Append to the loop-declared journal output %q at %s. Pass only the new journal entry; Thane stamps, windows, prunes, indexes, and applies root policy.", output.Name, output.Ref)
}

func truncateLoopOutputText(s string, maxBytes int, tail bool) (string, bool, int, int) {
	total := len(s)
	if total <= maxBytes {
		return s, false, total, total
	}
	if maxBytes <= 0 {
		return "", true, 0, total
	}
	var out string
	if tail {
		start := len(s) - maxBytes
		for start < len(s) && !utf8.RuneStart(s[start]) {
			start++
		}
		out = "[truncated: showing recent tail]\n" + s[start:]
	} else {
		end := maxBytes
		for end < len(s) && end > 0 && !utf8.RuneStart(s[end]) {
			end--
		}
		out = s[:end] + "\n[truncated: output exceeded context budget]"
	}
	return out, true, len(out), total
}

func marshalLoopOutputToolResult(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal loop output result: %w", err)
	}
	return string(data), nil
}

func cloneLoopOutputs(src []looppkg.OutputSpec) []looppkg.OutputSpec {
	if len(src) == 0 {
		return nil
	}
	dst := make([]looppkg.OutputSpec, len(src))
	copy(dst, src)
	for i := range dst {
		dst[i].Tiers = append([]looppkg.OutputTier(nil), src[i].Tiers...)
	}
	return dst
}
