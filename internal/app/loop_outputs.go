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

	"github.com/nugget/thane-ai-agent/internal/model/outputtargets"
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
	// Target carries the slot contract for structured payload outputs
	// and is absent for document outputs.
	Target *loopOutputTargetContext `json:"target,omitempty"`
}

// loopOutputTargetContext is the model-facing view of a structured
// payload output: what surface it renders on, which slots exist with
// what budgets, where the payload lands, and what was last sent.
type loopOutputTargetContext struct {
	ID                 string                 `json:"id"`
	Title              string                 `json:"title,omitempty"`
	Summary            string                 `json:"summary,omitempty"`
	Binding            string                 `json:"binding,omitempty"`
	EntityID           string                 `json:"entity_id,omitempty"`
	Slots              []outputtargets.Slot   `json:"slots,omitempty"`
	LastPublished      *outputtargets.Payload `json:"last_published,omitempty"`
	LastPublishedDelta string                 `json:"last_published_delta,omitempty"`
	LastPublishedNote  string                 `json:"last_published_note,omitempty"`
	Unavailable        string                 `json:"unavailable,omitempty"`
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
		outputs := cloneLoopOutputs(spec.Outputs)

		// Each output tier needs its own backend. Check only the tiers
		// this spec actually declares, so a loop publishing a watch
		// complication does not require document roots and vice versa.
		var sink structuredOutputSink
		for _, output := range outputs {
			switch output.Type {
			case looppkg.OutputTypeStructuredPayload:
				if sink = a.structuredOutputSink(); sink == nil {
					return looppkg.Spec{}, fmt.Errorf("loop %q declares structured payload output %q but MQTT publishing is not configured; structured outputs render through Home Assistant entities", spec.Name, output.Name)
				}
			default:
				if a == nil || a.documentStore == nil {
					return looppkg.Spec{}, fmt.Errorf("loop %q declares outputs but managed document roots are not configured", spec.Name)
				}
			}
		}

		runtimeTools, err := buildLoopOutputTools(a.documentStore, sink, outputs)
		if err != nil {
			return looppkg.Spec{}, fmt.Errorf("loop %q: %w", spec.Name, err)
		}
		spec.RuntimeTools = append(spec.RuntimeTools, runtimeTools...)
		spec.OutputContextBuilder = func(ctx context.Context, _ []looppkg.OutputSpec) (string, error) {
			return renderLoopOutputContextWithNow(ctx, a.documentStore, sink, outputs, time.Now())
		}
	}
	return a.hydrateLoopFocusTools(spec)
}

func buildLoopOutputTools(store *documents.Store, sink structuredOutputSink, outputs []looppkg.OutputSpec) ([]looppkg.RuntimeTool, error) {
	if len(outputs) == 0 {
		return nil, nil
	}
	out := make([]looppkg.RuntimeTool, 0, len(outputs))
	for _, output := range outputs {
		output := output
		if output.EffectiveMode() == looppkg.OutputModeSet {
			tool, err := buildStructuredOutputTool(sink, output)
			if err != nil {
				return nil, err
			}
			out = append(out, tool)
			continue
		}
		if store == nil {
			return nil, fmt.Errorf("output %q requires a document store", output.Name)
		}
		switch output.EffectiveMode() {
		case looppkg.OutputModeReplace:
			out = append(out, looppkg.RuntimeTool{
				Name:               output.ToolName(),
				Description:        fmt.Sprintf("Replace the loop-declared maintained document output %q at %s. Pass the complete markdown body for the new current document state; root policy and indexing are handled by Thane.", output.Name, output.Ref),
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
				Description:        fmt.Sprintf("Append to the loop-declared journal output %q at %s. Pass only the new journal entry; Thane stamps, windows, prunes, indexes, and applies root policy.", output.Name, output.Ref),
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
						Ref:        output.Ref,
						Entry:      entry,
						Window:     output.JournalWindow,
						MaxWindows: output.MaxWindows,
					})
					if err != nil {
						return "", err
					}
					return marshalLoopOutputToolResult(result)
				},
			})
		}
	}
	return out, nil
}

// buildStructuredOutputTool generates the request-scoped tool for one
// structured payload declaration. The tool's parameter schema is the
// target's slot contract, so the model is taught the exact slots, types,
// and size budgets of the surface it is filling instead of being handed a
// free-text field and left to guess what fits.
func buildStructuredOutputTool(sink structuredOutputSink, output looppkg.OutputSpec) (looppkg.RuntimeTool, error) {
	if sink == nil {
		return looppkg.RuntimeTool{}, fmt.Errorf("output %q requires a structured output sink", output.Name)
	}
	binding, err := structuredOutputBindingFor(output)
	if err != nil {
		return looppkg.RuntimeTool{}, err
	}
	entityID := sink.EntityID(binding.EntitySuffix)

	return looppkg.RuntimeTool{
		Name:        output.ToolName(),
		Description: binding.Target.ToolDescription(output.Name, entityID),
		// Slot values are literal display text: a value that happens to
		// look like a document ref must reach the watch face as typed,
		// not as the content it resembles.
		SkipContentResolve: true,
		Parameters:         binding.Target.Schema(),
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			payload, err := binding.Target.Normalize(args)
			if err != nil {
				return "", err
			}
			if err := sink.Publish(ctx, binding, payload); err != nil {
				return "", err
			}
			return marshalLoopOutputToolResult(structuredOutputToolResult{
				Output:     output.Name,
				Target:     binding.Target.ID,
				EntityID:   entityID,
				State:      payload.State,
				Attributes: payload.Attributes,
			})
		},
	}, nil
}

// structuredOutputToolResult is what the model sees after a successful
// publish: the entity it can now bind to, plus the exact payload that
// landed, so a later iteration can diff against it without a read tool.
type structuredOutputToolResult struct {
	Output     string         `json:"output"`
	Target     string         `json:"target"`
	EntityID   string         `json:"entity_id"`
	State      string         `json:"state"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// structuredOutputBindingFor resolves a declaration into the binding its
// handler publishes with. [looppkg.OutputSpec.Validate] already enforces
// both halves, so a failure here means a spec persisted before the
// current validation rules — worth an explicit error rather than a loop
// that starts with a silently missing output tool.
func structuredOutputBindingFor(output looppkg.OutputSpec) (structuredOutputBinding, error) {
	target, ok := outputtargets.Lookup(output.Target)
	if !ok {
		return structuredOutputBinding{}, fmt.Errorf("output %q names unknown target %q; registered targets are %s", output.Name, output.Target, strings.Join(outputtargets.IDs(), ", "))
	}
	_, suffix, found := strings.Cut(output.Ref, ":")
	suffix = strings.TrimSpace(suffix)
	if !found || suffix == "" {
		return structuredOutputBinding{}, fmt.Errorf("output %q has ref %q; structured payload outputs need a ref of the form mqtt:<entity_suffix>", output.Name, output.Ref)
	}
	return structuredOutputBinding{
		EntitySuffix: suffix,
		Label:        output.Name,
		Target:       target,
	}, nil
}

func renderLoopOutputContextWithNow(ctx context.Context, store *documents.Store, sink structuredOutputSink, outputs []looppkg.OutputSpec, now time.Time) (string, error) {
	if len(outputs) == 0 {
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
			Mode:      string(output.EffectiveMode()),
			Ref:       output.Ref,
			Purpose:   output.Purpose,
			ToolName:  output.ToolName(),
			Policy:    "Write only through the generated output tool. The managed document root handles path safety, indexing, provenance, and signature policy.",
			Interface: outputInterfaceDescription(output),
		}
		if output.Type == looppkg.OutputTypeStructuredPayload {
			entry.Policy = "Write only through the generated output tool. Every call replaces the whole payload — omitted slots are cleared — and slot budgets are enforced, not truncated."
			entry.Target = structuredOutputTargetContext(output, sink, now)
			payload.Outputs = append(payload.Outputs, entry)
			continue
		}
		if store == nil {
			entry.UnavailableError = "managed document roots are not configured"
			payload.Outputs = append(payload.Outputs, entry)
			continue
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
		case looppkg.OutputTypeJournalDocument:
			entry.RecentContent, entry.Truncated, entry.BytesShown, entry.BytesTotal = truncateLoopOutputText(doc.Body, loopOutputRecentBytes, true)
		}
		payload.Outputs = append(payload.Outputs, entry)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal loop output context: %w", err)
	}
	return "## Declared Durable Outputs\n\nThese are this loop's official durable outputs — documents it maintains and rendered surfaces it drives. Use the generated output tools below instead of generic file tools; write policy belongs to the output's backend, not to the prompt.\n\n```json\n" + string(data) + "\n```", nil
}

// structuredOutputTargetContext renders the slot contract for a
// structured payload output, plus whatever this process last published to
// it. The contract is included in full on every iteration: the model is
// choosing values against fixed geometry, and a budget it cannot see is a
// budget it will overrun.
func structuredOutputTargetContext(output looppkg.OutputSpec, sink structuredOutputSink, now time.Time) *loopOutputTargetContext {
	target, ok := outputtargets.Lookup(output.Target)
	if !ok {
		return &loopOutputTargetContext{ID: output.Target, Unavailable: "target is not registered in this build"}
	}
	entry := &loopOutputTargetContext{
		ID:      target.ID,
		Title:   target.Title,
		Summary: target.Summary,
		Binding: target.Binding,
		Slots:   target.Slots,
	}
	if sink == nil {
		entry.Unavailable = "no structured output sink is configured"
		return entry
	}
	binding, err := structuredOutputBindingFor(output)
	if err != nil {
		entry.Unavailable = err.Error()
		return entry
	}
	entry.EntityID = sink.EntityID(binding.EntitySuffix)
	if snapshot, published := sink.Last(binding.EntitySuffix); published {
		entry.LastPublished = &snapshot.Payload
		entry.LastPublishedDelta = promptfmt.FormatDeltaOnly(snapshot.At, now)
	} else {
		// Absence is ambiguous — never published, or published before a
		// restart cleared the in-process record — and the difference
		// changes whether re-publishing is redundant. Say which.
		entry.LastPublishedNote = "nothing published from this process yet; the surface may still be showing a payload from before the last restart"
	}
	return entry
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
	switch output.EffectiveMode() {
	case looppkg.OutputModeReplace:
		return "Call " + output.ToolName() + " with complete replacement markdown content for this maintained document."
	case looppkg.OutputModeAppend:
		return "Call " + output.ToolName() + " with one new journal entry; do not rewrite old entries."
	case looppkg.OutputModeSet:
		return "Call " + output.ToolName() + " with the complete slot set for this surface; omitted slots are cleared, not preserved."
	default:
		return "Use the generated output tool for this declaration."
	}
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
	return dst
}
