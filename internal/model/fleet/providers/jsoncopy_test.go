package providers

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestDeepCopyJSONValue pins that every mutable value a kwarg can hold
// is detached, typed ones included, and that the copy encodes exactly
// like the original so copying can never change a request body.
func TestDeepCopyJSONValue(t *testing.T) {
	t.Parallel()

	type opts struct {
		Names []string          `json:"names"`
		Tags  map[string]string `json:"tags"`
	}

	tests := []struct {
		name   string
		build  func() any
		mutate func(orig any)
	}{
		{
			name:   "nested untyped tree",
			build:  func() any { return map[string]any{"a": []any{map[string]any{"b": "x"}}} },
			mutate: func(o any) { o.(map[string]any)["a"].([]any)[0].(map[string]any)["b"] = "mutated" },
		},
		{
			name:   "typed string slice",
			build:  func() any { return []string{"first", "second"} },
			mutate: func(o any) { o.([]string)[0] = "mutated" },
		},
		{
			name:   "typed string map",
			build:  func() any { return map[string]string{"k": "v"} },
			mutate: func(o any) { o.(map[string]string)["k"] = "mutated" },
		},
		{
			name:   "typed values inside an untyped map",
			build:  func() any { return map[string]any{"stops": []string{"a"}, "labels": map[string]string{"k": "v"}} },
			mutate: func(o any) { o.(map[string]any)["stops"].([]string)[0] = "mutated" },
		},
		{
			name:   "map of typed slices",
			build:  func() any { return map[string][]string{"k": {"v"}} },
			mutate: func(o any) { o.(map[string][]string)["k"][0] = "mutated" },
		},
		{
			name:   "pointer to struct with typed fields",
			build:  func() any { return &opts{Names: []string{"n"}, Tags: map[string]string{"t": "v"}} },
			mutate: func(o any) { o.(*opts).Names[0] = "mutated"; o.(*opts).Tags["t"] = "mutated" },
		},
		{
			name:   "array of slices",
			build:  func() any { return [2][]string{{"a"}, {"b"}} },
			mutate: func(o any) {},
		},
		{
			name:   "scalar",
			build:  func() any { return 42 },
			mutate: func(o any) {},
		},
		{
			name:   "nil",
			build:  func() any { return nil },
			mutate: func(o any) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orig := tt.build()
			want, err := json.Marshal(orig)
			if err != nil {
				t.Fatalf("marshal original: %v", err)
			}

			copied := deepCopyJSONValue(orig)
			if !reflect.DeepEqual(copied, orig) {
				t.Fatalf("copy = %#v, want deep-equal to %#v", copied, orig)
			}
			if orig != nil && reflect.TypeOf(copied) != reflect.TypeOf(orig) {
				t.Fatalf("copy type = %T, want %T", copied, orig)
			}

			tt.mutate(orig)
			got, err := json.Marshal(copied)
			if err != nil {
				t.Fatalf("marshal copy: %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("copy encodes as %s after the original was mutated, want %s", got, want)
			}
		})
	}
}
