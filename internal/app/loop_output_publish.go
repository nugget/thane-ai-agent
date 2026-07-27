package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/outputtargets"
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
		properties[field.Key] = facetArgumentSchema(field)
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
			payload, err := output.FacetPayloadFromArgs(args)
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

// facetArgumentSchema returns the JSON-Schema fragment for one publish
// argument.
//
// A reading projection is a string with a budget. A target facet is not:
// the surface it is cut for has named slots with their own types and
// limits, so the argument is that slot object, taken whole from the
// registry. The model then fills a watch complication by naming its
// slots rather than by hand-encoding JSON into a string — the shape is
// something it can be told, so it is.
func facetArgumentSchema(field looppkg.FacetField) map[string]any {
	if field.Target != "" {
		if target, ok := outputtargets.Lookup(field.Target); ok {
			schema := target.Schema()
			schema["description"] = fmt.Sprintf(
				"The %s. %s Every publish replaces the whole set: slots you omit are cleared. %s",
				target.Title, target.Summary, target.Binding,
			)
			return schema
		}
	}
	description := field.Guidance + looppkg.FormatGuidance(field.Format)
	if field.MaxRunes > 0 {
		description = fmt.Sprintf("%s Maximum %d characters.", description, field.MaxRunes)
	}
	return map[string]any{
		"type":        "string",
		"description": description,
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
