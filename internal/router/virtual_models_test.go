package router

import (
	"log/slog"
	"testing"
)

func TestExposedVirtualModels(t *testing.T) {
	models := ExposedVirtualModels(VirtualModelRuntime{PremiumQualityFloor: "9"})
	var names []string
	for _, model := range models {
		names = append(names, model.Name)
	}
	want := []string{
		"thane:latest",
		"thane:premium",
		"thane:ops",
		"thane:assist",
		"thane:local",
	}
	if len(names) != len(want) {
		t.Fatalf("len(names) = %d, want %d (%v)", len(names), len(want), names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("names[%d] = %q, want %q (all=%v)", i, names[i], want[i], names)
		}
	}
}

func TestResolveVirtualModelSelection_PremiumAddsDelegatePolicy(t *testing.T) {
	selection := ResolveVirtualModelSelection("thane:premium", map[string]string{"channel": "api"}, VirtualModelRuntime{
		PremiumQualityFloor: "9",
	}, slog.Default())

	if !selection.Known {
		t.Fatal("Known = false, want true")
	}
	if selection.CanonicalName != "thane:premium" {
		t.Fatalf("CanonicalName = %q, want thane:premium", selection.CanonicalName)
	}
	if selection.Model != "" {
		t.Fatalf("Model = %q, want empty", selection.Model)
	}
	if selection.Hints[HintQualityFloor] != "9" {
		t.Fatalf("quality_floor = %q, want 9", selection.Hints[HintQualityFloor])
	}
	if selection.Hints[HintVirtualModel] != "thane:premium" {
		t.Fatalf("virtual_model = %q, want thane:premium", selection.Hints[HintVirtualModel])
	}
	if selection.Hints[DelegateHintKey(HintQualityFloor)] != "9" {
		t.Fatalf("delegate quality_floor = %q, want 9", selection.Hints[DelegateHintKey(HintQualityFloor)])
	}
	if selection.Hints[DelegateHintKey(HintLocalOnly)] != "false" {
		t.Fatalf("delegate local_only = %q, want false", selection.Hints[DelegateHintKey(HintLocalOnly)])
	}
	if selection.Hints[DelegateHintKey(HintPreferSpeed)] != "false" {
		t.Fatalf("delegate prefer_speed = %q, want false", selection.Hints[DelegateHintKey(HintPreferSpeed)])
	}
}

func TestOverlayDelegateHints(t *testing.T) {
	explicitModel, merged := OverlayDelegateHints(map[string]string{
		HintLocalOnly:   "true",
		HintPreferSpeed: "true",
	}, map[string]string{
		HintDelegateModel:                    "claude-opus-4-20250514",
		DelegateHintKey(HintQualityFloor):    "10",
		DelegateHintKey(HintLocalOnly):       "false",
		DelegateHintKey(HintPreferSpeed):     "false",
		DelegateHintKey(HintMission):         "device_control",
		DelegateHintKey(HintModelPreference): "spark/gpt-oss:120b",
	})

	if explicitModel != "claude-opus-4-20250514" {
		t.Fatalf("explicitModel = %q, want claude-opus-4-20250514", explicitModel)
	}
	if merged[HintQualityFloor] != "10" {
		t.Fatalf("quality_floor = %q, want 10", merged[HintQualityFloor])
	}
	if merged[HintLocalOnly] != "false" {
		t.Fatalf("local_only = %q, want false", merged[HintLocalOnly])
	}
	if merged[HintPreferSpeed] != "false" {
		t.Fatalf("prefer_speed = %q, want false", merged[HintPreferSpeed])
	}
	if merged[HintMission] != "device_control" {
		t.Fatalf("mission = %q, want device_control", merged[HintMission])
	}
	if merged[HintModelPreference] != "spark/gpt-oss:120b" {
		t.Fatalf("model_preference = %q, want spark/gpt-oss:120b", merged[HintModelPreference])
	}
}
