package loop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
)

// TestEffectiveCascadeReadsProfileDeclarations is the regression
// test for the PR-E1 bug: the cascade walker reads from snapshot
// accessors that previously looked at l.config.* only. In
// production, containers commonly declare exclude_tools /
// routing_factors / delegation_gating via the [router.LoopProfile]
// sub-struct (ego, metacognitive, operator YAML), not the
// top-level Spec field. The audit caught that those Profile-side
// declarations weren't reaching descendants or the EffectiveState
// surface. This test pins both: cascade and effective surface
// both see Profile values.
func TestEffectiveCascadeReadsProfileDeclarations(t *testing.T) {
	t.Parallel()

	r := NewRegistry()

	containerSpec := Spec{
		Name:      "safety",
		Operation: OperationContainer,
		Profile: router.LoopProfile{
			ExcludeTools:     []string{"shell_exec"},
			DelegationGating: "disabled",
			ExtraHints: map[string]string{
				"cluster": "home",
			},
		},
	}
	container, err := NewFromSpec(containerSpec, Deps{})
	if err != nil {
		t.Fatalf("new container: %v", err)
	}
	if err := r.Register(container); err != nil {
		t.Fatalf("register container: %v", err)
	}

	leaf, err := New(Config{
		Name:     "leaf",
		Task:     "t",
		ParentID: container.ID(),
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	// Effective surface should attribute each Profile-declared
	// value to the container — without the fix these surfaces
	// returned empty because the walker never read requestBase.
	excludes := r.EffectiveExcludeTools(leaf.ID())
	if len(excludes) != 1 || excludes[0].Tool != "shell_exec" || excludes[0].From != "safety" {
		t.Errorf("EffectiveExcludeTools = %+v, want [{shell_exec safety}]", excludes)
	}
	gating := r.EffectiveDelegationGating(leaf.ID())
	if gating == nil || gating.Value != "disabled" || gating.From != "safety" {
		t.Errorf("EffectiveDelegationGating = %+v, want {disabled safety}", gating)
	}
	factors := r.EffectiveRoutingFactors(leaf.ID())
	var hasCluster bool
	for _, f := range factors {
		if f.Key == "cluster" && f.Value == "home" && f.From == "safety" {
			hasCluster = true
		}
	}
	if !hasCluster {
		t.Errorf("EffectiveRoutingFactors = %+v, want one entry {cluster home safety}", factors)
	}

	// Iteration-time merge should land the inherited exclude in
	// the prepared Request — descendants now actually get the
	// safety guarantee the container set out to provide.
	req, err := leaf.prepareAgentTurnRequest(Request{}, "conv-1", false)
	if err != nil {
		t.Fatalf("prepareAgentTurnRequest: %v", err)
	}
	var sawShellExec bool
	for _, tool := range req.ExcludeTools {
		if tool == "shell_exec" {
			sawShellExec = true
		}
	}
	if !sawShellExec {
		t.Errorf("ExcludeTools = %v, want shell_exec inherited from container's Profile", req.ExcludeTools)
	}
	if req.RoutingFactors["cluster"] != "home" {
		t.Errorf("RoutingFactors[cluster] = %q, want home (inherited via Profile)", req.RoutingFactors["cluster"])
	}
	if req.DelegationGating != "disabled" {
		t.Errorf("DelegationGating = %q, want disabled (inherited via Profile)", req.DelegationGating)
	}
}

// TestStopLoopRefusesContainerWithChildren mirrors the
// loop_definition_delete refusal pattern. Stopping a container
// with live descendants would orphan them — their ParentID would
// point at a deregistered loop and ancestor walks would silently
// short-circuit. Refuse with the children named so the caller can
// re-parent or stop them first.
func TestStopLoopRefusesContainerWithChildren(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	core, err := New(Config{Name: CoreLoopName, Operation: OperationContainer}, Deps{})
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	if err := r.Register(core); err != nil {
		t.Fatalf("register core: %v", err)
	}

	parent, err := New(Config{Name: "research", Operation: OperationContainer}, Deps{})
	if err != nil {
		t.Fatalf("new parent: %v", err)
	}
	if err := r.Register(parent); err != nil {
		t.Fatalf("register parent: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := parent.Start(ctx); err != nil {
		t.Fatalf("start parent: %v", err)
	}

	child, err := New(Config{Name: "child", Task: "t", ParentID: parent.ID()}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new child: %v", err)
	}
	if err := r.Register(child); err != nil {
		t.Fatalf("register child: %v", err)
	}

	err = r.StopLoop(parent.ID())
	if err == nil {
		t.Fatal("StopLoop accepted parent with live child; want refusal")
	}
	if !strings.Contains(err.Error(), "child") {
		t.Errorf("err = %v, should name the child loop", err)
	}
	if r.Get(parent.ID()) == nil {
		t.Error("parent was deregistered despite the refusal")
	}

	// After the child is gone, the parent can stop cleanly.
	r.Deregister(child.ID())
	if err := r.StopLoop(parent.ID()); err != nil {
		t.Errorf("StopLoop after children cleared: %v", err)
	}
}

// TestSpecValidateReservesCoreName ensures the well-known core
// name can't be claimed by a non-container spec. Otherwise a
// service or request-reply named "core" would shadow
// Registry.Core() lookups and produce undefined behavior in
// ensureCoreLoop ("is core already up?" gets confused).
func TestSpecValidateReservesCoreName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		spec      Spec
		wantError string
	}{
		{
			name: "container named core is valid",
			spec: Spec{
				Name:      CoreLoopName,
				Operation: OperationContainer,
			},
			wantError: "",
		},
		{
			name: "service named core is rejected",
			spec: Spec{
				Name:         CoreLoopName,
				Task:         "t",
				Operation:    OperationService,
				Completion:   CompletionNone,
				SleepMin:     time.Minute,
				SleepMax:     time.Minute,
				SleepDefault: time.Minute,
			},
			wantError: "reserved for the singleton root container",
		},
		{
			name: "request-reply named core is rejected",
			spec: Spec{
				Name:      CoreLoopName,
				Task:      "t",
				Operation: OperationRequestReply,
			},
			wantError: "reserved for the singleton root container",
		},
		{
			name: "core with parent_name is rejected",
			spec: Spec{
				Name:       CoreLoopName,
				Operation:  OperationContainer,
				ParentName: "imposter",
			},
			wantError: "cannot declare a parent",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.spec.Validate()
			if tc.wantError == "" {
				if err != nil {
					t.Fatalf("Validate: %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate accepted spec %+v; want error containing %q", tc.spec, tc.wantError)
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Errorf("Validate error = %q, want substring %q", err.Error(), tc.wantError)
			}
		})
	}
}

// errors.As is the recommended way to test the typed errors; this
// file pulls the standard library import in the cascade test
// already, so we don't need an explicit assertion line here. The
// rest of the test files in this package already do it the same
// way.
var _ = errors.As
