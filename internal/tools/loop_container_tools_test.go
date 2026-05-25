package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// containerTestRig wires the minimum slice of loop-intent and loop-
// definition tooling needed to exercise thane_create_container and its
// non-empty-delete refusal. It keeps a live loop registry around so the
// tool's parent_name resolution and the delete refusal can find children.
type containerTestRig struct {
	reg       *Registry
	defs      *looppkg.DefinitionRegistry
	loops     *looppkg.Registry
	persisted map[string]looppkg.Spec
	deleted   []string
}

func newContainerTestRig(t *testing.T) *containerTestRig {
	t.Helper()
	defs, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	loops := looppkg.NewRegistry()
	rig := &containerTestRig{
		reg:       NewEmptyRegistry(),
		defs:      defs,
		loops:     loops,
		persisted: make(map[string]looppkg.Spec),
	}
	launchDef := func(_ context.Context, name string, launch looppkg.Launch) (looppkg.LaunchResult, error) {
		spec, ok := defs.Get(name)
		if !ok {
			return looppkg.LaunchResult{}, &looppkg.UnknownDefinitionError{Name: name}
		}
		// Idempotency: if the loop is already registered, surface the
		// existing ID. Mirrors the LaunchDefinition guard in
		// internal/app so the tool layer can be retried safely.
		if existing := loops.GetByName(name); existing != nil {
			return looppkg.LaunchResult{LoopID: existing.ID(), Operation: spec.Operation, Detached: true}, nil
		}
		// Mirror the production launch path: build a Loop from the
		// spec, resolve ParentName → live ParentID, register it in
		// the live registry, and return the resulting loop_id.
		// Container loops skip the goroutine inside Loop.Start, so no
		// runner is needed.
		cfg := spec.ToConfig()
		if cfg.ParentName != "" && cfg.ParentID == "" {
			if parent := loops.GetByName(cfg.ParentName); parent != nil {
				cfg.ParentID = parent.ID()
			}
		}
		if launch.ParentID != "" {
			cfg.ParentID = launch.ParentID
		}
		l, err := looppkg.New(cfg, looppkg.Deps{})
		if err != nil {
			return looppkg.LaunchResult{}, err
		}
		if err := loops.Register(l); err != nil {
			return looppkg.LaunchResult{}, err
		}
		if err := l.Start(context.Background()); err != nil {
			return looppkg.LaunchResult{}, err
		}
		return looppkg.LaunchResult{LoopID: l.ID(), Operation: cfg.Operation, Detached: true}, nil
	}
	rig.reg.ConfigureLoopIntentTools(LoopIntentToolDeps{
		DocTools:         nil, // not needed for container tool
		Registry:         defs,
		PersistSpec:      func(spec looppkg.Spec, _ time.Time) error { rig.persisted[spec.Name] = spec; return nil },
		Reconcile:        func(_ context.Context, _ string) error { return nil },
		LaunchDefinition: launchDef,
		LiveRegistry:     loops,
	})
	rig.reg.ConfigureLoopDefinitionTools(LoopDefinitionToolDeps{
		Registry:   defs,
		DeleteSpec: func(name string) error { rig.deleted = append(rig.deleted, name); return nil },
		Reconcile:  func(_ context.Context, _ string) error { return nil },
	})

	// ConfigureLoopIntentTools refuses to register thane_curate when
	// DocTools is nil, so register the create_container directly to keep
	// the rig small.
	if rig.reg.Get("thane_create_container") == nil {
		t.Fatal("thane_create_container not registered; ConfigureLoopIntentTools wiring changed?")
	}
	return rig
}

// TestThaneCreateContainerStoresAndLaunches covers the happy path:
// minimal arguments yield a persisted container definition, a running
// container loop, and a response with both the loop_id and the
// definition name.
func TestThaneCreateContainerStoresAndLaunches(t *testing.T) {
	t.Parallel()
	rig := newContainerTestRig(t)

	out, err := rig.reg.Get("thane_create_container").Handler(context.Background(), map[string]any{
		"name":   "home_automation",
		"intent": "Top-level container for all HA loops.",
		"tags":   []any{"home", "ha"},
	})
	if err != nil {
		t.Fatalf("thane_create_container: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := resp["operation"]; got != string(looppkg.OperationContainer) {
		t.Errorf("operation = %v, want %q", got, looppkg.OperationContainer)
	}
	if got, _ := resp["loop_id"].(string); got == "" {
		t.Error("loop_id missing")
	}

	spec, ok := rig.persisted["home_automation"]
	if !ok {
		t.Fatal("persist callback not invoked")
	}
	if spec.Operation != looppkg.OperationContainer {
		t.Errorf("persisted Operation = %q, want container", spec.Operation)
	}
	if len(spec.Tags) != 2 || spec.Tags[0] != "home" || spec.Tags[1] != "ha" {
		t.Errorf("persisted Tags = %v, want [home ha]", spec.Tags)
	}
	if spec.Metadata["intent"] == "" {
		t.Error("intent missing from container metadata")
	}
	if rig.loops.GetByName("home_automation") == nil {
		t.Error("container not registered in live registry after launch")
	}
}

// TestThaneCreateContainerNestsByName confirms parent_name resolves
// to the parent's live loop_id and that the new container records the
// parent_name on its metadata for cross-restart re-resolution.
func TestThaneCreateContainerNestsByName(t *testing.T) {
	t.Parallel()
	rig := newContainerTestRig(t)

	if _, err := rig.reg.Get("thane_create_container").Handler(context.Background(), map[string]any{
		"name":   "outer",
		"intent": "Outer.",
	}); err != nil {
		t.Fatalf("create outer: %v", err)
	}
	parent := rig.loops.GetByName("outer")
	if parent == nil {
		t.Fatal("outer container not in live registry")
	}

	out, err := rig.reg.Get("thane_create_container").Handler(context.Background(), map[string]any{
		"name":        "inner",
		"intent":      "Inner.",
		"parent_name": "outer",
	})
	if err != nil {
		t.Fatalf("create inner: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, _ := resp["parent_loop_id"].(string); got != parent.ID() {
		t.Errorf("parent_loop_id = %q, want %q", got, parent.ID())
	}

	innerSpec := rig.persisted["inner"]
	if innerSpec.ParentName != "outer" {
		t.Errorf("inner ParentName = %q, want outer", innerSpec.ParentName)
	}

	inner := rig.loops.GetByName("inner")
	if inner == nil {
		t.Fatal("inner container missing from live registry")
	}
	ancestors := rig.loops.Ancestors(inner.ID())
	if len(ancestors) != 1 || ancestors[0].ID() != parent.ID() {
		t.Errorf("ancestors = %v, want [outer]", ancestors)
	}
}

// TestThaneCreateContainerRejectsMissingParent makes sure the model
// gets an actionable error instead of a silent top-level container when
// it names a non-existent parent.
func TestThaneCreateContainerRejectsMissingParent(t *testing.T) {
	t.Parallel()
	rig := newContainerTestRig(t)

	_, err := rig.reg.Get("thane_create_container").Handler(context.Background(), map[string]any{
		"name":        "child",
		"intent":      "x",
		"parent_name": "no_such_container",
	})
	if err == nil || !strings.Contains(err.Error(), "no_such_container") {
		t.Fatalf("err = %v, want refusal naming missing parent", err)
	}
}

// TestLoopDefinitionDeleteRefusesNonEmptyContainer is the load-bearing
// safety test for Phase 1A: deleting a container that still has child
// loops must fail loudly and name the children, so the operator (or
// model) can re-parent or remove them deliberately.
func TestLoopDefinitionDeleteRefusesNonEmptyContainer(t *testing.T) {
	t.Parallel()
	rig := newContainerTestRig(t)

	if _, err := rig.reg.Get("thane_create_container").Handler(context.Background(), map[string]any{
		"name":   "parent_container",
		"intent": "Holds a child loop.",
	}); err != nil {
		t.Fatalf("create container: %v", err)
	}
	container := rig.loops.GetByName("parent_container")
	if container == nil {
		t.Fatal("parent_container missing")
	}

	// Synthesize a child loop pointing at the container so the delete
	// refusal has something to find. The child doesn't need a real
	// definition — only the live-registry parent_id link matters.
	child, err := looppkg.New(looppkg.Config{
		Name:     "child_loop",
		Task:     "t",
		ParentID: container.ID(),
	}, looppkg.Deps{Runner: noopRunnerForContainer{}})
	if err != nil {
		t.Fatalf("new child: %v", err)
	}
	if err := rig.loops.Register(child); err != nil {
		t.Fatalf("register child: %v", err)
	}

	_, err = rig.reg.Get("loop_definition_delete").Handler(context.Background(), map[string]any{
		"name": "parent_container",
	})
	if err == nil {
		t.Fatal("delete succeeded; want refusal because container has children")
	}
	if !strings.Contains(err.Error(), "child_loop") {
		t.Errorf("refusal message = %v, want it to name child_loop", err)
	}
	if len(rig.deleted) != 0 {
		t.Errorf("deleted = %v, want untouched because delete refused", rig.deleted)
	}
}

// TestThaneCreateContainerIdempotentRetry covers the retry contract:
// calling thane_create_container a second time against an already-
// running container (with the same parent) must short-circuit to the
// existing loop_id instead of tripping
// RunningDurableLoopOverridesError. This is the regression test for
// the "ParentID in launch payload trips HasOverrides" bug.
func TestThaneCreateContainerIdempotentRetry(t *testing.T) {
	t.Parallel()
	rig := newContainerTestRig(t)

	if _, err := rig.reg.Get("thane_create_container").Handler(context.Background(), map[string]any{
		"name":   "outer",
		"intent": "Outer.",
	}); err != nil {
		t.Fatalf("create outer: %v", err)
	}
	create := func() string {
		out, err := rig.reg.Get("thane_create_container").Handler(context.Background(), map[string]any{
			"name":        "inner",
			"intent":      "Inner.",
			"parent_name": "outer",
			"replace":     true,
		})
		if err != nil {
			t.Fatalf("create inner: %v", err)
		}
		var resp map[string]any
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return resp["loop_id"].(string)
	}

	first := create()
	second := create()
	if first != second {
		t.Errorf("loop_id changed on retry: %q → %q (idempotent create should reuse existing)", first, second)
	}
}

// TestLoopDefinitionDeleteAllowsEmptyContainer verifies the symmetric
// happy path: a container with no children deletes cleanly, so the
// model can dismantle stale structure without manual surgery.
func TestLoopDefinitionDeleteAllowsEmptyContainer(t *testing.T) {
	t.Parallel()
	rig := newContainerTestRig(t)

	if _, err := rig.reg.Get("thane_create_container").Handler(context.Background(), map[string]any{
		"name":   "empty_container",
		"intent": "Holds nothing.",
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := rig.reg.Get("loop_definition_delete").Handler(context.Background(), map[string]any{
		"name": "empty_container",
	}); err != nil {
		t.Fatalf("delete empty container: %v", err)
	}
	if len(rig.deleted) != 1 || rig.deleted[0] != "empty_container" {
		t.Errorf("deleted = %v, want [empty_container]", rig.deleted)
	}
}

// noopRunnerForContainer is a stub Runner so the synthesized child loop
// passes loop.New's "Runner or Handler required" check. The loop never
// actually starts in these tests, so the runner is not invoked.
type noopRunnerForContainer struct{}

func (noopRunnerForContainer) Run(_ context.Context, _ looppkg.Request, _ looppkg.StreamCallback) (*looppkg.Response, error) {
	return &looppkg.Response{}, nil
}
