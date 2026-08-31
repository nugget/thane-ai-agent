package app

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

var _ documents.RootWriter = (*documentRootProvenanceWriter)(nil)

func (w *documentRootProvenanceWriter) WriteIfRevision(ctx context.Context, filename, content, message, expectedRevision string) (string, error) {
	store, err := w.store()
	if err != nil {
		return "", err
	}
	revision, err := store.WriteIfRevision(ctx, w.storeFilename(filename), content, w.withTurnProvenance(ctx, message), expectedRevision)
	if err != nil {
		return "", err
	}
	return revision.Short, nil
}
