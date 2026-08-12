package app

import (
	"context"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/documents"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/runtime/metacognitive"
)

// readSystemSelfAssessmentDocument is the read function behind
// [awareness.SystemSelfAssessmentProvider]: it resolves metacog's
// maintained document lazily from the definition registry — the spec
// owns where the state lives, so no ref is hardcoded to drift — and
// returns the body with its last-modified time.
//
// Every absent-is-normal state returns empty content and a nil error
// so the provider stays quiet instead of warning each turn: no
// metacognitive definition (installs run without it), no declared
// maintained document, or a document that has never been written.
// Only a genuinely failed read reports an error.
func (a *App) readSystemSelfAssessmentDocument(ctx context.Context) (string, time.Time, error) {
	if a == nil || a.documentStore == nil {
		return "", time.Time{}, nil
	}
	spec, ok := a.loopDefinitionRegistry.Get(metacognitive.DefinitionName)
	if !ok {
		return "", time.Time{}, nil
	}
	// Select the verdict-bearing output: the maintained document that
	// declares a status_line facet. Falling back to the first maintained
	// document keeps the resolver working against a pre-facet spec,
	// where the provider stays quiet on content anyway.
	ref := ""
	for _, output := range spec.Outputs {
		if output.Type != looppkg.OutputTypeMaintainedDocument || strings.TrimSpace(output.Ref) == "" {
			continue
		}
		if ref == "" {
			ref = output.Ref
		}
		for _, facet := range output.Facets {
			if facet.Name == looppkg.OutputFacetStatusLine {
				ref = output.Ref
			}
		}
	}
	if ref == "" {
		return "", time.Time{}, nil
	}
	record, err := a.documentStore.Read(ctx, ref)
	if err != nil {
		if documents.IsNotFound(err) {
			return "", time.Time{}, nil
		}
		return "", time.Time{}, err
	}
	// Zero when unresolvable; the provider omits the age rather than
	// inventing one.
	modified, _ := documentUpdatedTime(record)
	return record.Body, modified, nil
}
