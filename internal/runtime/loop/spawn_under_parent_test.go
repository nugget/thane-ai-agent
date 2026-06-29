package loop

import (
	"context"
	"testing"
)

// TestSpawnLoopUnderParentOrCore covers the tolerant spawn used by loops that
// are created dynamically during bootstrap and want a grouping container: they
// attach to a live parent when present and fall back to core (rather than
// failing to register) when the parent isn't live yet.
func TestSpawnLoopUnderParentOrCore(t *testing.T) {
	t.Parallel()

	noop := func(context.Context, any) error { return nil }

	t.Run("attaches to live parent", func(t *testing.T) {
		t.Parallel()
		r := NewRegistry()
		ctx := context.Background()
		if _, err := r.SpawnSpec(ctx, Spec{Name: "grp", Enabled: true, Operation: OperationContainer}, Deps{}); err != nil {
			t.Fatalf("spawn parent container: %v", err)
		}
		if _, err := r.SpawnLoopUnderParentOrCore(ctx, Config{
			Name: "child", ParentName: "grp", Handler: noop,
		}, Deps{}); err != nil {
			t.Fatalf("spawn child: %v", err)
		}

		child := r.GetByName("child")
		if child == nil {
			t.Fatal("child not registered")
		}
		if grp := r.GetByName("grp"); child.ParentID() != grp.ID() {
			t.Errorf("child ParentID = %q, want grp %q", child.ParentID(), grp.ID())
		}
	})

	t.Run("falls back to core when parent missing", func(t *testing.T) {
		t.Parallel()
		r := NewRegistry()
		ctx := context.Background()
		if _, err := r.SpawnSpec(ctx, Spec{Name: CoreLoopName, Enabled: true, Operation: OperationContainer}, Deps{}); err != nil {
			t.Fatalf("spawn core: %v", err)
		}
		if _, err := r.SpawnLoopUnderParentOrCore(ctx, Config{
			Name: "orphan-child", ParentName: "nonexistent", Handler: noop,
		}, Deps{}); err != nil {
			t.Fatalf("expected graceful fallback, got error: %v", err)
		}
		child := r.GetByName("orphan-child")
		if child == nil {
			t.Fatal("child not registered after fallback")
		}
		if core := r.Core(); core == nil || child.ParentID() != core.ID() {
			t.Errorf("child ParentID = %q, want core's id", child.ParentID())
		}
	})
}
