package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// TestOwnOutputDocReadTruncatedRecordsNoReceipt: a document past even
// the privileged read budget comes back as a preview, and a preview is
// not the read a whole-body replacement is protected by. The owner tool
// writes at most 96 KiB, so only a legacy or externally grown document
// gets here — exactly the one whose unseen tail a blind replacement would
// discard.
func TestOwnOutputDocReadTruncatedRecordsNoReceipt(t *testing.T) {
	t.Parallel()
	store, _ := newReceiptLoopOutputStore(t)
	huge := strings.Repeat("Legacy content the owner never wrote through its tool. ", 3000) // ~165 KiB > ownOutputReadResultBytes
	seedDocument(t, store, "core:office.md", huge)
	docTools := documents.NewTools(store)
	outputs := officeOutputs()
	replace := buildLoopOutputTools(docTools, outputs)[0]
	read := ownDocRead(docTools, outputs)
	ctx := toolCallContext("loop-office", "loop-office-1-1")

	result, err := read.Handler(ctx, map[string]any{"ref": "core:office.md"})
	if err != nil {
		t.Fatalf("own-output read: %v", err)
	}
	if !strings.Contains(result, `"truncated": true`) {
		t.Fatalf("oversized read should truncate; the test proves nothing otherwise:\n%.300s", result)
	}
	_, err = replace.Handler(ctx, map[string]any{"body": "Shrunk from a preview."})
	var rejected *documents.MutationRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("replace after truncated read err = %v, want read-first refusal", err)
	}
	if got := readBody(t, store, "core:office.md"); !strings.Contains(got, "Legacy content") {
		t.Fatalf("refused replace changed the document")
	}
}
