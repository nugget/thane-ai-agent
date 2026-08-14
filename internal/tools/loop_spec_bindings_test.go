package tools

import (
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestDecodeLoopSpecArgPreservesBindings covers the path the agent
// itself authors through. thane_loop_create and loop_definition_set
// decode their spec argument as JSON, so the same wire type that
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
