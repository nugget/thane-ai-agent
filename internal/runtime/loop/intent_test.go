package loop

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestSpecIntent_ToConfigResolution covers the #1106 C1 promotion: ToConfig
// prefers the first-class Spec.Intent field and falls back to the legacy
// metadata["intent"] for one release.
func TestSpecIntent_ToConfigResolution(t *testing.T) {
	tests := []struct {
		name string
		spec Spec
		want string
	}{
		{"field set", Spec{Intent: "watch the lake"}, "watch the lake"},
		{"field preferred over metadata", Spec{Intent: "field wins", Metadata: map[string]string{"intent": "stale"}}, "field wins"},
		{"metadata fallback when field empty", Spec{Metadata: map[string]string{"intent": "legacy intent"}}, "legacy intent"},
		{"empty when neither set", Spec{}, ""},
		{"whitespace-only field falls back to metadata", Spec{Intent: "   ", Metadata: map[string]string{"intent": "legacy"}}, "legacy"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.spec.ToConfig().Intent; got != tc.want {
				t.Errorf("ToConfig().Intent = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSpecIntent_JSONRoundTrip confirms the first-class Intent field survives
// the custom Spec marshal/unmarshal wire format (specJSON).
func TestSpecIntent_JSONRoundTrip(t *testing.T) {
	spec := Spec{
		Name:      "canyon_lake_watch",
		Operation: OperationService,
		Intent:    "hold a current belief about the reservoir level",
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"intent":"hold a current belief about the reservoir level"`) {
		t.Errorf("marshaled JSON missing first-class intent field: %s", data)
	}
	var got Spec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Intent != spec.Intent {
		t.Errorf("round-trip Intent = %q, want %q", got.Intent, spec.Intent)
	}
}

// TestSpecIntent_LoopViewReadsResolved confirms LoopView.Intent reads the
// resolved Config.Intent — including the legacy metadata fallback, end to end.
func TestSpecIntent_LoopViewReadsResolved(t *testing.T) {
	r := NewLoopViewResolver(nil, nil, time.Now())

	fieldStatus := Status{ID: "lp_a", Name: "a", Config: (&Spec{Intent: "from field"}).ToConfig()}
	if got := r.FromStatus(fieldStatus).Intent; got != "from field" {
		t.Errorf("field intent: LoopView.Intent = %q, want %q", got, "from field")
	}

	fallbackStatus := Status{ID: "lp_b", Name: "b", Config: (&Spec{Metadata: map[string]string{"intent": "from metadata"}}).ToConfig()}
	if got := r.FromStatus(fallbackStatus).Intent; got != "from metadata" {
		t.Errorf("fallback intent: LoopView.Intent = %q, want %q", got, "from metadata")
	}
}
