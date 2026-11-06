package providers

import (
	"bytes"
	"encoding/json"
	"testing"
)

// selfRef points back at itself through a field JSON ignores, so it
// encodes fine but would send a naive traversal round forever.
type selfRef struct {
	Name   string   `json:"name"`
	Parent *selfRef `json:"-"`
}

// privateBox keeps its state in an unexported map that only its
// marshaler reads, so no reflective copy can reach it.
type privateBox struct{ m map[string]string }

func (p *privateBox) MarshalJSON() ([]byte, error) { return json.Marshal(p.m) }
func (p *privateBox) Set(k, v string)              { p.m[k] = v }

// canonicalJSON encodes v, decodes it with lossless numbers and encodes
// it again, so two values compare by JSON meaning with sorted keys.
func canonicalJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var plain any
	if err := dec.Decode(&plain); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	return string(out)
}

// assertPlainJSON fails unless v is built only from the types the JSON
// decoder produces, so nothing in it can alias a caller's value.
func assertPlainJSON(t *testing.T, v any) {
	t.Helper()
	switch typed := v.(type) {
	case nil, string, bool, json.Number:
	case map[string]any:
		for _, val := range typed {
			assertPlainJSON(t, val)
		}
	case []any:
		for _, val := range typed {
			assertPlainJSON(t, val)
		}
	default:
		t.Errorf("copy holds %T, want only decoded JSON types", v)
	}
}

// TestDeepCopyJSONValue pins that the copy carries the same JSON value
// as the original, shares nothing with it however the original is
// built, and never walks what JSON ignores.
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
			name:   "pointer to struct with typed fields",
			build:  func() any { return &opts{Names: []string{"n"}, Tags: map[string]string{"t": "v"}} },
			mutate: func(o any) { o.(*opts).Names[0] = "mutated"; o.(*opts).Tags["t"] = "mutated" },
		},
		{
			name: "self-reference behind an ignored field",
			build: func() any {
				n := &selfRef{Name: "root"}
				n.Parent = n
				return map[string]any{"node": n}
			},
			mutate: func(o any) { o.(map[string]any)["node"].(*selfRef).Name = "mutated" },
		},
		{
			name:   "marshaler over private state",
			build:  func() any { return map[string]any{"box": &privateBox{m: map[string]string{"k": "v"}}} },
			mutate: func(o any) { o.(map[string]any)["box"].(*privateBox).Set("k", "mutated") },
		},
		{
			name:   "numbers keep their literal text",
			build:  func() any { return map[string]any{"max": uint64(18446744073709551615), "tenth": 0.1, "int": 42} },
			mutate: func(o any) { o.(map[string]any)["int"] = 7 },
		},
		{
			name:   "scalar",
			build:  func() any { return 42 },
			mutate: func(any) {},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orig := tt.build()
			want := canonicalJSON(t, orig)

			copied := deepCopyJSONValue(orig)
			assertPlainJSON(t, copied)
			if got := canonicalJSON(t, copied); got != want {
				t.Fatalf("copy encodes as %s, want %s", got, want)
			}

			tt.mutate(orig)
			if got := canonicalJSON(t, copied); got != want {
				t.Errorf("copy encodes as %s after the original was mutated, want %s", got, want)
			}
		})
	}
}

// TestDeepCopyJSONValueEdges pins the two values that are returned as
// they are: nil, and a value the encoder rejects, which no request can
// ever send.
func TestDeepCopyJSONValueEdges(t *testing.T) {
	t.Parallel()
	if got := deepCopyJSONValue(nil); got != nil {
		t.Errorf("nil copies as %#v, want nil", got)
	}
	ch := make(chan int)
	if got, ok := deepCopyJSONValue(ch).(chan int); !ok || got != ch {
		t.Errorf("unencodable value copies as %#v, want it returned unchanged", got)
	}
}
