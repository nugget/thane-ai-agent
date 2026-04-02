package router

import (
	"testing"
)

func TestLoopSeed_Hints(t *testing.T) {
	localOnly := true
	s := LoopSeed{
		Source:           "wake",
		Mission:          "anticipation",
		Model:            "claude-sonnet-4-20250514",
		LocalOnly:        &localOnly,
		QualityFloor:     7,
		DelegationGating: "disabled",
		ExtraHints: map[string]string{
			"wake_subscription_id": "wake_123",
		},
	}

	h := s.Hints()

	checks := map[string]string{
		"source":               "wake",
		HintMission:            "anticipation",
		HintModelPreference:    "claude-sonnet-4-20250514",
		HintLocalOnly:          "true",
		HintQualityFloor:       "7",
		HintDelegationGating:   "disabled",
		"wake_subscription_id": "wake_123",
	}

	for k, want := range checks {
		if got := h[k]; got != want {
			t.Errorf("Hints()[%q] = %q, want %q", k, got, want)
		}
	}
}

func TestLoopSeed_Hints_Defaults(t *testing.T) {
	s := LoopSeed{Source: "test"}
	h := s.Hints()

	if _, ok := h[HintLocalOnly]; ok {
		t.Error("nil LocalOnly should not produce a hint")
	}
	if _, ok := h[HintQualityFloor]; ok {
		t.Error("zero QualityFloor should not produce a hint")
	}
	if _, ok := h[HintMission]; ok {
		t.Error("empty Mission should not produce a hint")
	}
}

func TestLoopSeed_LocalOnly_False(t *testing.T) {
	localOnly := false
	s := LoopSeed{LocalOnly: &localOnly}
	h := s.Hints()

	if got := h[HintLocalOnly]; got != "false" {
		t.Errorf("Hints()[HintLocalOnly] = %q, want %q", got, "false")
	}
}
