package documents

import (
	"strings"
	"testing"
)

// TestReceiptRequiresWholeWithholdsReceiptOnTruncatedRead pins the two
// read contracts side by side: the standard read records a receipt even
// when its result is cut to a preview, and a caller that reads in order
// to replace the whole body can ask for the receipt to be withheld in
// that case.
func TestReceiptRequiresWholeWithholdsReceiptOnTruncatedRead(t *testing.T) {
	t.Parallel()
	tools, store, _ := newReceiptLifecycleTools(t)
	ctx := t.Context()
	const ref = "projects:big.md"
	big := strings.Repeat("x", 4096)
	if _, err := store.Write(ctx, WriteArgs{Ref: ref, Body: &big}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	payload, err := tools.ReadWithResultBudget(ctx, RefArgs{Ref: ref, ReceiptScope: "loop:whole", ReceiptRequiresWhole: true}, 1024)
	if err != nil {
		t.Fatalf("truncated whole read: %v", err)
	}
	if !strings.Contains(payload, `"truncated": true`) {
		t.Fatalf("read under a 1 KiB budget should truncate: %.200s", payload)
	}
	if _, ok := tools.revisionReceipt("loop:whole", ref); ok {
		t.Fatalf("truncated read recorded a receipt despite ReceiptRequiresWhole")
	}

	if _, err := tools.ReadWithResultBudget(ctx, RefArgs{Ref: ref, ReceiptScope: "loop:standard"}, 1024); err != nil {
		t.Fatalf("truncated standard read: %v", err)
	}
	if _, ok := tools.revisionReceipt("loop:standard", ref); !ok {
		t.Fatalf("standard truncated read no longer records a receipt; that contract was meant to stay")
	}

	if _, err := tools.ReadWithResultBudget(ctx, RefArgs{Ref: ref, ReceiptScope: "loop:whole", ReceiptRequiresWhole: true}, 1<<20); err != nil {
		t.Fatalf("whole read: %v", err)
	}
	if _, ok := tools.revisionReceipt("loop:whole", ref); !ok {
		t.Fatalf("a read that returned the whole document should record a receipt")
	}
}
