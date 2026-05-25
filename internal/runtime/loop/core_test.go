package loop

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestOperationCoreShapeMatchesContainer covers the validation
// contract: core is shape-identical to container — no Task, no
// sleep envelope, no execution hooks, no outputs.
func TestOperationCoreShapeMatchesContainer(t *testing.T) {
	t.Parallel()

	t.Run("minimal core spec is valid", func(t *testing.T) {
		spec := &Spec{Name: "core", Operation: OperationCore}
		if err := spec.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("core rejects task", func(t *testing.T) {
		spec := &Spec{Name: "core", Operation: OperationCore, Task: "do something"}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "cannot set task") {
			t.Fatalf("Validate: %v, want core task rejection", err)
		}
	})

	t.Run("core rejects outputs", func(t *testing.T) {
		spec := &Spec{
			Name:      "core",
			Operation: OperationCore,
			Outputs: []OutputSpec{{
				Name: "doc", Ref: "kb:test.md",
				Type: OutputTypeMaintainedDocument, Mode: OutputModeReplace,
			}},
		}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "cannot declare outputs") {
			t.Fatalf("Validate: %v, want core outputs rejection", err)
		}
	})
}

// TestRegistryRefusesSecondCore covers the singleton invariant:
// the second OperationCore registration returns a typed
// MultipleCoreError naming the existing root.
func TestRegistryRefusesSecondCore(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	first, err := New(Config{Name: "core", Operation: OperationCore}, Deps{})
	if err != nil {
		t.Fatalf("new first core: %v", err)
	}
	if err := r.Register(first); err != nil {
		t.Fatalf("register first core: %v", err)
	}

	second, err := New(Config{Name: "imposter", Operation: OperationCore}, Deps{})
	if err != nil {
		t.Fatalf("new second core: %v", err)
	}
	err = r.Register(second)
	if err == nil {
		t.Fatal("second core register succeeded; want MultipleCoreError")
	}
	var dupe *MultipleCoreError
	if !errors.As(err, &dupe) {
		t.Fatalf("err = %v, want *MultipleCoreError", err)
	}
	if dupe.ExistingName != "core" {
		t.Errorf("ExistingName = %q, want core", dupe.ExistingName)
	}
	if dupe.ExistingID != first.ID() {
		t.Errorf("ExistingID = %q, want %q", dupe.ExistingID, first.ID())
	}
}

// TestRegistryCoreAccessor returns the registered core when present
// and nil otherwise.
func TestRegistryCoreAccessor(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	if got := r.Core(); got != nil {
		t.Errorf("Core() = %v, want nil for empty registry", got)
	}

	core, err := New(Config{Name: "core", Operation: OperationCore}, Deps{})
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	if err := r.Register(core); err != nil {
		t.Fatalf("register core: %v", err)
	}
	if got := r.Core(); got != core {
		t.Errorf("Core() returned %v, want the registered core", got)
	}
}

// TestRegistryStopLoopRefusesCore guards the graph root: even
// though core is structurally a container, the operator-facing
// kill switch refuses to stop it. ShutdownAll still tears it down
// (covered by the existing ShutdownAll tests).
func TestRegistryStopLoopRefusesCore(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	core, err := New(Config{Name: "core", Operation: OperationCore}, Deps{})
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	if err := r.Register(core); err != nil {
		t.Fatalf("register core: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := core.Start(ctx); err != nil {
		t.Fatalf("start core: %v", err)
	}

	err = r.StopLoop(core.ID())
	if err == nil {
		t.Fatal("StopLoop succeeded on core; want refusal")
	}
	if !strings.Contains(err.Error(), "cannot stop core") {
		t.Errorf("err = %v, should mention 'cannot stop core'", err)
	}
	if r.Get(core.ID()) == nil {
		t.Error("core was deregistered despite the refusal")
	}
}

// TestCoreInheritanceContributesToDescendants verifies core acts
// like container for the inheritance walk — its tags / subscriptions
// flow down to descendants.
func TestCoreInheritanceContributesToDescendants(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	core, err := New(Config{
		Name:      "core",
		Operation: OperationCore,
		Tags:      []string{"system"},
		Subscriptions: []EntitySubscription{
			{EntityID: "binary_sensor.heartbeat"},
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	if err := r.Register(core); err != nil {
		t.Fatalf("register core: %v", err)
	}
	leaf, err := New(Config{
		Name:     "child",
		Task:     "t",
		ParentID: core.ID(),
		Tags:     []string{"own"},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	tags := r.EffectiveTags(leaf.ID())
	hasCore := false
	for _, tag := range tags {
		if tag.From == "core" && tag.Tag == "system" {
			hasCore = true
		}
	}
	if !hasCore {
		t.Errorf("EffectiveTags = %+v, want one entry inheriting 'system' from core", tags)
	}

	subs := r.EffectiveSubscriptions(leaf.ID())
	if len(subs) != 1 || subs[0].From != "core" || subs[0].EntityID != "binary_sensor.heartbeat" {
		t.Errorf("EffectiveSubscriptions = %+v, want one entry from core", subs)
	}
}
