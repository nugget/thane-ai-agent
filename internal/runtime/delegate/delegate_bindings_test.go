package delegate

import (
	"context"
	"log/slog"
	"testing"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestDelegateCarriesCallerBindings closes the laundering path. A
// delegate is the caller acting through another turn, so a boundary
// the caller is inside must travel with the work: otherwise a loop
// bound to a read-only forge account could reach the write account by
// asking a delegate to do it, and the binding would restrict only the
// callers too direct to route around it.
//
// The registry cascade does not cover this on its own. A delegate's
// parent is the calling loop, and effectiveState inherits only from
// container ancestors — a service or request-reply caller is skipped —
// so the binding has to be carried explicitly onto the launched spec.
func TestDelegateCarriesCallerBindings(t *testing.T) {
	t.Parallel()

	newPrep := func(t *testing.T, ctx context.Context) *preparedExecution {
		t.Helper()
		exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "spark/gpt-oss:20b")
		prep, err := exec.prepareExecution(ctx, "audit the queue", "", "", nil, executionOptions{})
		if err != nil {
			t.Fatalf("prepareExecution: %v", err)
		}
		return prep
	}

	boundCtx := looppkg.WithBindings(context.Background(), map[string]string{
		looppkg.BindingForgeAccount: "github-readonly",
	})

	t.Run("prepareExecution captures the caller's bindings", func(t *testing.T) {
		t.Parallel()
		prep := newPrep(t, boundCtx)
		if got := prep.bindings[looppkg.BindingForgeAccount]; got != "github-readonly" {
			t.Errorf("captured binding = %q, want %q", got, "github-readonly")
		}
	})

	t.Run("an unbound caller stays unbound", func(t *testing.T) {
		t.Parallel()
		prep := newPrep(t, context.Background())
		if len(prep.bindings) != 0 {
			t.Errorf("bindings = %v, want none for an unbound caller", prep.bindings)
		}
	})

	// Both delegate surfaces build their launch through buildLoopLaunch:
	// thane_assign as a background task, thane_now as request-reply. The
	// binding has to reach both, so both are asserted rather than
	// trusting that one implies the other.
	for _, op := range []looppkg.Operation{
		looppkg.OperationBackgroundTask,
		looppkg.OperationRequestReply,
	} {
		t.Run("launch carries the binding for "+string(op), func(t *testing.T) {
			t.Parallel()
			exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "spark/gpt-oss:20b")
			prep := newPrep(t, boundCtx)

			launch := exec.buildLoopLaunch(prep, "audit the queue", "", op,
				looppkg.CompletionReturn, "", nil, "delegate-test", time.Minute, nil)

			if got := launch.Spec.Bindings[looppkg.BindingForgeAccount]; got != "github-readonly" {
				t.Errorf("launched spec binding = %q, want %q — a bound caller could delegate its way to another account", got, "github-readonly")
			}
		})
	}

	t.Run("the launched spec does not alias the caller's map", func(t *testing.T) {
		t.Parallel()
		exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "spark/gpt-oss:20b")
		prep := newPrep(t, boundCtx)

		launch := exec.buildLoopLaunch(prep, "audit", "", looppkg.OperationRequestReply,
			looppkg.CompletionReturn, "", nil, "delegate-test", time.Minute, nil)
		launch.Spec.Bindings[looppkg.BindingForgeAccount] = "github-primary"

		if got := prep.bindings[looppkg.BindingForgeAccount]; got != "github-readonly" {
			t.Errorf("caller's binding = %q after mutating the launched spec; the two share a map", got)
		}
	})

	t.Run("the launched spec validates", func(t *testing.T) {
		t.Parallel()
		exec := NewExecutor(slog.Default(), nil, nil, newTestRegistry(), "spark/gpt-oss:20b")
		prep := newPrep(t, boundCtx)

		launch := exec.buildLoopLaunch(prep, "audit", "", looppkg.OperationRequestReply,
			looppkg.CompletionReturn, "", nil, "delegate-test", time.Minute, nil)
		if err := looppkg.ValidateBindings(launch.Spec.Bindings); err != nil {
			t.Errorf("carried bindings do not validate: %v", err)
		}
	})
}
