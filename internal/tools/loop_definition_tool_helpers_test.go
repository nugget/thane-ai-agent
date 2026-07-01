package tools

import (
	"strings"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// TestDecodeLoopSpecArgAcceptsStringifiedJSON covers #1116: models sometimes
// emit the nested spec argument as a JSON *string* rather than a native
// object (a size-correlated LLM quirk). Both shapes must decode to the same
// spec.
func TestDecodeLoopSpecArgAcceptsStringifiedJSON(t *testing.T) {
	cases := []struct {
		name string
		spec any
	}{
		{
			name: "native object",
			spec: map[string]any{
				"name":      "ranch_climate_watch",
				"task":      "Watch the barn sensors.",
				"intent":    "Keep the ranch climate within range.",
				"operation": "service",
			},
		},
		{
			name: "stringified object",
			spec: `{"name":"ranch_climate_watch","task":"Watch the barn sensors.",` +
				`"intent":"Keep the ranch climate within range.","operation":"service"}`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			spec, err := decodeLoopSpecArg(map[string]any{"spec": tc.spec}, "spec")
			if err != nil {
				t.Fatalf("decodeLoopSpecArg: unexpected error: %v", err)
			}
			if spec.Name != "ranch_climate_watch" {
				t.Errorf("Name = %q, want ranch_climate_watch", spec.Name)
			}
			if spec.Task != "Watch the barn sensors." {
				t.Errorf("Task = %q, want the sensor task", spec.Task)
			}
			if spec.Intent != "Keep the ranch climate within range." {
				t.Errorf("Intent = %q, want the climate intent", spec.Intent)
			}
			if spec.Operation != looppkg.OperationService {
				t.Errorf("Operation = %q, want service", spec.Operation)
			}
		})
	}
}

// TestDecodeLoopSpecArgStringifiedInvalidJSON verifies that a JSON-looking
// string that fails to parse surfaces a precise error rather than the opaque
// "cannot unmarshal string into Go value of type loop.specJSON".
func TestDecodeLoopSpecArgStringifiedInvalidJSON(t *testing.T) {
	_, err := decodeLoopSpecArg(map[string]any{"spec": `{"name": "oops",`}, "spec")
	if err == nil {
		t.Fatal("expected an error for a truncated JSON string, got nil")
	}
	if !strings.Contains(err.Error(), "spec was a JSON string but did not parse") {
		t.Fatalf("error = %q, want the tolerant-decode parse message", err.Error())
	}
}

// TestDecodeLoopLaunchArgAcceptsStringifiedJSON covers the same #1116 quirk on
// the launch payload (spawn_loop / loop_definition_launch).
func TestDecodeLoopLaunchArgAcceptsStringifiedJSON(t *testing.T) {
	cases := []struct {
		name   string
		launch any
	}{
		{
			name:   "native object",
			launch: map[string]any{"task": "Do the thing.", "max_iterations": 7},
		},
		{
			name:   "stringified object",
			launch: `{"task":"Do the thing.","max_iterations":7}`,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			launch, err := decodeLoopLaunchArg(map[string]any{"launch": tc.launch}, "launch")
			if err != nil {
				t.Fatalf("decodeLoopLaunchArg: unexpected error: %v", err)
			}
			if launch.Task != "Do the thing." {
				t.Errorf("Task = %q, want the launch task", launch.Task)
			}
			if launch.MaxIterations != 7 {
				t.Errorf("MaxIterations = %d, want 7", launch.MaxIterations)
			}
		})
	}
}

// TestDecodeLoopLaunchArgStringifiedStillRejectsModel guards the ordering:
// coercion must run BEFORE rejectLaunchModelKeys, so a stringified launch
// cannot smuggle a model override past the raw-map pre-check.
func TestDecodeLoopLaunchArgStringifiedStillRejectsModel(t *testing.T) {
	_, err := decodeLoopLaunchArg(
		map[string]any{"launch": `{"model":"claude-sonnet-4-5"}`},
		"launch",
	)
	if err == nil {
		t.Fatal("expected launch.model to be rejected even when the launch arrives stringified, got nil")
	}
	if !strings.Contains(err.Error(), "launch.model") || !strings.Contains(err.Error(), "spec.profile.model") {
		t.Fatalf("error = %q, want guidance pointing from launch.model to spec.profile.model", err.Error())
	}
}

// TestCoerceStringifiedJSON exercises the coercion boundary directly.
func TestCoerceStringifiedJSON(t *testing.T) {
	cases := []struct {
		name    string
		in      any
		want    any
		wantErr bool
	}{
		{name: "non-string passthrough", in: map[string]any{"a": 1}, want: map[string]any{"a": 1}},
		{name: "empty string passthrough", in: "", want: ""},
		{name: "bare string passthrough", in: "not json", want: "not json"},
		{name: "json object string decoded", in: `{"a":1}`, want: map[string]any{"a": float64(1)}},
		{name: "json array string decoded", in: `[1,2]`, want: []any{float64(1), float64(2)}},
		{name: "whitespace-led object decoded", in: "  {\"a\":1}", want: map[string]any{"a": float64(1)}},
		{name: "invalid json object string errors", in: `{"a":`, wantErr: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := coerceStringifiedJSON(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got value %#v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !jsonEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

// jsonEqual compares two decoded-JSON values structurally. Sufficient for the
// scalar/object/array shapes coerceStringifiedJSON returns.
func jsonEqual(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !jsonEqual(v, bv[k]) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		return a == b
	}
}
