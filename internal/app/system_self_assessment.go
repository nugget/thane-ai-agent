package app

import (
	"context"
	"strings"
	"time"

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
	ref := ""
	for _, output := range spec.Outputs {
		if output.Type == looppkg.OutputTypeMaintainedDocument && strings.TrimSpace(output.Ref) != "" {
			ref = output.Ref
			break
		}
	}
	if ref == "" {
		return "", time.Time{}, nil
	}
	record, err := a.documentStore.Read(ctx, ref)
	if err != nil {
		if strings.Contains(err.Error(), "document not found") {
			return "", time.Time{}, nil
		}
		return "", time.Time{}, err
	}
	modified, _ := time.Parse(time.RFC3339, record.ModifiedAt)
	return record.Body, modified, nil
}
