package loop

import (
	"fmt"
	"testing"
)

// countTreeNodes returns the total number of nodes across a forest.
func countTreeNodes(nodes []LoopTreeNode) int {
	n := 0
	for _, node := range nodes {
		n++
		n += countTreeNodes(node.Children)
	}
	return n
}

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

	tree, truncated := BuildLoopTree(statuses, 0)
	if truncated {
		t.Fatal("unlimited build should not truncate")
	}

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
	if tree, _ := BuildLoopTree(statuses, 0); len(tree) != 0 {
		t.Fatalf("pure-cycle forest = %+v, want empty (no rootable node)", tree)
	}
}

func TestBuildLoopTreeTruncates(t *testing.T) {
	t.Parallel()

	// One root with a flat fan-out of ten children.
	statuses := []Status{{ID: "root", Name: "root", Config: Config{Operation: OperationContainer}}}
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("c%02d", i)
		statuses = append(statuses, Status{ID: id, Name: id, ParentID: "root", Config: Config{Operation: OperationService}})
	}

	tree, truncated := BuildLoopTree(statuses, 4)
	if !truncated {
		t.Fatal("expected truncated=true when the node budget is exceeded")
	}
	if got := countTreeNodes(tree); got > 4 {
		t.Fatalf("emitted %d nodes, want <= 4 (the cap)", got)
	}

	full, fullTruncated := BuildLoopTree(statuses, 0)
	if fullTruncated {
		t.Error("unlimited build should not truncate")
	}
	if got := countTreeNodes(full); got != 11 {
		t.Fatalf("unlimited build emitted %d nodes, want 11 (root + 10)", got)
	}
}
