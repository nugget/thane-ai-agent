package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

const (
	loopOutputContentBytes = 16 * 1024
)

type loopOutputContext struct {
	Outputs []loopOutputContextEntry `json:"outputs"`
}

type loopOutputContextEntry struct {
	Name             string   `json:"name"`
	Type             string   `json:"type"`
	Mode             string   `json:"mode"`
	Ref              string   `json:"ref"`
	Purpose          string   `json:"purpose,omitempty"`
	ToolName         string   `json:"tool_name"`
	Interface        string   `json:"interface"`
	Policy           string   `json:"policy"`
	Exists           bool     `json:"exists"`
	Title            string   `json:"title,omitempty"`
	UpdatedDelta     string   `json:"updated_delta,omitempty"`
	Content          string   `json:"content,omitempty"`
	Truncated        bool     `json:"truncated,omitempty"`
	BytesShown       int      `json:"bytes_shown,omitempty"`
	BytesTotal       int      `json:"bytes_total,omitempty"`
	UnavailableError string   `json:"unavailable_error,omitempty"`
	Facets           []string `json:"facets,omitempty"`
	// Projections carries the published facet values verbatim for a
	// faceted document — each is budget-capped by contract, so they
	// ride whole. Content then holds only the full/Details body, which
	// is where the byte budget belongs: before this split, the entire
	// rendered document was blindly byte-truncated even though authored
	// projections existed precisely so no reader has to do that (#1250).
	Projections orderedProjections `json:"projections,omitempty"`
	Audience    string             `json:"audience,omitempty"`
}

// facetProjection is one published facet value, keyed by its facet name.
type facetProjection struct {
	Key   string
	Value string
}

// orderedProjections marshals as a JSON object whose keys ride in
// declared-facet order. A plain map would serialize alphabetically
// (digest before signal), making this the one surface out of step
// with the ladder order every sibling rendering follows — the facets
// array here, doc_read levels, and the rendered document headings.
type orderedProjections []facetProjection

func (p orderedProjections) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, projection := range p {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, err := json.Marshal(projection.Key)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(projection.Value)
		if err != nil {
			return nil, err
		}
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(value)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
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
		spec.RuntimeTools = append(spec.RuntimeTools, a.ownOutputReadTools(outputs)...)
		spec.OutputContextBuilder = func(ctx context.Context, _ []looppkg.OutputSpec) (string, error) {
			return renderLoopOutputContextWithNow(ctx, a.documentStore, outputs, time.Now())
		}
	}
	return a.hydrateLoopFocusTools(spec)
}

// ownOutputReadToolNames is the read-only document tool family every
// output-declaring loop carries regardless of its tags: read the
// current body, page through it when it outgrows a single result
// (doc_read's own truncation envelope says "select a section" —
// doc_outline and doc_section are what make that followable), and walk
// the revision history behind it. The write-side document tools stay
// tag-gated — a loop's writes belong to its generated output tool, and
// a second write door is the two-doors hazard managed_by exists to
// prevent.
var ownOutputReadToolNames = []string{"doc_read", "doc_outline", "doc_section", "doc_history", "doc_diff", "doc_at"}

// ownOutputReadResultBytes is the serialization budget for a loop
// reading its OWN declared output whole. The general doc_read cap
// protects a casual reader from a giant document; the owning loop's
// whole-document read is not casual — it is the mandatory input to a
// whole-body rewrite, and a truncated result is never a valid end
// state there (the loop would have to page and re-stitch, which is
// where content gets dropped). 8× the general cap keeps document plus
// rewrite inside a local model's context window; past this size the
// document has outgrown single-document maintenance. Never unbounded:
// a pathological document (the 32 MB frontmatter-amplification
// incident) must truncate rather than annihilate the loop's context.
const ownOutputReadResultBytes = 128 * 1024

// ownOutputReadTools re-exposes the native read-side document tools as
// loop runtime tools. A loop that owns a maintained document must be
// able to read what it owns: its own task boilerplate says "read the
// full document with doc_read" when the output context is truncated,
// and the working-notes contract says revision history records what
// changed — both were unfollowable for a loop whose tags don't include
// `documents` (a [ha, awareness] curator has zero doc_* tools). The
// natives are re-exposed verbatim — same names, same handlers — so the
// vocabulary already frozen into stored specs and shipped teaching
// becomes true, including for loops launched long before this existed.
// doc_read alone is owner-aware: a whole-document read of one of this
// loop's own outputs runs under [ownOutputReadResultBytes] instead of
// the general cap.
func (a *App) ownOutputReadTools(outputs []looppkg.OutputSpec) []looppkg.RuntimeTool {
	if a == nil || a.loop == nil {
		return nil
	}
	return wrapOwnOutputDocRead(reExposeNativeTools(a.loop.Tools(), ownOutputReadToolNames), a.documentTools, outputs)
}

// wrapOwnOutputDocRead makes the re-exposed doc_read owner-aware: a
// whole-document read of one of the loop's own declared outputs is
// served under the privileged budget, while foreign refs and
// facet-level reads keep the native behavior (a level read returns one
// projection and never nears the cap; a foreign document deserves the
// same protection every other reader gets). The ref must match a
// declared output exactly — a miss degrades to the capped native path,
// never to an error.
func wrapOwnOutputDocRead(runtimeTools []looppkg.RuntimeTool, docTools *documents.Tools, outputs []looppkg.OutputSpec) []looppkg.RuntimeTool {
	if docTools == nil || len(outputs) == 0 {
		return runtimeTools
	}
	ownRefs := make(map[string]bool, len(outputs))
	for _, output := range outputs {
		ownRefs[output.Ref] = true
	}
	for i := range runtimeTools {
		if runtimeTools[i].Name != "doc_read" {
			continue
		}
		native := runtimeTools[i].Handler
		runtimeTools[i].Description += " Reading one of THIS loop's own declared outputs without level returns the whole document under a raised result budget — one read, the full body you are about to replace."
		runtimeTools[i].Handler = func(ctx context.Context, args map[string]any) (string, error) {
			ref, _ := args["ref"].(string)
			ref = strings.TrimSpace(ref)
			level, _ := args["level"].(string)
			if ownRefs[ref] && strings.TrimSpace(level) == "" {
				return docTools.ReadWithResultBudget(ctx, documents.RefArgs{Ref: ref}, ownOutputReadResultBytes)
			}
			return native(ctx, args)
		}
	}
	return runtimeTools
}

// reExposeNativeTools copies named tools out of a registry into the
// runtime-tool shape hydration attaches to a spec, skipping names the
// registry doesn't currently carry (a registry without document roots
// registers no doc tools, and a missing read tool should degrade to
// the pre-existing behavior rather than fail the launch).
func reExposeNativeTools(registry *tools.Registry, names []string) []looppkg.RuntimeTool {
	if registry == nil {
		return nil
	}
	out := make([]looppkg.RuntimeTool, 0, len(names))
	for _, name := range names {
		native := registry.Get(name)
		if native == nil || native.Handler == nil {
			continue
		}
		out = append(out, looppkg.RuntimeTool{
			Name:                 native.Name,
			Description:          native.Description,
			Parameters:           native.Parameters,
			Handler:              native.Handler,
			SkipContentResolve:   native.SkipContentResolve,
			ContentResolveExempt: append([]string(nil), native.ContentResolveExempt...),
		})
	}
	return out
}

func buildLoopOutputTools(store *documents.Store, outputs []looppkg.OutputSpec) []looppkg.RuntimeTool {
	if store == nil || len(outputs) == 0 {
		return nil
	}
	out := make([]looppkg.RuntimeTool, 0, len(outputs))
	notes := findWorkingNotesOutput(outputs)
	for _, output := range outputs {
		output := output
		if output.HasFacets() {
			// A faceted output's interface is a set of typed projections,
			// so it gets the publish tool instead of a body-blob replace.
			out = append(out, buildFacetPublishTool(store, output, notes))
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
					if err := looppkg.ValidateOutputBodySize(content); err != nil {
						return "", err
					}
					result, err := store.Write(ctx, documents.WriteArgs{
						Ref:  output.Ref,
						Body: &content,
						// Stamped on every write, not only the first: the
						// exclusion in search and tagged-guidance injection
						// reads this key off the document, so a rewrite that
						// dropped it would quietly publish the loop's private
						// thinking.
						Frontmatter: loopOutputFrontmatter(output),
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
		// Declared facets only, matching every sibling surface (LoopView
		// outputs[].facets, doc_search hits). FacetFields() would append
		// the always-published full field, which is every faceted
		// document's baseline rather than a declared facet — listing it
		// here and not there would make the same field name carry two
		// memberships at surfaces the same model reads.
		for _, facet := range output.Facets {
			entry.Facets = append(entry.Facets, string(facet.Name))
		}
		doc, err := store.Read(ctx, output.Ref)
		if err != nil {
			if documents.IsNotFound(err) {
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
			if _, faceted := looppkg.ParseFacetSections(doc.Body); faceted && output.HasFacets() {
				// The loop's own context mirrors its publish tool's
				// argument shape: each authored projection whole, the
				// Details body under the byte budget. Same shape out
				// as goes in, so a republish is mechanical.
				payload := output.ParseFacetDocument(doc.Body)
				var projections orderedProjections
				// Declared facets only — FacetFields() appends the
				// always-published full field, and full is exactly what
				// must NOT ride here: it is unbudgeted, and its home is
				// the byte-capped content below. Duplicating it under
				// projections would reintroduce the unbounded growth
				// this split exists to end.
				for _, facet := range output.Facets {
					key := string(facet.Name)
					if value, ok := payload.FacetByKey(key); ok && strings.TrimSpace(value) != "" {
						projections = append(projections, facetProjection{Key: key, Value: value})
					}
				}
				if len(projections) > 0 {
					entry.Projections = projections
				}
				entry.Content, entry.Truncated, entry.BytesShown, entry.BytesTotal = truncateLoopOutputText(payload.Full, loopOutputContentBytes)
				break
			}
			// A pre-facet body (declared facets, first publish pending)
			// keeps the legacy whole-body path.
			entry.Content, entry.Truncated, entry.BytesShown, entry.BytesTotal = truncateLoopOutputText(doc.Body, loopOutputContentBytes)
		case looppkg.OutputTypeWorkingNotes:
			// The head, not the tail: a loop rewriting its current
			// thinking needs to see what it is replacing.
			entry.Content, entry.Truncated, entry.BytesShown, entry.BytesTotal = truncateLoopOutputText(doc.Body, loopOutputContentBytes)
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
	if ts, ok := documentUpdatedTime(doc); ok {
		return promptfmt.FormatDeltaOnly(ts, now)
	}
	return ""
}

// documentUpdatedTime resolves when a maintained document was last
// updated: the document's own declared frontmatter stamps ("updated",
// then "updated_at") outrank the file's modification time, and every
// value parses through database.ParseTimestamp — the shared
// timestamp-format authority — never a per-caller layout guess. One
// seam serves both delta rendering here and any consumer needing the
// time itself (the system self-assessment provider's age).
func documentUpdatedTime(doc *documents.DocumentRecord) (time.Time, bool) {
	if doc == nil {
		return time.Time{}, false
	}
	for _, key := range []string{"updated", "updated_at"} {
		for _, value := range doc.Frontmatter[key] {
			if ts, err := database.ParseTimestamp(value); err == nil {
				return ts, true
			}
		}
	}
	if ts, err := database.ParseTimestamp(doc.ModifiedAt); err == nil {
		return ts, true
	}
	return time.Time{}, false
}

func outputInterfaceDescription(output looppkg.OutputSpec) string {
	if output.HasFacets() {
		keys := make([]string, 0, 4)
		for _, field := range output.FacetFields() {
			keys = append(keys, field.Key)
		}
		return "Call " + output.ToolName() + " with every projection in one call (" + strings.Join(keys, ", ") + "). Headings are rendered for you; each projection has its own size budget."
	}
	switch output.EffectiveMode() {
	case looppkg.OutputModeReplace:
		return "Call " + output.ToolName() + " with complete replacement markdown content for this maintained document."
	default:
		return "Use the generated output tool for this declaration."
	}
}

// loopOutputContextMode reports the write interface the model actually
// has for this output. A faceted output's spec-level mode is still
// replace — facets are the only declaration, so there is no authorable
// publish mode to contradict — but its generated tool takes projections
// rather than a document body. Reporting the spec mode here would pair
// "publish_output_*" with "mode: replace" in the same context block and
// leave the model to guess which one describes the call it should make.
func loopOutputContextMode(output looppkg.OutputSpec) string {
	if output.HasFacets() {
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
	return fmt.Sprintf("Replace the loop-declared maintained document output %q at %s. Pass the complete markdown body for the new current document state; root policy and indexing are handled by Thane. The body has a 96 KiB ceiling — the guarantee that this document always reads back whole in one call.", output.Name, output.Ref)
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

// truncateLoopOutputText keeps the head of an over-budget body on a
// rune boundary and marks the cut. Head only: the tail variant from the
// append-journal era went unused once working notes switched to
// whole-body rewrites, where what matters is what is being replaced.
func truncateLoopOutputText(s string, maxBytes int) (string, bool, int, int) {
	total := len(s)
	if total <= maxBytes {
		return s, false, total, total
	}
	if maxBytes <= 0 {
		return "", true, 0, total
	}
	end := maxBytes
	for end < len(s) && end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	out := s[:end] + "\n[truncated: output exceeded context budget]"
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
		dst[i].Facets = append([]looppkg.FacetSpec(nil), src[i].Facets...)
	}
	return dst
}
