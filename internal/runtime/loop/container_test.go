package loop

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestContainerSpecValidation covers the shape contract for container
// loops: they hold inheritable state but reject any field that would
// imply execution. The validator should fail loudly rather than silently
// ignore an authoring mistake.
func TestContainerSpecValidation(t *testing.T) {
	t.Parallel()

	t.Run("minimal container spec is valid", func(t *testing.T) {
		spec := &Spec{
			Name:      "home_automation",
			Operation: OperationContainer,
			Tags:      []string{"home"},
		}
		if err := spec.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("container rejects task", func(t *testing.T) {
		spec := &Spec{
			Name:      "with_task",
			Operation: OperationContainer,
			Task:      "do something",
		}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "cannot set task") {
			t.Fatalf("Validate: %v, want container task rejection", err)
		}
	})

	t.Run("container rejects sleep envelope", func(t *testing.T) {
		spec := &Spec{
			Name:      "with_sleep",
			Operation: OperationContainer,
			SleepMin:  time.Minute,
		}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "sleep envelope") {
			t.Fatalf("Validate: %v, want container sleep envelope rejection", err)
		}
	})

	t.Run("container rejects outputs", func(t *testing.T) {
		spec := &Spec{
			Name:      "with_outputs",
			Operation: OperationContainer,
			Outputs: []OutputSpec{{
				Name: "doc",
				Ref:  "kb:test.md",
				Type: OutputTypeMaintainedDocument,
				Mode: OutputModeReplace,
			}},
		}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "cannot declare outputs") {
			t.Fatalf("Validate: %v, want container outputs rejection", err)
		}
	})

	t.Run("container rejects completion", func(t *testing.T) {
		spec := &Spec{
			Name:       "with_completion",
			Operation:  OperationContainer,
			Completion: CompletionConversation,
		}
		err := spec.Validate()
		if err == nil || !strings.Contains(err.Error(), "cannot set completion") {
			t.Fatalf("Validate: %v, want container completion rejection", err)
		}
	})

	t.Run("container accepts completion none", func(t *testing.T) {
		spec := &Spec{
			Name:       "with_none_completion",
			Operation:  OperationContainer,
			Completion: CompletionNone,
		}
		if err := spec.Validate(); err != nil {
			t.Fatalf("Validate: %v, want CompletionNone accepted", err)
		}
	})
}

// TestContainerLoopSkipsGoroutine verifies that starting a container
// loop marks it started and closes Done() immediately without spawning
// a wake goroutine. A container that accidentally ran a wake loop
// would burn CPU and confuse the visualizer.
func TestContainerLoopSkipsGoroutine(t *testing.T) {
	t.Parallel()

	l, err := New(Config{
		Name:      "container_only",
		Operation: OperationContainer,
	}, Deps{})
	if err != nil {
		t.Fatalf("New container: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start container: %v", err)
	}

	select {
	case <-l.Done():
	case <-time.After(time.Second):
		t.Fatal("container Done channel never closed; goroutine likely spawned")
	}
}

// TestRegistryAncestorsWalksChain covers Registry.Ancestors for the
// shape Phase 1A actually relies on: a leaf loop reaches every
// container above it, in order, and the walk terminates cleanly when
// ParentID is empty.
func TestRegistryAncestorsWalksChain(t *testing.T) {
	t.Parallel()

	r := NewRegistry()

	root, err := New(Config{Name: "root", Operation: OperationContainer, Tags: []string{"root_tag"}}, Deps{})
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	if err := r.Register(root); err != nil {
		t.Fatalf("register root: %v", err)
	}

	mid, err := New(Config{Name: "mid", Operation: OperationContainer, Tags: []string{"mid_tag"}, ParentID: root.ID()}, Deps{})
	if err != nil {
		t.Fatalf("new mid: %v", err)
	}
	if err := r.Register(mid); err != nil {
		t.Fatalf("register mid: %v", err)
	}

	leaf, err := New(Config{Name: "leaf", Task: "t", ParentID: mid.ID()}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	ancestors := r.Ancestors(leaf.ID())
	if len(ancestors) != 2 {
		t.Fatalf("ancestors len = %d, want 2", len(ancestors))
	}
	if ancestors[0].ID() != mid.ID() {
		t.Errorf("ancestors[0] = %q, want mid", ancestors[0].Name())
	}
	if ancestors[1].ID() != root.ID() {
		t.Errorf("ancestors[1] = %q, want root", ancestors[1].Name())
	}

	if got := r.Ancestors(root.ID()); len(got) != 0 {
		t.Errorf("root has no parent, got %d ancestors", len(got))
	}
	if got := r.Ancestors("unknown"); got != nil {
		t.Errorf("unknown loop id returned %d ancestors, want nil", len(got))
	}
}

// TestRegistryAncestorsBreaksOnCycle defends Registry.Ancestors against
// a malformed graph: even if parent_id somehow forms a cycle, the walk
// must terminate. Validators reject this shape at definition time, but
// the runtime walker should not assume the data is well-formed.
func TestRegistryAncestorsBreaksOnCycle(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	a, err := New(Config{Name: "a", Operation: OperationContainer}, Deps{})
	if err != nil {
		t.Fatalf("new a: %v", err)
	}
	b, err := New(Config{Name: "b", Operation: OperationContainer}, Deps{})
	if err != nil {
		t.Fatalf("new b: %v", err)
	}
	a.config.ParentID = b.ID()
	b.config.ParentID = a.ID()
	if err := r.Register(a); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if err := r.Register(b); err != nil {
		t.Fatalf("register b: %v", err)
	}

	got := r.Ancestors(a.ID())
	if len(got) != 1 || got[0].ID() != b.ID() {
		t.Fatalf("expected single-step cycle walk to halt at b, got %d ancestors", len(got))
	}
}

// TestRegistryAncestorContainerTagsMergesParents asserts the
// inheritance contract used by iteration prep: a child loop's
// inherited tags are the union of every container ancestor's Tags,
// while non-container ancestors contribute nothing.
func TestRegistryAncestorContainerTagsMergesParents(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	root, err := New(Config{Name: "root", Operation: OperationContainer, Tags: []string{"alpha", "beta"}}, Deps{})
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	if err := r.Register(root); err != nil {
		t.Fatalf("register root: %v", err)
	}
	mid, err := New(Config{Name: "mid", Operation: OperationContainer, Tags: []string{"beta", "gamma"}, ParentID: root.ID()}, Deps{})
	if err != nil {
		t.Fatalf("new mid: %v", err)
	}
	if err := r.Register(mid); err != nil {
		t.Fatalf("register mid: %v", err)
	}
	leaf, err := New(Config{Name: "leaf", Task: "t", ParentID: mid.ID()}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	inherited := r.ancestorContainerTags(leaf.ID())
	if len(inherited) != 3 {
		t.Fatalf("inherited len = %d, want 3 (alpha, beta, gamma)", len(inherited))
	}
	want := []string{"beta", "gamma", "alpha"} // mid first, then root, dedup keeps first-seen
	for i, w := range want {
		if inherited[i] != w {
			t.Errorf("inherited[%d] = %q, want %q (full=%v)", i, inherited[i], w, inherited)
		}
	}
}

// TestRegistryAncestorContainerTagsSkipsNonContainers verifies that a
// non-container ancestor (e.g. a parent service loop someone wired
// children to) does not contribute tags to descendants. Only declared
// container nodes are inheritance sources.
func TestRegistryAncestorContainerTagsSkipsNonContainers(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	svc, err := New(Config{
		Name:      "service",
		Task:      "t",
		Operation: OperationService,
		Tags:      []string{"service_tag"},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new svc: %v", err)
	}
	if err := r.Register(svc); err != nil {
		t.Fatalf("register svc: %v", err)
	}
	child, err := New(Config{Name: "child", Task: "t", ParentID: svc.ID()}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new child: %v", err)
	}
	if err := r.Register(child); err != nil {
		t.Fatalf("register child: %v", err)
	}

	if got := r.ancestorContainerTags(child.ID()); got != nil {
		t.Fatalf("service ancestor contributed tags %v, want nil", got)
	}
}

// TestRegistryChildrenListsDirectDescendants asserts that Children
// returns only the immediate children of a loop, used by the
// non-empty container delete refusal.
func TestRegistryChildrenListsDirectDescendants(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	parent, err := New(Config{Name: "parent", Operation: OperationContainer}, Deps{})
	if err != nil {
		t.Fatalf("new parent: %v", err)
	}
	if err := r.Register(parent); err != nil {
		t.Fatalf("register parent: %v", err)
	}
	a, err := New(Config{Name: "a", Task: "t", ParentID: parent.ID()}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new a: %v", err)
	}
	if err := r.Register(a); err != nil {
		t.Fatalf("register a: %v", err)
	}
	b, err := New(Config{Name: "b", Task: "t", ParentID: parent.ID()}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new b: %v", err)
	}
	if err := r.Register(b); err != nil {
		t.Fatalf("register b: %v", err)
	}
	orphan, err := New(Config{Name: "orphan", Task: "t"}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new orphan: %v", err)
	}
	if err := r.Register(orphan); err != nil {
		t.Fatalf("register orphan: %v", err)
	}

	children := r.Children(parent.ID())
	if len(children) != 2 {
		t.Fatalf("children len = %d, want 2", len(children))
	}
	if children[0].Name() != "a" || children[1].Name() != "b" {
		t.Errorf("children = [%s, %s], want sorted [a, b]", children[0].Name(), children[1].Name())
	}
	if got := r.Children(orphan.ID()); len(got) != 0 {
		t.Errorf("orphan has %d children, want 0", len(got))
	}
}

// TestContainerInheritedTagsFlowIntoRequest is the end-to-end check
// for Phase 1A: a leaf loop's prepared request carries inherited
// container tags through the merge in prepareAgentTurnRequest.
func TestContainerInheritedTagsFlowIntoRequest(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	container, err := New(Config{
		Name:      "ctx",
		Operation: OperationContainer,
		Tags:      []string{"inherited_tag"},
	}, Deps{})
	if err != nil {
		t.Fatalf("new container: %v", err)
	}
	if err := r.Register(container); err != nil {
		t.Fatalf("register container: %v", err)
	}

	leaf, err := New(Config{
		Name:     "leaf",
		Task:     "do",
		ParentID: container.ID(),
		Tags:     []string{"own_tag"},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("new leaf: %v", err)
	}
	if err := r.Register(leaf); err != nil {
		t.Fatalf("register leaf: %v", err)
	}

	req, err := leaf.prepareAgentTurnRequest(Request{}, "conv-1", false)
	if err != nil {
		t.Fatalf("prepareAgentTurnRequest: %v", err)
	}

	gotOwn := false
	gotInherited := false
	for _, tag := range req.InitialTags {
		switch tag {
		case "own_tag":
			gotOwn = true
		case "inherited_tag":
			gotInherited = true
		}
	}
	if !gotOwn || !gotInherited {
		t.Fatalf("InitialTags = %v, want both own_tag and inherited_tag", req.InitialTags)
	}
}
