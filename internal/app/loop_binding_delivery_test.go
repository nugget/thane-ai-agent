package app

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// newBindingDeliveryRuntime builds a definition runtime holding a
// container definition that binds an account and a child definition
// that inherits it without declaring one of its own — the shape where
// degraded parentage silently degrades a boundary.
func newBindingDeliveryRuntime(t *testing.T) (*loopDefinitionRuntime, *looppkg.Registry) {
	t.Helper()

	loops := looppkg.NewRegistry()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		loops.ShutdownAll(shutdownCtx)
	})

	defs, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{
		{
			Name:      "bounded-container",
			Operation: looppkg.OperationContainer,
			Bindings:  map[string]string{looppkg.BindingForgeAccount: "github-readonly"},
		},
		{
			Name:       "child",
			Operation:  looppkg.OperationService,
			Task:       "watch",
			ParentName: "bounded-container",
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	return &loopDefinitionRuntime{
		definitions: defs,
		loops:       loops,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, loops
}

// TestRuntimeSpecRefusesWhenInheritedBindingCannotBeDelivered is the
// fail-closed case. The child's restriction lives entirely on its
// container, and the surrounding code treats an unavailable parent as
// a routing inconvenience — reparent to core and carry on. That is
// right for tags and wrong for a boundary: the loop would start
// reaching every configured account, with nothing saying the
// restriction had stopped applying.
func TestRuntimeSpecRefusesWhenInheritedBindingCannotBeDelivered(t *testing.T) {
	t.Parallel()

	r, _ := newBindingDeliveryRuntime(t)

	// The container is defined but not live — paused, inactive, or
	// simply reconciled later in the batch.
	_, err := r.runtimeSpec(looppkg.Spec{
		Name:       "child",
		Operation:  looppkg.OperationService,
		Task:       "watch",
		ParentName: "bounded-container",
	})
	if err == nil {
		t.Fatal("runtimeSpec() = nil; the child would have started unbound")
	}
	for _, want := range []string{"child", looppkg.BindingForgeAccount, "github-readonly", "refusing to start"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q\nmissing %q", err.Error(), want)
		}
	}
}

// TestRuntimeSpecAllowsWhenLiveParentDeliversTheBinding confirms the
// check passes on the ordinary path rather than blocking every
// inheriting loop.
func TestRuntimeSpecAllowsWhenLiveParentDeliversTheBinding(t *testing.T) {
	t.Parallel()

	r, loops := newBindingDeliveryRuntime(t)

	container, err := looppkg.New(looppkg.Config{
		Name:      "bounded-container",
		Operation: looppkg.OperationContainer,
		Bindings:  map[string]string{looppkg.BindingForgeAccount: "github-readonly"},
	}, looppkg.Deps{})
	if err != nil {
		t.Fatalf("new container: %v", err)
	}
	if err := loops.Register(container); err != nil {
		t.Fatalf("register container: %v", err)
	}

	out, err := r.runtimeSpec(looppkg.Spec{
		Name:       "child",
		Operation:  looppkg.OperationService,
		Task:       "watch",
		ParentName: "bounded-container",
	})
	if err != nil {
		t.Fatalf("runtimeSpec() = %v, want the launch to proceed", err)
	}
	if out.ParentID != container.ID() {
		t.Errorf("ParentID = %q, want the live container %q", out.ParentID, container.ID())
	}
}

// TestRuntimeSpecRefusesWhenLiveParentBindsDifferently covers the
// divergence case. A running container keeps the structure it launched
// with, so replacing its definition leaves the stored chain the
// authoring guards approved against disagreeing with the live cascade.
// The runtime is the authority: the child does not start under a
// boundary it was not authorized for.
func TestRuntimeSpecRefusesWhenLiveParentBindsDifferently(t *testing.T) {
	t.Parallel()

	r, loops := newBindingDeliveryRuntime(t)

	// Live container carries the account it launched with, which is no
	// longer what its stored definition says.
	container, err := looppkg.New(looppkg.Config{
		Name:      "bounded-container",
		Operation: looppkg.OperationContainer,
		Bindings:  map[string]string{looppkg.BindingForgeAccount: "github-primary"},
	}, looppkg.Deps{})
	if err != nil {
		t.Fatalf("new container: %v", err)
	}
	if err := loops.Register(container); err != nil {
		t.Fatalf("register container: %v", err)
	}

	_, err = r.runtimeSpec(looppkg.Spec{
		Name:       "child",
		Operation:  looppkg.OperationService,
		Task:       "watch",
		ParentName: "bounded-container",
	})
	if err == nil {
		t.Fatal("runtimeSpec() = nil; the child started under a different account than its ancestry declares")
	}
	if !strings.Contains(err.Error(), "github-primary") || !strings.Contains(err.Error(), "github-readonly") {
		t.Errorf("error = %q, want both the declared and the live account named", err.Error())
	}
}

// TestRuntimeSpecUnaffectedWhenNothingBinds keeps the check from
// turning every unresolved parent into a refusal — that degradation is
// deliberate for loops with no boundary at stake.
func TestRuntimeSpecUnaffectedWhenNothingBinds(t *testing.T) {
	t.Parallel()

	loops := looppkg.NewRegistry()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		loops.ShutdownAll(shutdownCtx)
	})
	defs, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	r := &loopDefinitionRuntime{
		definitions: defs,
		loops:       loops,
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	out, err := r.runtimeSpec(looppkg.Spec{
		Name:       "child",
		Operation:  looppkg.OperationService,
		Task:       "watch",
		ParentName: "not-registered",
	})
	if err != nil {
		t.Fatalf("runtimeSpec() = %v, want an unbound loop to still degrade to core", err)
	}
	if out.ParentName != "" {
		t.Errorf("ParentName = %q, want it dropped as before", out.ParentName)
	}
}
