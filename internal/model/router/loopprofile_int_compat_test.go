package router

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestLoopProfileUnmarshalQualityFloorAcceptsInt covers the
// canonical post-PR-Q1 wire form for `quality_floor`: a JSON
// integer decodes directly onto the int field.
func TestLoopProfileUnmarshalQualityFloorAcceptsInt(t *testing.T) {
	t.Parallel()

	var p LoopProfile
	if err := json.Unmarshal([]byte(`{"quality_floor": 7}`), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if p.QualityFloor != 7 {
		t.Errorf("QualityFloor = %d, want 7", p.QualityFloor)
	}
}

// TestLoopProfileUnmarshalQualityFloorAcceptsLegacyString covers
// the backwards-compatibility path for persisted overlay specs
// and operator config files written with the pre-PR-Q1 string
// form. Without this, an upgrade from a pre-PR-Q1 deployment
// would silently drop the operator's configured quality floor
// (or fail the load entirely) on first restart.
func TestLoopProfileUnmarshalQualityFloorAcceptsLegacyString(t *testing.T) {
	t.Parallel()

	var p LoopProfile
	if err := json.Unmarshal([]byte(`{"quality_floor": "7"}`), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if p.QualityFloor != 7 {
		t.Errorf("QualityFloor = %d, want 7 (legacy string form)", p.QualityFloor)
	}
}

// TestLoopProfileUnmarshalQualityFloorEmptyOrMissingIsZero pins
// the "unset" contract: missing key, null value, and empty
// string all decode to zero (the canonical "let the router
// pick" sentinel).
func TestLoopProfileUnmarshalQualityFloorEmptyOrMissingIsZero(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
	}{
		{"missing", `{}`},
		{"null", `{"quality_floor": null}`},
		{"empty string", `{"quality_floor": ""}`},
		{"whitespace string", `{"quality_floor": "   "}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p LoopProfile
			if err := json.Unmarshal([]byte(tc.raw), &p); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if p.QualityFloor != 0 {
				t.Errorf("QualityFloor = %d, want 0 (unset)", p.QualityFloor)
			}
		})
	}
}

// TestLoopProfileUnmarshalQualityFloorMalformedFails ensures
// garbage is loud rather than silently treated as zero. A
// malformed value should fail the Unmarshal so the caller
// notices.
func TestLoopProfileUnmarshalQualityFloorMalformedFails(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
	}{
		{"non-numeric string", `{"quality_floor": "high"}`},
		{"bool", `{"quality_floor": true}`},
		{"array", `{"quality_floor": [7]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p LoopProfile
			err := json.Unmarshal([]byte(tc.raw), &p)
			if err == nil {
				t.Fatalf("Unmarshal accepted garbage: %s", tc.raw)
			}
			if !strings.Contains(err.Error(), "quality_floor") {
				t.Errorf("error %q should mention quality_floor", err)
			}
		})
	}
}

// TestLoopProfileMarshalEmitsIntQualityFloor confirms the
// canonical wire shape is int — a marshal round-trip never
// regresses to the legacy string form.
func TestLoopProfileMarshalEmitsIntQualityFloor(t *testing.T) {
	t.Parallel()

	p := LoopProfile{QualityFloor: 5}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	js := string(out)
	if !strings.Contains(js, `"quality_floor":5`) {
		t.Errorf("Marshal didn't emit int quality_floor: %s", js)
	}
	if strings.Contains(js, `"quality_floor":"5"`) {
		t.Errorf("Marshal regressed to legacy string form: %s", js)
	}
}
