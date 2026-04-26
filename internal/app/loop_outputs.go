package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

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
	ModifiedAt       string             `json:"modified_at,omitempty"`
	Content          string             `json:"content,omitempty"`
	RecentContent    string             `json:"recent_content,omitempty"`
	Truncated        bool               `json:"truncated,omitempty"`
	BytesShown       int                `json:"bytes_shown,omitempty"`
	BytesTotal       int                `json:"bytes_total,omitempty"`
	UnavailableError string             `json:"unavailable_error,omitempty"`
	Journal          *loopOutputJournal `json:"journal,omitempty"`
}

type loopOutputJournal struct {
	Window     string `json:"window,omitempty"`
	MaxWindows int    `json:"max_windows,omitempty"`
}

func (a *App) hydrateLoopOutputs(spec looppkg.Spec) (looppkg.Spec, error) {
	if len(spec.Outputs) == 0 {
		return spec, nil
	}
	if a == nil || a.documentStore == nil {
		return looppkg.Spec{}, fmt.Errorf("loop %q declares outputs but managed document roots are not configured", spec.Name)
	}
	outputs := cloneLoopOutputs(spec.Outputs)
	spec.RuntimeTools = append(spec.RuntimeTools, buildLoopOutputTools(a.documentStore, outputs)...)
	spec.OutputContextBuilder = func(ctx context.Context, _ []looppkg.OutputSpec) (string, error) {
		return renderLoopOutputContext(ctx, a.documentStore, outputs)
	}
	return spec, nil
}

func buildLoopOutputTools(store *documents.Store, outputs []looppkg.OutputSpec) []looppkg.RuntimeTool {
	if store == nil || len(outputs) == 0 {
		return nil
	}
	out := make([]looppkg.RuntimeTool, 0, len(outputs))
	for _, output := range outputs {
		output := output
		switch output.EffectiveMode() {
		case looppkg.OutputModeReplace:
			out = append(out, looppkg.RuntimeTool{
				Name:               output.ToolName(),
				Description:        fmt.Sprintf("Replace the loop-declared maintained document output %q at %s. Pass the complete markdown body for the new current document state; root policy and indexing are handled by Thane.", output.Name, output.Ref),
				SkipContentResolve: true,
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"content": map[string]any{
							"type":        "string",
							"description": "Complete markdown body for this output. This replaces the document body as the current authoritative state.",
						},
					},
					"required": []string{"content"},
				},
				Handler: func(ctx context.Context, args map[string]any) (string, error) {
					content, _ := args["content"].(string)
					if strings.TrimSpace(content) == "" {
						return "", fmt.Errorf("content is required")
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
	return out
}

func renderLoopOutputContext(ctx context.Context, store *documents.Store, outputs []looppkg.OutputSpec) (string, error) {
	if store == nil || len(outputs) == 0 {
		return "", nil
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
		entry.ModifiedAt = doc.ModifiedAt
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
	return "## Declared Durable Outputs\n\nThese are this loop's official durable document outputs. Use the generated output tools below instead of generic file tools; write policy belongs to the document root, not to the prompt.\n\n```json\n" + string(data) + "\n```", nil
}

func outputInterfaceDescription(output looppkg.OutputSpec) string {
	switch output.EffectiveMode() {
	case looppkg.OutputModeReplace:
		return "Call " + output.ToolName() + " with complete replacement markdown content for this maintained document."
	case looppkg.OutputModeAppend:
		return "Call " + output.ToolName() + " with one new journal entry; do not rewrite old entries."
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
