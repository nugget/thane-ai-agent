package tools

import (
	"context"
	"strings"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestGuardLaunchParent covers the escape that survives merging the
// caller's binding onto a spawned spec: bindings resolve
// ancestors-first, and parent_id is caller-supplied, so choosing a
// parent is choosing a binding unless the launch checks.
func TestGuardLaunchParent(t *testing.T) {
	t.Parallel()

	// A container bound to the write account, and one bound to the same
	// account the caller is on.
	newRegistryWithContainers := func(t *testing.T) (*Registry, string, string, string) {
		t.Helper()
		loops := looppkg.NewRegistry()

		write, err := looppkg.New(looppkg.Config{
			Name:      "write-container",
			Operation: looppkg.OperationContainer,
			Bindings:  map[string]string{looppkg.BindingForgeAccount: "github-primary"},
		}, looppkg.Deps{})
		if err != nil {
			t.Fatalf("new write container: %v", err)
		}
		if err := loops.Register(write); err != nil {
			t.Fatalf("register: %v", err)
		}

		same, err := looppkg.New(looppkg.Config{
			Name:      "readonly-container",
			Operation: looppkg.OperationContainer,
			Bindings:  map[string]string{looppkg.BindingForgeAccount: "github-readonly"},
		}, looppkg.Deps{})
		if err != nil {
			t.Fatalf("new readonly container: %v", err)
		}
		if err := loops.Register(same); err != nil {
			t.Fatalf("register: %v", err)
		}

		plain, err := looppkg.New(looppkg.Config{
			Name:      "plain-container",
			Operation: looppkg.OperationContainer,
		}, looppkg.Deps{})
		if err != nil {
			t.Fatalf("new plain container: %v", err)
		}
		if err := loops.Register(plain); err != nil {
			t.Fatalf("register: %v", err)
		}

		r := NewRegistry(nil, nil, nil)
		r.liveLoopRegistry = loops
		return r, write.ID(), same.ID(), plain.ID()
	}

	boundCtx := looppkg.WithBindings(context.Background(), map[string]string{
		looppkg.BindingForgeAccount: "github-readonly",
	})

	t.Run("refuses a parent that would move the binding", func(t *testing.T) {
		t.Parallel()
		r, writeID, _, _ := newRegistryWithContainers(t)
		err := r.guardLaunchParent(boundCtx, writeID, "")
		if err == nil {
			t.Fatal("guardLaunchParent() = nil; a bound caller reparented under a differently-bound container")
		}
		for _, want := range []string{"github-primary", "github-readonly", looppkg.BindingForgeAccount} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q\nmissing %q", err.Error(), want)
			}
		}
	})

	t.Run("allows a parent bound to the same account", func(t *testing.T) {
		t.Parallel()
		r, _, sameID, _ := newRegistryWithContainers(t)
		if err := r.guardLaunchParent(boundCtx, sameID, ""); err != nil {
			t.Errorf("guardLaunchParent() = %v, want nil for a matching binding", err)
		}
	})

	t.Run("allows a parent that binds nothing", func(t *testing.T) {
		t.Parallel()
		r, _, _, plainID := newRegistryWithContainers(t)
		if err := r.guardLaunchParent(boundCtx, plainID, ""); err != nil {
			t.Errorf("guardLaunchParent() = %v, want nil for an unbound parent", err)
		}
	})

	t.Run("an unbound caller is unrestricted", func(t *testing.T) {
		t.Parallel()
		r, writeID, _, _ := newRegistryWithContainers(t)
		if err := r.guardLaunchParent(context.Background(), writeID, ""); err != nil {
			t.Errorf("guardLaunchParent() = %v, want nil for an unbound caller", err)
		}
	})

	t.Run("no parent requested is allowed", func(t *testing.T) {
		t.Parallel()
		r, _, _, _ := newRegistryWithContainers(t)
		if err := r.guardLaunchParent(boundCtx, "   ", ""); err != nil {
			t.Errorf("guardLaunchParent() = %v, want nil when no parent is named", err)
		}
	})

	t.Run("an unknown parent does not refuse here", func(t *testing.T) {
		t.Parallel()
		// Resolution of a bad parent ID belongs to the launch itself;
		// this guard only answers the binding question.
		r, _, _, _ := newRegistryWithContainers(t)
		if err := r.guardLaunchParent(boundCtx, "no-such-loop", ""); err != nil {
			t.Errorf("guardLaunchParent() = %v, want nil for an unresolvable parent", err)
		}
	})
}

// TestGuardLaunchParentResolvesParentName covers the second way a
// launch can name its parent. Which reference the runtime resolves
// depends on the path, so the guard checks both rather than depending
// on that distinction holding.
func TestGuardLaunchParentResolvesParentName(t *testing.T) {
	t.Parallel()

	loops := looppkg.NewRegistry()
	write, err := looppkg.New(looppkg.Config{
		Name:      "write-container",
		Operation: looppkg.OperationContainer,
		Bindings:  map[string]string{looppkg.BindingForgeAccount: "github-primary"},
	}, looppkg.Deps{})
	if err != nil {
		t.Fatalf("new container: %v", err)
	}
	if err := loops.Register(write); err != nil {
		t.Fatalf("register: %v", err)
	}

	r := NewRegistry(nil, nil, nil)
	r.liveLoopRegistry = loops
	boundCtx := looppkg.WithBindings(context.Background(), map[string]string{
		looppkg.BindingForgeAccount: "github-readonly",
	})

	if err := r.guardLaunchParent(boundCtx, "", "write-container"); err == nil {
		t.Error("guardLaunchParent() = nil; a parent named by parent_name escaped the check")
	}
	if err := r.guardLaunchParent(boundCtx, "", "no-such-container"); err != nil {
		t.Errorf("guardLaunchParent() = %v, want nil for an unresolvable name", err)
	}
}
