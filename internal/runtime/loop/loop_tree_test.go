package loop

import (
	"slices"
	"testing"
)

func TestBuildLoopTree(t *testing.T) {
	t.Parallel()

	statuses := []Status{
		{ID: "lp_core", Name: "core", Config: Config{Operation: OperationContainer}},
		{ID: "lp_travel", Name: "travel", ParentID: "lp_core", Config: Config{Operation: OperationContainer, Intent: "trip logistics"}},
		{ID: "lp_flight", Name: "flight_watch", ParentID: "lp_travel", Config: Config{Operation: OperationService}},
		{ID: "lp_hotel", Name: "hotel_tracker", ParentID: "lp_travel", Config: Config{Operation: OperationService}},
		{ID: "lp_ego", Name: "ego", ParentID: "lp_core", Config: Config{Operation: OperationService}},
		// Parent not present in the batch → treated as a root.
		{ID: "lp_orphan", Name: "orphan", ParentID: "lp_missing", Config: Config{Operation: OperationService}},
	}

	tree := BuildLoopTree(statuses)

	// Roots sorted by name: core, orphan.
	if len(tree) != 2 || tree[0].Name != "core" || tree[1].Name != "orphan" {
		t.Fatalf("roots = %+v, want [core orphan]", tree)
	}

	core := tree[0]
	// core's children sorted by name: ego, travel.
	if len(core.Children) != 2 || core.Children[0].Name != "ego" || core.Children[1].Name != "travel" {
		t.Fatalf("core children = %+v, want [ego travel]", core.Children)
	}

	travel := core.Children[1]
	if travel.Operation != string(OperationContainer) {
		t.Errorf("travel operation = %q, want container", travel.Operation)
	}
	if travel.Intent != "trip logistics" {
		t.Errorf("travel intent = %q, want trip logistics", travel.Intent)
	}
	if len(travel.Children) != 2 || travel.Children[0].Name != "flight_watch" || travel.Children[1].Name != "hotel_tracker" {
		t.Fatalf("travel children = %+v, want [flight_watch hotel_tracker]", travel.Children)
	}
	// Leaf carries no children key.
	if travel.Children[0].Children != nil {
		t.Errorf("leaf flight_watch should have nil children, got %+v", travel.Children[0].Children)
	}
}

func TestBuildLoopTreeTerminatesOnCycle(t *testing.T) {
	t.Parallel()

	// a→b→a is a pure pointer cycle with no root; it must terminate (and
	// render nothing, since neither node is reachable from a real root)
	// rather than spin.
	statuses := []Status{
		{ID: "a", Name: "a", ParentID: "b", Config: Config{Operation: OperationService}},
		{ID: "b", Name: "b", ParentID: "a", Config: Config{Operation: OperationService}},
	}
	if tree := BuildLoopTree(statuses); len(tree) != 0 {
		t.Fatalf("pure-cycle forest = %+v, want empty (no rootable node)", tree)
	}
}

func TestRegistryDescendants(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	deps := Deps{Runner: &noopRunner{}}
	reg := func(cfg Config) *Loop {
		l := mustNew(t, cfg, deps)
		if err := r.Register(l); err != nil {
			t.Fatalf("Register %s: %v", cfg.Name, err)
		}
		return l
	}

	root := reg(Config{Name: "root", Task: "x"})
	mid := reg(Config{Name: "mid", Task: "x", ParentID: root.ID()})
	reg(Config{Name: "leaf1", Task: "x", ParentID: mid.ID()})
	reg(Config{Name: "leaf2", Task: "x", ParentID: root.ID()})
	unrelated := reg(Config{Name: "unrelated", Task: "x"})

	descNames := func(ls []*Loop) []string {
		out := make([]string, len(ls))
		for i, l := range ls {
			out[i] = l.config.Name
		}
		return out
	}

	// root's full subtree, sorted by name.
	if got := descNames(r.Descendants(root.ID())); !slices.Equal(got, []string{"leaf1", "leaf2", "mid"}) {
		t.Fatalf("Descendants(root) = %v, want [leaf1 leaf2 mid]", got)
	}
	// mid has one child.
	if got := descNames(r.Descendants(mid.ID())); !slices.Equal(got, []string{"leaf1"}) {
		t.Fatalf("Descendants(mid) = %v, want [leaf1]", got)
	}
	// A leaf / unrelated loop has none.
	if got := r.Descendants(unrelated.ID()); got != nil {
		t.Fatalf("Descendants(unrelated) = %v, want nil", descNames(got))
	}
}
