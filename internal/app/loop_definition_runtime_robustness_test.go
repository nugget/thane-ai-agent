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

// TestRuntimeSpecDropsUnresolvedParentName covers the post-#895
// boundary: Register now rejects a loop whose ParentName is set
// but ParentID is empty. runtimeSpec must catch that case for
// definition-driven spawns and clear ParentName (with a warn log)
// so the reconciler keeps converging when the operator imports
// children before their parent — the alternative is fail-loop on
// bulk imports, which we explicitly didn't want at this layer.
func TestRuntimeSpecDropsUnresolvedParentName(t *testing.T) {
	t.Parallel()

	loops := looppkg.NewRegistry()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		loops.ShutdownAll(shutdownCtx)
	})

	r := &loopDefinitionRuntime{
		loops:  loops,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	spec := looppkg.Spec{
		Name:       "child",
		Operation:  looppkg.OperationService,
		ParentName: "outer_not_yet_registered",
	}
	out, err := r.runtimeSpec(spec)
	if err != nil {
		t.Fatalf("runtimeSpec: %v", err)
	}
	if out.ParentName != "" {
		t.Errorf("ParentName = %q, want empty (should be dropped with warn log)", out.ParentName)
	}
	if out.ParentID != "" {
		t.Errorf("ParentID = %q, want empty (parent not registered, nothing to set)", out.ParentID)
	}
}

// TestRuntimeSpecResolvesRegisteredParent confirms the happy
// path: a live parent in the loop registry gets its ID stamped
// onto the child spec at hydration time.
func TestRuntimeSpecResolvesRegisteredParent(t *testing.T) {
	t.Parallel()

	loops := looppkg.NewRegistry()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		loops.ShutdownAll(shutdownCtx)
	})

	core, err := looppkg.New(looppkg.Config{Name: looppkg.CoreLoopName, Operation: looppkg.OperationContainer}, looppkg.Deps{})
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	if err := loops.Register(core); err != nil {
		t.Fatalf("register core: %v", err)
	}
	parent, err := looppkg.New(looppkg.Config{Name: "outer", Operation: looppkg.OperationContainer}, looppkg.Deps{})
	if err != nil {
		t.Fatalf("new parent: %v", err)
	}
	if err := loops.Register(parent); err != nil {
		t.Fatalf("register parent: %v", err)
	}

	r := &loopDefinitionRuntime{
		loops:  loops,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	out, err := r.runtimeSpec(looppkg.Spec{
		Name:       "child",
		Operation:  looppkg.OperationService,
		ParentName: "outer",
	})
	if err != nil {
		t.Fatalf("runtimeSpec: %v", err)
	}
	if out.ParentID != parent.ID() {
		t.Errorf("ParentID = %q, want %q", out.ParentID, parent.ID())
	}
	if out.ParentName != "outer" {
		t.Errorf("ParentName = %q, want %q (preserved on resolve)", out.ParentName, "outer")
	}
}

// TestStopLoopForReconcileSkipsContainerWithLiveChildren covers
// the reconciler-side handling of the post-#895
// [looppkg.ContainerHasChildrenError]: removing a container
// definition while its descendants are still live must not
// fail-loop reconciliation. The helper logs and returns nil so
// the next pass (after descendants stop) lands cleanly.
func TestStopLoopForReconcileSkipsContainerWithLiveChildren(t *testing.T) {
	t.Parallel()

	loops := looppkg.NewRegistry()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		loops.ShutdownAll(shutdownCtx)
	})

	core, err := looppkg.New(looppkg.Config{Name: looppkg.CoreLoopName, Operation: looppkg.OperationContainer}, looppkg.Deps{})
	if err != nil {
		t.Fatalf("new core: %v", err)
	}
	if err := loops.Register(core); err != nil {
		t.Fatalf("register core: %v", err)
	}
	container, err := looppkg.New(looppkg.Config{Name: "research", Operation: looppkg.OperationContainer}, looppkg.Deps{})
	if err != nil {
		t.Fatalf("new container: %v", err)
	}
	if err := loops.Register(container); err != nil {
		t.Fatalf("register container: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := container.Start(ctx); err != nil {
		t.Fatalf("start container: %v", err)
	}
	child, err := looppkg.New(looppkg.Config{Name: "worker", Task: "t", ParentID: container.ID()}, looppkg.Deps{Runner: testLoopRunner{}})
	if err != nil {
		t.Fatalf("new child: %v", err)
	}
	if err := loops.Register(child); err != nil {
		t.Fatalf("register child: %v", err)
	}

	var buf strings.Builder
	r := &loopDefinitionRuntime{
		loops:  loops,
		logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}
	log := r.definitionLogger("research")
	if err := r.stopLoopForReconcile(log, container.ID(), "definition_removed"); err != nil {
		t.Errorf("stopLoopForReconcile returned err = %v; want nil so reconcile converges", err)
	}
	if loops.Get(container.ID()) == nil {
		t.Error("container was deregistered despite the children-live refusal")
	}
	if !strings.Contains(buf.String(), "deferring container stop") {
		t.Errorf("expected warn log about deferral, got: %q", buf.String())
	}
}
