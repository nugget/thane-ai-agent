package toolargs

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args map[string]any
		key  string
		want string
	}{
		{"present", map[string]any{"k": "hello"}, "k", "hello"},
		{"absent", map[string]any{}, "k", ""},
		{"wrong type", map[string]any{"k": 42}, "k", ""},
		{"nil value", map[string]any{"k": nil}, "k", ""},
		{"nil map", nil, "k", ""},
		{"no trim", map[string]any{"k": "  pad  "}, "k", "  pad  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := String(tt.args, tt.key); got != tt.want {
				t.Errorf("String = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTrimmedString(t *testing.T) {
	t.Parallel()
	if got := TrimmedString(map[string]any{"k": "  pad  "}, "k"); got != "pad" {
		t.Errorf("TrimmedString = %q, want %q", got, "pad")
	}
	if got := TrimmedString(map[string]any{"k": 7}, "k"); got != "" {
		t.Errorf("TrimmedString wrong-type = %q, want empty", got)
	}
}

func TestIntOK(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		val    any
		want   int
		wantOK bool
	}{
		{"int", 5, 5, true},
		{"int32", int32(6), 6, true},
		{"int64", int64(7), 7, true},
		{"float64 integral", float64(8), 8, true},
		{"float64 fractional rejected", 8.5, 0, false},
		{"json.Number integral", json.Number("9"), 9, true},
		{"json.Number decimal-integral", json.Number("9.0"), 9, true},
		{"json.Number fractional rejected", json.Number("9.5"), 0, false},
		{"string decimal", "395", 395, true},
		{"string padded", "  42 ", 42, true},
		{"string non-numeric", "abc", 0, false},
		{"bool rejected", true, 0, false},
		{"nil value", nil, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := IntOK(map[string]any{"k": tt.val}, "k")
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("IntOK = (%d, %v), want (%d, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
	if _, ok := IntOK(map[string]any{}, "missing"); ok {
		t.Error("IntOK absent key should be not-ok")
	}
}

func TestIntAndIntOr(t *testing.T) {
	t.Parallel()
	if got := Int(map[string]any{"k": float64(3)}, "k"); got != 3 {
		t.Errorf("Int = %d, want 3", got)
	}
	if got := Int(map[string]any{}, "k"); got != 0 {
		t.Errorf("Int absent = %d, want 0", got)
	}
	if got := IntOr(map[string]any{}, "k", 99); got != 99 {
		t.Errorf("IntOr absent = %d, want 99", got)
	}
	if got := IntOr(map[string]any{"k": "abc"}, "k", 99); got != 99 {
		t.Errorf("IntOr uncoercible = %d, want fallback 99", got)
	}
}

func TestBoolOK(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		val    any
		want   bool
		wantOK bool
	}{
		{"true", true, true, true},
		{"false", false, false, true},
		{"string true", "true", true, true},
		{"string TRUE", "TRUE", true, true},
		{"string false padded", "  false ", false, true},
		{"string other", "yes", false, false},
		{"int rejected", 1, false, false},
		{"nil value", nil, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := BoolOK(map[string]any{"k": tt.val}, "k")
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("BoolOK = (%v, %v), want (%v, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestBoolAndBoolOrAndHasBool(t *testing.T) {
	t.Parallel()
	// Bool: false when absent.
	if Bool(map[string]any{}, "k") {
		t.Error("Bool absent should be false")
	}
	if !Bool(map[string]any{"k": true}, "k") {
		t.Error("Bool true should be true")
	}
	// BoolOr: the #930 omitted-default case — absent key returns the
	// documented fallback, not the zero value.
	if !BoolOr(map[string]any{}, "k", true) {
		t.Error("BoolOr absent with true fallback should be true")
	}
	if BoolOr(map[string]any{"k": false}, "k", true) {
		t.Error("BoolOr explicit false should override true fallback")
	}
	// HasBool: present vs defaulted.
	if !HasBool(map[string]any{"k": false}, "k") {
		t.Error("HasBool present false should be true")
	}
	if HasBool(map[string]any{}, "k") {
		t.Error("HasBool absent should be false")
	}
	if HasBool(map[string]any{"k": "maybe"}, "k") {
		t.Error("HasBool uncoercible should be false")
	}
}

func TestStringSlice(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  any
		want []string
	}{
		{"json array", []any{"a", "b"}, []string{"a", "b"}},
		{"array skips non-strings", []any{"a", 2, "b"}, []string{"a", "b"}},
		{"single string", "solo", []string{"solo"}},
		{"empty string", "", nil},
		{"wrong type", 42, nil},
		{"empty array", []any{}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StringSlice(map[string]any{"k": tt.val}, "k")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("StringSlice = %#v, want %#v", got, tt.want)
			}
		})
	}
	if got := StringSlice(map[string]any{}, "missing"); got != nil {
		t.Errorf("StringSlice absent = %#v, want nil", got)
	}
}

func TestUint32Slice(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		val  any
		want []uint32
	}{
		{"json array of floats", []any{float64(1), float64(2)}, []uint32{1, 2}},
		{"array skips fractional", []any{float64(1), 2.5, float64(3)}, []uint32{1, 3}},
		{"array skips zero and negative", []any{float64(0), float64(-1), float64(4)}, []uint32{4}},
		{"single float", float64(7), []uint32{7}},
		{"single string", "8", []uint32{8}},
		{"string array", []any{"10", "bad", "12"}, []uint32{10, 12}},
		{"uncoercible single", "x", nil},
		{"wrong type", true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Uint32Slice(map[string]any{"k": tt.val}, "k")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Uint32Slice = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCoerceUint32RangeCeiling(t *testing.T) {
	t.Parallel()
	// Above the uint32 ceiling must be rejected.
	if _, ok := coerceUint32(float64(maxUint32) + 1); ok {
		t.Error("coerceUint32 above ceiling should be not-ok")
	}
	if got, ok := coerceUint32(float64(maxUint32)); !ok || got != maxUint32 {
		t.Errorf("coerceUint32 at ceiling = (%d, %v), want (%d, true)", got, ok, uint32(maxUint32))
	}
}
