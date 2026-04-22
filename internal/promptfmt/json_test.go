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
	})

	t.Run("no newlines", func(t *testing.T) {
		got := MarshalCompact(map[string]any{"x": []int{1, 2, 3}})
		if strings.ContainsAny(got, "\n\t") {
			t.Errorf("MarshalCompact produced whitespace: %q", got)
		}
	})

	t.Run("unmarshalable falls back to fmt", func(t *testing.T) {
		// json.Marshal rejects NaN; ensure we still return a non-empty
		// string so the prompt path never crashes.
		got := MarshalCompact(math.NaN())
		if got == "" {
			t.Error("MarshalCompact returned empty string on unmarshalable input")
		}
		if strings.Contains(got, "{") || strings.Contains(got, "[") {
			t.Errorf("MarshalCompact fallback should not look like JSON: %q", got)
		}
	})
}
