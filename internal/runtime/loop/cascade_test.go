package loop

import (
	"testing"
)

// TestEffectiveExcludeToolsUnion covers the union semantics: every
// ancestor's ExcludeTools contributes and a descendant cannot
// un-exclude an ancestor's restriction. Provenance reports the
// closest declaration when the same tool appears at multiple
// levels.
func TestEffectiveExcludeToolsUnion(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	root, err := New(Config{
		Name:         "root",
		Operation:    OperationContainer,
		ExcludeTools: []string{"shell_exec", "doc_write"},
	}, Deps{})
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	if err := r.Register(root); err != nil {
		t.Fatalf("register root: %v", err)
	}

	leaf, err := New(Config{
		Name:         "leaf",
		Task:         "t",
		ParentID:     root.ID(),
		ExcludeTools: []string{"shell_exec", "schedule_task"},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	got := r.EffectiveExcludeTools(leaf.ID())
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (shell_exec, schedule_task, doc_write): %+v", len(got), got)
	}

	byTool := make(map[string]string, len(got))
	for _, e := range got {
		byTool[e.Tool] = e.From
	}
	// shell_exec appears at both levels — closest declaration wins on dedup.
	if byTool["shell_exec"] != EffectiveOriginSelf {
		t.Errorf("shell_exec From = %q, want self (closest wins on collision)", byTool["shell_exec"])
	}
	if byTool["schedule_task"] != EffectiveOriginSelf {
		t.Errorf("schedule_task From = %q, want self", byTool["schedule_task"])
	}
	if byTool["doc_write"] != "root" {
		t.Errorf("doc_write From = %q, want root (only the ancestor declared it)", byTool["doc_write"])
	}
}

// TestEffectiveRoutingFactorsChildWins covers the child-wins
// semantics on key collision: when a leaf and an ancestor both
// declare the same key, the leaf's value+provenance survive.
func TestEffectiveRoutingFactorsChildWins(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	root, err := New(Config{
		Name:      "root",
		Operation: OperationContainer,
		RoutingFactors: map[string]string{
			"cluster":     "home",
			"environment": "production",
		},
	}, Deps{})
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	if err := r.Register(root); err != nil {
		t.Fatalf("register root: %v", err)
	}

	leaf, err := New(Config{
		Name:     "leaf",
		Task:     "t",
		ParentID: root.ID(),
		RoutingFactors: map[string]string{
			"cluster":    "home", // same as root — leaf's "self" wins on dedup
			"complexity": "low",  // leaf-only
		},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	got := r.EffectiveRoutingFactors(leaf.ID())
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3: %+v", len(got), got)
	}
	byKey := make(map[string]EffectiveRoutingFactor, len(got))
	for _, f := range got {
		byKey[f.Key] = f
	}
	if byKey["cluster"].From != EffectiveOriginSelf {
		t.Errorf("cluster From = %q, want self (child wins on collision)", byKey["cluster"].From)
	}
	if byKey["complexity"].From != EffectiveOriginSelf {
		t.Errorf("complexity From = %q, want self", byKey["complexity"].From)
	}
	if byKey["environment"].From != "root" {
		t.Errorf("environment From = %q, want root", byKey["environment"].From)
	}
	if byKey["environment"].Value != "production" {
		t.Errorf("environment Value = %q, want production", byKey["environment"].Value)
	}
}

// TestEffectiveDelegationGatingClosestNonEmpty covers the closest-
// non-empty walk: a leaf with no declaration inherits from the
// closest ancestor that declared a value, skipping intermediate
// ancestors with no declaration.
func TestEffectiveDelegationGatingClosestNonEmpty(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	grandparent, err := New(Config{
		Name:             "grandparent",
		Operation:        OperationContainer,
		DelegationGating: "permissive",
	}, Deps{})
	if err != nil {
		t.Fatalf("new grandparent: %v", err)
	}
	if err := r.Register(grandparent); err != nil {
		t.Fatalf("register grandparent: %v", err)
	}
	parent, err := New(Config{
		Name:             "parent",
		Operation:        OperationContainer,
		ParentID:         grandparent.ID(),
		DelegationGating: "disabled",
	}, Deps{})
	if err != nil {
		t.Fatalf("new parent: %v", err)
	}
	if err := r.Register(parent); err != nil {
		t.Fatalf("register parent: %v", err)
	}
	leaf, err := New(Config{
		Name:     "leaf",
		Task:     "t",
		ParentID: parent.ID(),
		// No DelegationGating declared.
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	got := r.EffectiveDelegationGating(leaf.ID())
	if got == nil {
		t.Fatal("EffectiveDelegationGating = nil, want closest ancestor's value")
	}
	if got.Value != "disabled" {
		t.Errorf("Value = %q, want disabled (parent wins over grandparent — closest non-empty)", got.Value)
	}
	if got.From != "parent" {
		t.Errorf("From = %q, want parent", got.From)
	}

	// Sanity: a loop with its own non-empty value should report self.
	override, err := New(Config{
		Name:             "override",
		Task:             "t",
		ParentID:         parent.ID(),
		DelegationGating: "strict",
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new override: %v", err)
	}
	if err := r.Register(override); err != nil {
		t.Fatalf("register override: %v", err)
	}
	got = r.EffectiveDelegationGating(override.ID())
	if got == nil || got.From != EffectiveOriginSelf || got.Value != "strict" {
		t.Errorf("override leaf got = %+v, want {strict self}", got)
	}
}

// TestEffectiveDelegationGatingNoDeclarationAnywhere asserts the
// nil-when-empty contract: if no loop in the chain set a non-empty
// gating value, the result is nil so the caller falls back to the
// agent default.
func TestEffectiveDelegationGatingNoDeclarationAnywhere(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	root, err := New(Config{
		Name:      "root",
		Operation: OperationContainer,
	}, Deps{})
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	if err := r.Register(root); err != nil {
		t.Fatalf("register root: %v", err)
	}
	leaf, err := New(Config{Name: "leaf", Task: "t", ParentID: root.ID()}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}
	if got := r.EffectiveDelegationGating(leaf.ID()); got != nil {
		t.Errorf("got %+v, want nil (no declaration in chain)", got)
	}
}

// TestPrepareAgentTurnRequestAppliesCascade is the load-bearing
// end-to-end test for PR-C: it confirms each cascading field
// actually lands in the prepared Request that goes to the runner.
func TestPrepareAgentTurnRequestAppliesCascade(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	container, err := New(Config{
		Name:         "ctx",
		Operation:    OperationContainer,
		ExcludeTools: []string{"shell_exec"},
		RoutingFactors: map[string]string{
			"cluster":     "home",
			"environment": "production",
		},
		DelegationGating: "disabled",
	}, Deps{})
	if err != nil {
		t.Fatalf("new container: %v", err)
	}
	if err := r.Register(container); err != nil {
		t.Fatalf("register container: %v", err)
	}
	leaf, err := New(Config{
		Name:         "leaf",
		Task:         "t",
		ParentID:     container.ID(),
		ExcludeTools: []string{"doc_write"},
		RoutingFactors: map[string]string{
			"cluster":    "edge", // overrides container's "home"
			"complexity": "low",
		},
		// No DelegationGating — should inherit "disabled".
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	got, err := leaf.prepareAgentTurnRequest(Request{}, "conv-1", false)
	if err != nil {
		t.Fatalf("prepareAgentTurnRequest: %v", err)
	}

	// ExcludeTools union — both shell_exec (from container) and doc_write (own).
	excludes := make(map[string]bool, len(got.ExcludeTools))
	for _, e := range got.ExcludeTools {
		excludes[e] = true
	}
	if !excludes["shell_exec"] || !excludes["doc_write"] {
		t.Errorf("ExcludeTools = %v, want both shell_exec (inherited) and doc_write (own)", got.ExcludeTools)
	}

	// RoutingFactors child-wins — cluster=edge (leaf), environment=production (inherited).
	if got.RoutingFactors["cluster"] != "edge" {
		t.Errorf("cluster = %q, want edge (leaf overrides container)", got.RoutingFactors["cluster"])
	}
	if got.RoutingFactors["environment"] != "production" {
		t.Errorf("environment = %q, want production (inherited)", got.RoutingFactors["environment"])
	}
	if got.RoutingFactors["complexity"] != "low" {
		t.Errorf("complexity = %q, want low (leaf-only)", got.RoutingFactors["complexity"])
	}

	// DelegationGating closest-non-empty — leaf has nothing, inherits from container.
	if got.DelegationGating != "disabled" {
		t.Errorf("DelegationGating = %q, want disabled (inherited from ctx)", got.DelegationGating)
	}
}

// TestLoopStatusReportsCascadeFields confirms the Status surface
// includes the three new effective fields populated by the
// registry-installed hook.
func TestLoopStatusReportsCascadeFields(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	container, err := New(Config{
		Name:             "ctx",
		Operation:        OperationContainer,
		ExcludeTools:     []string{"shell_exec"},
		RoutingFactors:   map[string]string{"env": "prod"},
		DelegationGating: "disabled",
	}, Deps{})
	if err != nil {
		t.Fatalf("new container: %v", err)
	}
	if err := r.Register(container); err != nil {
		t.Fatalf("register container: %v", err)
	}
	leaf, err := New(Config{
		Name:         "reporter",
		Task:         "t",
		ParentID:     container.ID(),
		ExcludeTools: []string{"doc_write"},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	st := leaf.Status()
	if len(st.EffectiveExcludeTools) != 2 {
		t.Errorf("EffectiveExcludeTools len = %d, want 2: %+v", len(st.EffectiveExcludeTools), st.EffectiveExcludeTools)
	}
	if len(st.EffectiveRoutingFactors) != 1 || st.EffectiveRoutingFactors[0].Key != "env" {
		t.Errorf("EffectiveRoutingFactors = %+v, want one entry for env", st.EffectiveRoutingFactors)
	}
	if st.EffectiveDelegationGating == nil || st.EffectiveDelegationGating.Value != "disabled" {
		t.Errorf("EffectiveDelegationGating = %+v, want {disabled ctx}", st.EffectiveDelegationGating)
	}
}
