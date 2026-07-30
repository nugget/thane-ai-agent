package app

import (
	"context"
	"fmt"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// Frontmatter keys stamped on loop-managed documents. The contract
// package owns the names (the create-time scaffold stamps the same
// keys); see the [looppkg.OutputAudienceFrontmatterKey] doc for what
// each key does.
const (
	loopOutputAudienceKey  = looppkg.OutputAudienceFrontmatterKey
	loopOutputManagedByKey = looppkg.OutputManagedByFrontmatterKey
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
		description := field.Guidance + looppkg.FormatGuidance(field.Format)
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
			payload, err := output.FacetPayloadFromArgs(args)
			if err != nil {
				return "", err
			}
			if err := output.ValidateFacetPayload(payload); err != nil {
				return "", err
			}
			body := output.RenderFacetDocument(payload)
			result, err := store.Write(ctx, documents.WriteArgs{
				Ref:         output.Ref,
				Body:        &body,
				Frontmatter: loopOutputFrontmatter(output),
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
		"Publish the loop-declared output %q at %s. Pass every projection in one call (%s): they are written together so no reader sees one projection describing a state another has moved past. Section structure and headings are rendered automatically — pass only the content of each projection, never its heading. Each projection has its own size budget and an over-budget value is rejected rather than trimmed; full has a 96 KiB ceiling — the guarantee that this document always reads back whole in one call.",
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
	if err := looppkg.ValidateOutputBodySize(body); err != nil {
		return nil, fmt.Errorf("notes: %w", err)
	}
	return store.Write(ctx, documents.WriteArgs{
		Ref:         notes.Ref,
		Body:        &body,
		Frontmatter: loopOutputFrontmatter(notes),
	})
}

// loopOutputFrontmatter returns the ownership stamp for an output's
// document, applied on every write rather than only the first. Stamping
// the audience is what makes an internal declaration real: the
// exclusion in search and tagged-guidance injection reads this key off
// the document, not the loop spec, so a rewrite that dropped it would
// quietly publish the loop's private thinking. managed_by rides along
// so an edit arriving through a general document tool can be pointed
// back at the owning interface.
func loopOutputFrontmatter(output looppkg.OutputSpec) map[string][]string {
	return map[string][]string{
		loopOutputAudienceKey:  {string(output.EffectiveAudience())},
		loopOutputManagedByKey: {output.ToolName()},
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
