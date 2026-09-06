package documents

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestGuardRefusesBakedDeltaOnEveryAuthorPath pins that the guard sits
// on the author's text rather than on one tool. doc_write, doc_edit and
// doc_journal_update each supply prose through a different field, and a
// guard installed on only one of them is a hole an author reaches by
// picking a different tool.
func TestGuardRefusesBakedDeltaOnEveryAuthorPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const rotten = "## Evidence\n\n- room Office since -513s\n"

	t.Run("doc_write", func(t *testing.T) {
		t.Parallel()
		store, _ := newMutationStore(t)
		_, err := store.Write(ctx, WriteArgs{Ref: "kb:w.md", Title: "W", Body: stringPtr(rotten)})
		assertBakedDeltaRefusal(t, err, "-513s")
	})

	t.Run("doc_write journal entry", func(t *testing.T) {
		t.Parallel()
		store, _ := newMutationStore(t)
		_, err := store.Write(ctx, WriteArgs{Ref: "kb:wj.md", Title: "WJ", JournalEntry: "checked in -2h2m"})
		assertBakedDeltaRefusal(t, err, "-2h2m")
	})

	t.Run("doc_edit append_body", func(t *testing.T) {
		t.Parallel()
		store, _ := newMutationStore(t)
		if _, err := store.Write(ctx, WriteArgs{Ref: "kb:e.md", Title: "E", Body: stringPtr("clean\n")}); err != nil {
			t.Fatalf("seed: %v", err)
		}
		_, err := store.Edit(ctx, EditArgs{Ref: "kb:e.md", Mode: "append_body", Body: "arrived -2h45m\n"})
		assertBakedDeltaRefusal(t, err, "-2h45m")
	})

	t.Run("doc_journal_update", func(t *testing.T) {
		t.Parallel()
		store, _ := newMutationStore(t)
		_, err := store.JournalUpdate(ctx, JournalUpdateArgs{Ref: "kb:j.md", Title: "J", Entry: "departed -2h20m"})
		assertBakedDeltaRefusal(t, err, "-2h20m")
	})
}

// TestGuardScansOnlyWhatTheWriteSupplies is the property that keeps the
// guard from being a trap. A document that already carries a baked
// delta must stay writable: scanning the merged result would refuse
// every future write on account of prose the author did not touch, and
// the loop whose document rotted would be the one loop unable to
// repair it.
func TestGuardScansOnlyWhatTheWriteSupplies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := newMutationStore(t)

	// Seed a rotted body the way production got one — before the guard
	// existed — by writing it through the file path the guard does not
	// sit on.
	seeded := "---\ntitle: \"Rotted\"\n---\n\n## Evidence\n\n- room Office since -513s\n"
	if err := store.writeDocumentFile(ctx, "kb", "rotted.md", seeded); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// An unrelated edit to the same document must succeed: the author is
	// supplying clean prose, whatever the rest of the file still says.
	result, err := store.Edit(ctx, EditArgs{
		Ref:  "kb:rotted.md",
		Mode: "upsert_section",
		// Deliberately no baked delta in the supplied text.
		Heading: "Status",
		Section: "Status",
		Body:    "Home since 12:06 CDT.\n",
	})
	if err != nil {
		t.Fatalf("edit of an already-rotted document was refused: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("clean supplied prose produced warnings: %v", result.Warnings)
	}
}

// TestGuardWarnsWithoutRefusingNarrativeTime pins the tier split: the
// write lands, and the author is told what will go stale.
func TestGuardWarnsWithoutRefusingNarrativeTime(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := newMutationStore(t)

	result, err := store.Write(ctx, WriteArgs{
		Ref:   "kb:narrative.md",
		Title: "N",
		Body:  stringPtr("Awaiting the sensor installation tomorrow morning.\n"),
	})
	if err != nil {
		t.Fatalf("narrative relative time must warn, not refuse: %v", err)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want exactly one", result.Warnings)
	}
	for _, want := range []string{"tomorrow", "line 1", "{{delta:"} {
		if !strings.Contains(result.Warnings[0], want) {
			t.Errorf("warning missing %q: %s", want, result.Warnings[0])
		}
	}
}

// TestGuardIgnoresBodyOnModesThatDiscardIt pins that an inert argument
// cannot refuse a write. A body passed alongside mode "metadata" never
// reaches the document, so scanning it would refuse a write over text
// the store was about to throw away.
func TestGuardIgnoresBodyOnModesThatDiscardIt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := newMutationStore(t)
	if _, err := store.Write(ctx, WriteArgs{Ref: "kb:m.md", Title: "M", Body: stringPtr("clean\n")}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := store.Edit(ctx, EditArgs{
		Ref:   "kb:m.md",
		Mode:  "metadata",
		Title: "Retitled",
		Body:  "arrived -2h45m",
	}); err != nil {
		t.Fatalf("metadata edit refused over a discarded body: %v", err)
	}
}

// TestGuardExemptsFencedAndInlineCode pins the escape hatch the refusal
// message promises. An author quoting tool output or documenting the
// delta format has somewhere to put it.
func TestGuardExemptsFencedAndInlineCode(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, _ := newMutationStore(t)

	body := "Write `(-127s)` rather than a bare phrase.\n\n```json\n{\"room_since\": \"-513s\"}\n```\n"
	result, err := store.Write(ctx, WriteArgs{Ref: "kb:doc.md", Title: "D", Body: stringPtr(body)})
	if err != nil {
		t.Fatalf("code regions must not be scanned: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", result.Warnings)
	}
}

func assertBakedDeltaRefusal(t *testing.T, err error, wantText string) {
	t.Helper()
	var refusal *BakedDeltaRefusedError
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want *BakedDeltaRefusedError", err)
	}
	if !strings.Contains(refusal.Error(), wantText) {
		t.Errorf("refusal does not name %q: %s", wantText, refusal.Error())
	}
	if !strings.Contains(refusal.Error(), "{{delta:") {
		t.Errorf("refusal must name the fix: %s", refusal.Error())
	}
	if !strings.Contains(refusal.Error(), "backticks") {
		t.Errorf("refusal must name the escape hatch: %s", refusal.Error())
	}
}
