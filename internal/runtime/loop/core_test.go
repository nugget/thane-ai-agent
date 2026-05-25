package loop

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestLoopIsCore covers the IsCore predicate that captures the
// "container with the well-known name" marker. It's the single
// source of truth that every other core check defers to.
func TestLoopIsCore(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		loopName  string
		operation Operation
		want      bool
	}{
		{"named container is core", CoreLoopName, OperationContainer, true},
		{"container named other is not core", "home_automation", OperationContainer, false},
		{"service named 'core' is not core", CoreLoopName, OperationService, false},
		{"request_reply named 'core' is not core", CoreLoopName, OperationRequestReply, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Name: tc.loopName, Operation: tc.operation}
			if tc.operation != OperationContainer {
				cfg.Task = "t"
			}
			l, err := New(cfg, Deps{Runner: &noopRunner{}})
			if err != nil {
				t.Fatalf("new: %v", err)
			}
			if got := l.IsCore(); got != tc.want {
				t.Errorf("IsCore() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestCoreUsesContainerValidation confirms core is shape-identical
// to any other container — same validateContainerShape contract.
// Container validation rejects task/outputs whether or not the loop
// is the core; the name doesn't change the shape.
func TestCoreUsesContainerValidation(t *testing.T) {
	t.Parallel()

	t.Run("minimal core spec is valid", func(t *testing.T) {
		spec := &Spec{Name: CoreLoopName, Operation: OperationContainer}
		if err := spec.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("core rejects task like any container", func(t *testing.T) {
		spec := &Spec{Name: CoreLoopName, Operation: OperationContainer, Task: "do something"}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "cannot set task") {
			t.Fatalf("Validate: %v, want container task rejection", err)
		}
	})
}

// TestRegistryRefusesSecondCore covers the singleton invariant:
// a second core loop (container named CoreLoopName) registration
// returns a typed MultipleCoreError. Two containers with the same
// non-core name would also be flagged by the registry's name
// uniqueness elsewhere, but the core invariant is enforced
// explicitly because the bootstrap depends on it.
func TestRegistryRefusesSecondCore(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	first, err := New(Config{Name: CoreLoopName, Operation: OperationContainer}, Deps{})
	if err != nil {
		t.Fatalf("new first core: %v", err)
	}
	if err := r.Register(first); err != nil {
		t.Fatalf("register first core: %v", err)
	}

	// Note: this also gives the loop the well-known name. Anyone
	// trying to create a "second core" has to use the same name —
	// that's the marker.
	second, err := New(Config{Name: CoreLoopName, Operation: OperationContainer}, Deps{})
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
	if dupe.ExistingID != first.ID() {
		t.Errorf("ExistingID = %q, want %q", dupe.ExistingID, first.ID())
	}
}

// TestRegistryAcceptsContainerNamedNotCore confirms ordinary
// containers don't trigger the singleton check — only the
// CoreLoopName marker does.
func TestRegistryAcceptsContainerNamedNotCore(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	c1, err := New(Config{Name: "home_automation", Operation: OperationContainer}, Deps{})
	if err != nil {
		t.Fatalf("new c1: %v", err)
	}
	if err := r.Register(c1); err != nil {
		t.Fatalf("register c1: %v", err)
	}
	c2, err := New(Config{Name: "research", Operation: OperationContainer}, Deps{})
	if err != nil {
		t.Fatalf("new c2: %v", err)
	}
	if err := r.Register(c2); err != nil {
		t.Errorf("register c2: %v — two non-core containers should both register", err)
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

	core, err := New(Config{Name: CoreLoopName, Operation: OperationContainer}, Deps{})
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
	core, err := New(Config{Name: CoreLoopName, Operation: OperationContainer}, Deps{})
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

// TestRegisterDefaultParentsOrphansToCore covers the orphan
// attachment contract: a freshly registered loop with neither
// ParentID nor ParentName picks up the core's loop ID as its
// parent automatically — every spawn path (definition hydration,
// channel roots via SpawnLoop, delegate launches) goes through
// Register, so the graph always has a single root.
func TestRegisterDefaultParentsOrphansToCore(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	core, err := New(Config{Name: CoreLoopName, Operation: OperationContainer}, Deps{})
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	if err := r.Register(core); err != nil {
		t.Fatalf("register core: %v", err)
	}

	orphan, err := New(Config{Name: "orphan", Task: "t"}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new orphan: %v", err)
	}
	if err := r.Register(orphan); err != nil {
		t.Fatalf("register orphan: %v", err)
	}

	if got := orphan.ParentID(); got != core.ID() {
		t.Errorf("orphan ParentID = %q, want %q (core)", got, core.ID())
	}
}

// TestRegisterPreservesExplicitParentID guards against the
// "default-parent overrides explicit parent" bug: if the loop
// already has ParentID set, Register must not touch it.
func TestRegisterPreservesExplicitParentID(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	core, err := New(Config{Name: CoreLoopName, Operation: OperationContainer}, Deps{})
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	if err := r.Register(core); err != nil {
		t.Fatalf("register core: %v", err)
	}
	parent, err := New(Config{Name: "explicit_parent", Operation: OperationContainer}, Deps{})
	if err != nil {
		t.Fatalf("new explicit_parent: %v", err)
	}
	if err := r.Register(parent); err != nil {
		t.Fatalf("register explicit_parent: %v", err)
	}

	child, err := New(Config{
		Name:     "child",
		Task:     "t",
		ParentID: parent.ID(),
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new child: %v", err)
	}
	if err := r.Register(child); err != nil {
		t.Fatalf("register child: %v", err)
	}

	if got := child.ParentID(); got != parent.ID() {
		t.Errorf("child ParentID = %q, want %q (explicit parent, not core)", got, parent.ID())
	}
}

// TestRegisterLeavesUnresolvedParentNameAlone guards the
// late-reconcile path: a loop with ParentName set but no live
// parent yet should NOT default-parent to core, because doing so
// would lose the intent ("attach to outer when it comes up").
// Leaving ParentID empty lets a later reconcile bind it
// correctly. This is the regression test for the Copilot finding.
func TestRegisterLeavesUnresolvedParentNameAlone(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	core, err := New(Config{Name: CoreLoopName, Operation: OperationContainer}, Deps{})
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	if err := r.Register(core); err != nil {
		t.Fatalf("register core: %v", err)
	}

	// "outer" isn't registered yet — this loop declares intent to
	// be parented by it but Register can't resolve.
	pending, err := New(Config{
		Name:       "child",
		Task:       "t",
		ParentName: "outer",
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new pending: %v", err)
	}
	if err := r.Register(pending); err != nil {
		t.Fatalf("register pending: %v", err)
	}

	if got := pending.ParentID(); got != "" {
		t.Errorf("pending ParentID = %q, want empty (unresolved ParentName should not fall back to core)", got)
	}
}

// TestRegisterOrphanWithoutCoreLeavesParentEmpty covers the
// narrow startup window before [App.ensureCoreLoop] runs: orphan
// loops registered with no core present stay parentless rather
// than crashing or attaching to some accidental "first" loop.
// They'll reattach on the next reconcile once core is up.
func TestRegisterOrphanWithoutCoreLeavesParentEmpty(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	// No core registered.
	orphan, err := New(Config{Name: "orphan", Task: "t"}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new orphan: %v", err)
	}
	if err := r.Register(orphan); err != nil {
		t.Fatalf("register orphan: %v", err)
	}
	if got := orphan.ParentID(); got != "" {
		t.Errorf("orphan ParentID = %q, want empty when no core is registered", got)
	}
}

// TestCoreInheritanceContributesToDescendants verifies core
// participates in the cascade exactly like any container — its
// tags / subscriptions flow down to descendants through the same
// EffectiveTags / EffectiveSubscriptions walk. No core-specific
// code path; "core-ness" is just a name + a few registry rules.
func TestCoreInheritanceContributesToDescendants(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	core, err := New(Config{
		Name:      CoreLoopName,
		Operation: OperationContainer,
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
		if tag.From == CoreLoopName && tag.Tag == "system" {
			hasCore = true
		}
	}
	if !hasCore {
		t.Errorf("EffectiveTags = %+v, want one entry inheriting 'system' from core", tags)
	}

	subs := r.EffectiveSubscriptions(leaf.ID())
	if len(subs) != 1 || subs[0].From != CoreLoopName || subs[0].EntityID != "binary_sensor.heartbeat" {
		t.Errorf("EffectiveSubscriptions = %+v, want one entry from core", subs)
	}
}
