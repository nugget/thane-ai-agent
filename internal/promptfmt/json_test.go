package promptfmt

import (
	"math"
	"strings"
	"testing"
)

func TestMarshalCompact(t *testing.T) {
	t.Run("struct", func(t *testing.T) {
		got := MarshalCompact(struct {
			A int    `json:"a"`
			B string `json:"b"`
		}{A: 1, B: "two"})
		want := `{"a":1,"b":"two"}`
		if got != want {
			t.Errorf("MarshalCompact(struct) = %q, want %q", got, want)
		}
		if HasMarshalError(got) {
			t.Errorf("HasMarshalError reported true for a successful marshal: %q", got)
		}
	})

	t.Run("no newlines", func(t *testing.T) {
		got := MarshalCompact(map[string]any{"x": []int{1, 2, 3}})
		if strings.ContainsAny(got, "\n\t") {
			t.Errorf("MarshalCompact produced whitespace: %q", got)
		}
	})

	t.Run("unmarshalable scalar uses sentinel fallback", func(t *testing.T) {
		// json.Marshal rejects NaN; the fallback must start with the
		// stable sentinel prefix so downstream code can detect the
		// failure mode without inspecting output shape.
		got := MarshalCompact(math.NaN())
		if !HasMarshalError(got) {
			t.Errorf("expected marshal-error sentinel in %q", got)
		}
	})

	t.Run("unmarshalable struct uses sentinel fallback", func(t *testing.T) {
		// Struct values defeat the old "no JSON delimiters" heuristic
		// because fmt's %v verb renders structs with braces. Ensure the
		// sentinel prefix still identifies the failure unambiguously.
		type badFloat struct{ V float64 }
		got := MarshalCompact(badFloat{V: math.Inf(1)})
		if !HasMarshalError(got) {
			t.Errorf("expected marshal-error sentinel for struct-with-Inf, got %q", got)
		}
	})
}
