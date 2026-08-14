package tools

import (
	"context"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestDecodeLoopSpecArgPreservesBindings covers the full-spec path the
// agent authors through. loop_definition_set decodes its spec argument
// as JSON, so the same wire type that
// carries a binding into storage also carries it in from the model.
// The schema advertises `bindings`; a decode that dropped the field
// would let the model declare a boundary, report success, and run
// without it — the failure being invisible from either end.
func TestDecodeLoopSpecArgPreservesBindings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec any
	}{
		{
			name: "object argument",
			spec: map[string]any{
				"name":      "watcher",
				"operation": "service",
				"task":      "watch",
				"bindings":  map[string]any{looppkg.BindingForgeAccount: "github-readonly"},
			},
		},
		{
			// Models frequently pass the whole spec as a JSON string;
			// the helper coerces it, and the binding must survive that
			// path too.
			name: `stringified JSON argument`,
			spec: `{"name":"watcher","operation":"service","task":"watch","bindings":{"forge_account":"github-readonly"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			spec, err := decodeLoopSpecArg(map[string]any{"spec": tt.spec}, "spec")
			if err != nil {
				t.Fatalf("decodeLoopSpecArg: %v", err)
			}
			if got := spec.Bindings[looppkg.BindingForgeAccount]; got != "github-readonly" {
				t.Errorf("decoded binding = %q, want %q — a model-declared boundary was dropped on the way in", got, "github-readonly")
			}
		})
	}
}

// TestAdHocLaunchCannotEscapeCallerBinding covers spawn_loop, the
// other way a bound loop can start a new turn. The spec here is
// model-written, so the caller's boundary has to be merged over it —
// and merged caller-first, or a loop bound to a read-only account
// could spawn one naming the write account and reach it that way.
//
// The registry cascade does not cover this: a spawning service loop
// is not a container ancestor, so the spawned loop would inherit
// nothing from it.
func TestAdHocLaunchCannotEscapeCallerBinding(t *testing.T) {
	t.Parallel()

	boundCtx := looppkg.WithBindings(context.Background(), map[string]string{
		looppkg.BindingForgeAccount: "github-readonly",
	})

	t.Run("an unbound spec inherits the caller's binding", func(t *testing.T) {
		t.Parallel()
		launch, _ := applyAdHocLoopLaunchContextDefaults(boundCtx, looppkg.Launch{
			Spec: looppkg.Spec{Name: "spawned", Operation: looppkg.OperationBackgroundTask},
		})
		if got := launch.Spec.Bindings[looppkg.BindingForgeAccount]; got != "github-readonly" {
			t.Errorf("spawned binding = %q, want the caller's %q", got, "github-readonly")
		}
	})

	t.Run("a spec naming another account cannot override the caller", func(t *testing.T) {
		t.Parallel()
		launch, _ := applyAdHocLoopLaunchContextDefaults(boundCtx, looppkg.Launch{
			Spec: looppkg.Spec{
				Name:      "spawned",
				Operation: looppkg.OperationBackgroundTask,
				Bindings:  map[string]string{looppkg.BindingForgeAccount: "github-primary"},
			},
		})
		if got := launch.Spec.Bindings[looppkg.BindingForgeAccount]; got != "github-readonly" {
			t.Errorf("spawned binding = %q; a model-authored spec overrode the caller's boundary", got)
		}
	})

	t.Run("an unbound caller leaves the spec alone", func(t *testing.T) {
		t.Parallel()
		launch, _ := applyAdHocLoopLaunchContextDefaults(context.Background(), looppkg.Launch{
			Spec: looppkg.Spec{
				Name:      "spawned",
				Operation: looppkg.OperationBackgroundTask,
				Bindings:  map[string]string{looppkg.BindingForgeAccount: "github-primary"},
			},
		})
		if got := launch.Spec.Bindings[looppkg.BindingForgeAccount]; got != "github-primary" {
			t.Errorf("spawned binding = %q, want the spec's own %q when the caller is unbound", got, "github-primary")
		}
	})
}
