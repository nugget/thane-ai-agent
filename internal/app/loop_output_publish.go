package app

import (
	"context"
	"fmt"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// Frontmatter keys stamped on loop-managed documents. audience is the
// document layer's projection gate (an internal document stays out of
// search results and tagged-guidance injection); managed_by records
// which generated tool owns the document's structure, so a later edit
// arriving through a general document tool can be pointed back at the
// owning interface instead of silently competing with it.
const (
	loopOutputAudienceKey  = "audience"
	loopOutputManagedByKey = "managed_by"
)

// buildFacetPublishTool generates the publish interface for one faceted
// maintained document. The tool advertises one typed argument per
// declared projection plus the full body, validates the whole payload
// before writing anything, and renders the document itself — the model
// supplies content, never structure.
//
// notes is the loop's declared working-notes output when it has one.
// Its presence adds the optional notes argument, so the argument exists
// only when there is somewhere for the notes to land.
func buildFacetPublishTool(store *documents.Store, output looppkg.OutputSpec, notes *looppkg.OutputSpec) looppkg.RuntimeTool {
	fields := output.FacetFields()
	properties := make(map[string]any, len(fields)+1)
	required := make([]string, 0, len(fields))
	for _, field := range fields {
		description := field.Guidance
		if field.MaxRunes > 0 {
			description = fmt.Sprintf("%s Maximum %d characters.", description, field.MaxRunes)
		}
		properties[field.Key] = map[string]any{
			"type":        "string",
			"description": description,
		}
		required = append(required, field.Key)
	}
	if notes != nil {
		properties["notes"] = map[string]any{
			"type":        "string",
			"description": fmt.Sprintf("Optional: your working notes (%s) as they now stand, rewritten in the same call — private thinking no consumer surface reads. Hold what you currently believe: theories, what an experiment is showing, what you expect next, what would change your mind. This replaces the whole body rather than adding to it, so carry forward what still holds and drop what you no longer think. Revision history already records what changed; this is for what you make of it.", notes.Ref),
		}
	}

	return looppkg.RuntimeTool{
		Name:               output.ToolName(),
		Description:        facetPublishDescription(output, notes),
		SkipContentResolve: true,
		Parameters: map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   required,
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			payload, err := facetPayloadFromArgs(output, args)
			if err != nil {
				return "", err
			}
			if err := output.ValidateFacetPayload(payload); err != nil {
				return "", err
			}
			body := output.RenderFacetDocument(payload)
			result, err := store.Write(ctx, documents.WriteArgs{
				Ref:  output.Ref,
				Body: &body,
				Frontmatter: map[string][]string{
					loopOutputAudienceKey:  {string(output.EffectiveAudience())},
					loopOutputManagedByKey: {output.ToolName()},
				},
			})
			if err != nil {
				return "", err
			}

			published := map[string]any{"published": result}
			if note, _ := args["notes"].(string); strings.TrimSpace(note) != "" && notes != nil {
				noteResult, noteErr := writeLoopWorkingNotes(ctx, store, *notes, note)
				switch {
				case noteErr != nil:
					// The document publish already succeeded, so this is
					// not a failed call to retry: report the partial
					// outcome instead of an error that would invite a
					// duplicate publish.
					published["notes_error"] = fmt.Sprintf("%s was published, but the working notes were not updated: %v", output.Ref, noteErr)
				default:
					published["notes_written"] = noteResult
				}
			}
			return marshalLoopOutputToolResult(published)
		},
	}
}

// facetPublishDescription frames the publish interface for the model:
// what it owns, that structure is not the model's job, and that the
// projections move together.
func facetPublishDescription(output looppkg.OutputSpec, notes *looppkg.OutputSpec) string {
	keys := make([]string, 0, 4)
	for _, field := range output.FacetFields() {
		keys = append(keys, field.Key)
	}
	description := fmt.Sprintf(
		"Publish the loop-declared output %q at %s. Pass every projection in one call (%s): they are written together so no reader sees one projection describing a state another has moved past. Section structure and headings are rendered automatically — pass only the content of each projection, never its heading. Each projection has its own size budget and an over-budget value is rejected rather than trimmed.",
		output.Name, output.Ref, strings.Join(keys, ", "),
	)
	if notes != nil {
		description += " Pass notes to rewrite this loop's private working notes in the same call — what you currently believe, not an entry about this change."
	}
	return description
}

// facetPayloadFromArgs reads the publish arguments into a payload,
// rejecting a non-string value with the argument name rather than
// silently treating it as empty.
func facetPayloadFromArgs(output looppkg.OutputSpec, args map[string]any) (looppkg.FacetPayload, error) {
	var payload looppkg.FacetPayload
	for _, field := range output.FacetFields() {
		raw, present := args[field.Key]
		if !present {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return looppkg.FacetPayload{}, fmt.Errorf("%s must be a string", field.Key)
		}
		switch field.Key {
		case "status_line":
			payload.StatusLine = value
		case "teaser":
			payload.Teaser = value
		case "digest":
			payload.Digest = value
		case "full":
			payload.Full = value
		}
	}
	return payload, nil
}

// writeLoopWorkingNotes replaces a working-notes body, stamping the
// audience that keeps it out of consumer surfaces on every write —
// notes hold a current view, so each one supersedes the last.
func writeLoopWorkingNotes(ctx context.Context, store *documents.Store, notes looppkg.OutputSpec, body string) (*documents.MutationResult, error) {
	return store.Write(ctx, documents.WriteArgs{
		Ref:         notes.Ref,
		Body:        &body,
		Frontmatter: loopOutputAudienceFrontmatter(notes),
	})
}

// loopOutputAudienceFrontmatter returns the audience stamp for an
// output's document, or nil when the output is published (the document
// layer's default). Stamping internal is what makes an internal
// declaration real: the exclusion in search and tagged-guidance
// injection reads this key, not the loop spec.
func loopOutputAudienceFrontmatter(output looppkg.OutputSpec) map[string][]string {
	if output.EffectiveAudience() != looppkg.OutputAudienceInternal {
		return nil
	}
	return map[string][]string{
		loopOutputAudienceKey: {string(looppkg.OutputAudienceInternal)},
	}
}

// findWorkingNotesOutput returns the loop's working-notes output, if it
// declared one. Spec validation permits at most one, so the first match
// is the only match.
func findWorkingNotesOutput(outputs []looppkg.OutputSpec) *looppkg.OutputSpec {
	for i := range outputs {
		if outputs[i].Type == looppkg.OutputTypeWorkingNotes {
			return &outputs[i]
		}
	}
	return nil
}
