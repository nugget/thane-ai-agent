package app

import (
	"context"
	"errors"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// commitLoopDefinition is the single durable-commit chokepoint every
// model-facing authoring tool routes through. With no store or runtime
// wired, persist and reconcile are no-ops, so these tests isolate the
// overlay-upsert behavior and the error contract callers depend on.

func TestCommitLoopDefinitionUpsertsOverlay(t *testing.T) {
	t.Parallel()

	reg := testLoopDefinitionRegistry(t)
	a := &App{loopDefinitionRegistry: reg}

	spec := looppkg.Spec{
		Name:       "room_watch",
		Task:       "Watch the room and surface noteworthy changes.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
	}
	updatedAt := time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC)
	if err := a.commitLoopDefinition(context.Background(), spec, updatedAt); err != nil {
		t.Fatalf("commitLoopDefinition: %v", err)
	}

	stored, ok := reg.Get("room_watch")
	if !ok {
		t.Fatal("committed definition not present in registry overlay")
	}
	if stored.Task != spec.Task {
		t.Fatalf("stored task = %q, want %q", stored.Task, spec.Task)
	}
}

func TestCommitLoopDefinitionPropagatesImmutableError(t *testing.T) {
	t.Parallel()

	// testLoopDefinitionRegistry seeds a config-owned base loop named
	// "metacog_like"; committing over it must surface a typed
	// ImmutableDefinitionError. The chokepoint returns the upsert error
	// bare (no wrapping) precisely so callers can errors.As it to map a
	// 409/Conflict — guard that contract here.
	reg := testLoopDefinitionRegistry(t)
	a := &App{loopDefinitionRegistry: reg}

	spec := looppkg.Spec{
		Name:       "metacog_like",
		Task:       "Try to shadow a config-owned definition.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
	}
	err := a.commitLoopDefinition(context.Background(), spec, time.Now().UTC())
	if err == nil {
		t.Fatal("expected error committing over a config-owned definition")
	}
	var immutable *looppkg.ImmutableDefinitionError
	if !errors.As(err, &immutable) {
		t.Fatalf("error = %v, want it to unwrap to *ImmutableDefinitionError", err)
	}
}

func TestCommitLoopDefinitionRejectsInvalidSpec(t *testing.T) {
	t.Parallel()

	// A structurally invalid spec (corrupt output ref — the #1068 shape)
	// must be rejected at commit time by the upsert's ValidatePersistable,
	// not silently stored.
	reg := testLoopDefinitionRegistry(t)
	a := &App{loopDefinitionRegistry: reg}

	spec := looppkg.Spec{
		Name:       "broken_output",
		Task:       "Maintain a doc.",
		Operation:  looppkg.OperationService,
		Completion: looppkg.CompletionNone,
		Outputs: []looppkg.OutputSpec{{
			Name: "doc",
			Type: looppkg.OutputTypeMaintainedDocument,
			Mode: looppkg.OutputModeReplace,
			Ref:  "---\ntitle: \"corrupt\"\n---\n\nbody",
		}},
	}
	if err := a.commitLoopDefinition(context.Background(), spec, time.Now().UTC()); err == nil {
		t.Fatal("expected commit to reject a spec with a content-corrupted output ref")
	}
	if _, ok := reg.Get("broken_output"); ok {
		t.Fatal("invalid definition must not be stored in the overlay")
	}
}
