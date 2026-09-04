package documents

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

func newReceiptLifecycleTools(t *testing.T) (*Tools, *Store, *revisionMutationBackend) {
	t.Helper()
	root := t.TempDir()
	backend := &revisionMutationBackend{root: root, contents: make(map[string]string)}
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStoreWithOptions(db, map[string]string{"projects": root}, nil, StoreOptions{
		RootWriters:  map[string]RootWriter{"projects": backend},
		RootRevisers: map[string]RootReviser{"projects": backend},
	})
	if err != nil {
		t.Fatalf("NewStoreWithOptions: %v", err)
	}
	return NewTools(store), store, backend
}

// TestDeleteAdvancesReceiptToAbsent pins the receipt lifecycle across a
// deletion the caller performed itself: create, delete, create again at
// the same ref, all from one scope. Before the fix the pre-deletion
// receipt stayed pinned and the second create was refused as "deleted
// after this loop last read it" — a report from production, three hours
// after the loop's own doc_delete.
func TestDeleteAdvancesReceiptToAbsent(t *testing.T) {
	t.Parallel()
	tools, store, _ := newReceiptLifecycleTools(t)
	ctx := t.Context()
	const (
		ref   = "projects:ranch-operations/_writepath-probe.md"
		scope = "loop:hor_conditions"
	)

	if _, err := tools.Write(ctx, WriteArgs{Ref: ref, Body: stringPtr("Probe."), ReceiptScope: scope}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := tools.Delete(ctx, DeleteArgs{Ref: ref, ReceiptScope: scope}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if revision, ok := tools.revisionReceipt(scope, ref); !ok || revision != revisionAbsent {
		t.Fatalf("receipt after delete = %q, %v; want %q", revision, ok, revisionAbsent)
	}

	recreatedJSON, err := tools.Write(ctx, WriteArgs{Ref: ref, Body: stringPtr("Probe again."), ReceiptScope: scope})
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}
	var recreated modelMutationResult
	if err := json.Unmarshal([]byte(recreatedJSON), &recreated); err != nil {
		t.Fatalf("unmarshal recreate: %v\n%s", err, recreatedJSON)
	}
	if !recreated.Applied {
		t.Fatalf("recreate after own delete was refused: %s", recreatedJSON)
	}
	record, err := store.Read(ctx, ref)
	if err != nil {
		t.Fatalf("read recreated: %v", err)
	}
	if !strings.Contains(record.Body, "Probe again.") {
		t.Fatalf("recreated body = %q", record.Body)
	}
}

// TestMoveAdvancesSourceReceiptToAbsent: the mover's receipt for the
// source ref must not outlive the document at that ref.
func TestMoveAdvancesSourceReceiptToAbsent(t *testing.T) {
	t.Parallel()
	tools, _, _ := newReceiptLifecycleTools(t)
	ctx := t.Context()
	const scope = "loop:curator"

	if _, err := tools.Write(ctx, WriteArgs{Ref: "projects:draft.md", Body: stringPtr("Draft."), ReceiptScope: scope}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := tools.Move(ctx, MoveArgs{Ref: "projects:draft.md", DestinationRef: "projects/final.md"[0:0] + "projects:final.md", ReceiptScope: scope}); err != nil {
		t.Fatalf("move: %v", err)
	}
	if revision, ok := tools.revisionReceipt(scope, "projects:draft.md"); !ok || revision != revisionAbsent {
		t.Fatalf("source receipt after move = %q, %v; want %q", revision, ok, revisionAbsent)
	}
	if _, ok := tools.revisionReceipt(scope, "projects:final.md"); ok {
		t.Fatalf("destination receipt was invented by the move; a later write must read or conflict first")
	}
}

// TestRejectionIsErrorSurfacesTypedError covers the error contract a
// loop's generated output tool relies on: with RejectionIsError set, a
// refusal is a *MutationRejectedError whose Result is the inline payload
// and whose text carries the message plus that payload; without it the
// same refusal is the inline applied:false result, unchanged.
func TestRejectionIsErrorSurfacesTypedError(t *testing.T) {
	t.Parallel()
	tools, store, backend := newReceiptLifecycleTools(t)
	ctx := t.Context()
	const (
		ref   = "projects:home/garage-bays.md"
		scope = "loop:garage-bays"
	)
	if _, err := store.Write(ctx, WriteArgs{Ref: ref, Body: stringPtr("Bay 1 open.")}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Run("read-first refusal", func(t *testing.T) {
		payload, err := tools.Write(ctx, WriteArgs{Ref: ref, Body: stringPtr("Blind."), ReceiptScope: scope, StructuredTool: "replace_output_garage_bays", RejectionIsError: true})
		if payload != "" {
			t.Fatalf("payload = %q, want empty alongside the error", payload)
		}
		var rejected *MutationRejectedError
		if !errors.As(err, &rejected) {
			t.Fatalf("err = %v, want *MutationRejectedError", err)
		}
		if rejected.Action != "replace_output_garage_bays" || rejected.Ref != ref {
			t.Fatalf("rejected = %#v", rejected)
		}
		var conflict modelMutationConflict
		if err := json.Unmarshal([]byte(rejected.Result), &conflict); err != nil {
			t.Fatalf("Result is not the inline payload: %v\n%s", err, rejected.Result)
		}
		if conflict.Applied || conflict.Message != rejected.Message || !strings.Contains(conflict.Message, "no record of this loop reading "+ref) {
			t.Fatalf("conflict = %#v", conflict)
		}
		if !strings.HasPrefix(rejected.Error(), rejected.Message) || !strings.Contains(rejected.Error(), `"applied": false`) {
			t.Fatalf("Error() = %q, want message then payload", rejected.Error())
		}
		record, err := store.Read(ctx, ref)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !strings.Contains(record.Body, "Bay 1 open.") {
			t.Fatalf("refused write changed the document: %q", record.Body)
		}
	})

	t.Run("stale receipt refusal carries the diff", func(t *testing.T) {
		if _, err := tools.Read(ctx, RefArgs{Ref: ref, ReceiptScope: scope}); err != nil {
			t.Fatalf("read: %v", err)
		}
		if err := backend.Write(ctx, "home/garage-bays.md", "Bay 2 open.", "operator update"); err != nil {
			t.Fatalf("external update: %v", err)
		}
		_, err := tools.Write(ctx, WriteArgs{Ref: ref, Body: stringPtr("Stale."), ReceiptScope: scope, StructuredTool: "replace_output_garage_bays", RejectionIsError: true})
		var rejected *MutationRejectedError
		if !errors.As(err, &rejected) {
			t.Fatalf("err = %v, want *MutationRejectedError", err)
		}
		if !strings.Contains(rejected.Message, "changed after this loop last read it") || !strings.Contains(rejected.Result, "Bay 2 open.") {
			t.Fatalf("rejected = %#v, want conflict with intervening diff", rejected)
		}
		// The receipt advanced with the refusal, so the reconciled retry
		// lands without another read.
		retryJSON, err := tools.Write(ctx, WriteArgs{Ref: ref, Body: stringPtr("Bays 1 and 2 open."), ReceiptScope: scope, StructuredTool: "replace_output_garage_bays", RejectionIsError: true})
		if err != nil {
			t.Fatalf("retry: %v", err)
		}
		var retried modelMutationResult
		if err := json.Unmarshal([]byte(retryJSON), &retried); err != nil {
			t.Fatalf("unmarshal retry: %v", err)
		}
		if !retried.Applied {
			t.Fatalf("retry applied = false: %s", retryJSON)
		}
	})

	t.Run("inline contract is unchanged by default", func(t *testing.T) {
		payload, err := tools.Write(ctx, WriteArgs{Ref: ref, Body: stringPtr("Blind."), ReceiptScope: "loop:someone-else"})
		if err != nil {
			t.Fatalf("default refusal returned an error: %v", err)
		}
		var conflict modelMutationConflict
		if err := json.Unmarshal([]byte(payload), &conflict); err != nil {
			t.Fatalf("unmarshal: %v\n%s", err, payload)
		}
		if conflict.Applied {
			t.Fatalf("blind write applied: %s", payload)
		}
	})
}
