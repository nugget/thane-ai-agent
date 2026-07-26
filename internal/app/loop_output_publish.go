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

// buildTieredPublishTool generates the publish interface for one tiered
// maintained document. The tool advertises one typed argument per
// declared projection plus the full body, validates the whole payload
// before writing anything, and renders the document itself — the model
// supplies content, never structure.
//
// notes is the loop's declared working-notes output when it has one.
// Its presence adds the optional note argument, so the argument exists
// only when there is somewhere for the note to land.
func buildTieredPublishTool(store *documents.Store, output looppkg.OutputSpec, notes *looppkg.OutputSpec) looppkg.RuntimeTool {
	fields := output.TierFields()
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
		properties["note"] = map[string]any{
			"type":        "string",
			"description": fmt.Sprintf("Optional: why this publish changed what it changed. Appended as a timestamped entry to this loop's working notes (%s) in the same call — the private record of how this understanding evolved, which no consumer surface reads. Revision history already records what changed; this is for why.", notes.Ref),
		}
	}

	return looppkg.RuntimeTool{
		Name:               output.ToolName(),
		Description:        tieredPublishDescription(output, notes),
		SkipContentResolve: true,
		Parameters: map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   required,
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			payload, err := tierPayloadFromArgs(output, args)
			if err != nil {
				return "", err
			}
			if err := output.ValidateTierPayload(payload); err != nil {
				return "", err
			}
			body := output.RenderTierDocument(payload)
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
			if note, _ := args["note"].(string); strings.TrimSpace(note) != "" && notes != nil {
				noteResult, noteErr := appendLoopWorkingNote(ctx, store, *notes, note)
				switch {
				case noteErr != nil:
					// The document publish already succeeded, so this is
					// not a failed call to retry: report the partial
					// outcome instead of an error that would invite a
					// duplicate publish.
					published["note_error"] = fmt.Sprintf("%s was published, but the working note was not recorded: %v", output.Ref, noteErr)
				default:
					published["note_recorded"] = noteResult
				}
			}
			return marshalLoopOutputToolResult(published)
		},
	}
}

// tieredPublishDescription frames the publish interface for the model:
// what it owns, that structure is not the model's job, and that the
// projections move together.
func tieredPublishDescription(output looppkg.OutputSpec, notes *looppkg.OutputSpec) string {
	keys := make([]string, 0, 4)
	for _, field := range output.TierFields() {
		keys = append(keys, field.Key)
	}
	description := fmt.Sprintf(
		"Publish the loop-declared output %q at %s. Pass every projection in one call (%s): they are written together so no reader sees one projection describing a state another has moved past. Section structure and headings are rendered automatically — pass only the content of each projection, never its heading. Each projection has its own size budget and an over-budget value is rejected rather than trimmed.",
		output.Name, output.Ref, strings.Join(keys, ", "),
	)
	if notes != nil {
		description += " Pass note to record why this publish changed what it changed into the loop's working notes."
	}
	return description
}

// tierPayloadFromArgs reads the publish arguments into a payload,
// rejecting a non-string value with the argument name rather than
// silently treating it as empty.
func tierPayloadFromArgs(output looppkg.OutputSpec, args map[string]any) (looppkg.TierPayload, error) {
	var payload looppkg.TierPayload
	for _, field := range output.TierFields() {
		raw, present := args[field.Key]
		if !present {
			continue
		}
		value, ok := raw.(string)
		if !ok {
			return looppkg.TierPayload{}, fmt.Errorf("%s must be a string", field.Key)
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

// appendLoopWorkingNote appends one entry to a working-notes output,
// stamping the audience that keeps it out of consumer surfaces.
func appendLoopWorkingNote(ctx context.Context, store *documents.Store, notes looppkg.OutputSpec, entry string) (*documents.MutationResult, error) {
	return store.JournalUpdate(ctx, documents.JournalUpdateArgs{
		Ref:         notes.Ref,
		Entry:       entry,
		Window:      notes.JournalWindow,
		MaxWindows:  notes.MaxWindows,
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

// findWorkingNotesOutput returns the loop's internal-audience journal
// output, if it declared one. A loop may declare at most one working
// notes surface; the first is used when several are present.
func findWorkingNotesOutput(outputs []looppkg.OutputSpec) *looppkg.OutputSpec {
	for i := range outputs {
		if outputs[i].Type == looppkg.OutputTypeWorkingNotes {
			return &outputs[i]
		}
	}
	return nil
}
