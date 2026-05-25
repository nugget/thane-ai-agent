package router

import (
	"log/slog"
	"testing"
)

func TestExposedVirtualModels(t *testing.T) {
	models := ExposedVirtualModels(VirtualModelRuntime{PremiumQualityFloor: 9})
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

func TestResolveVirtualModelSelection_PremiumKeepsDelegatePolicyAdaptive(t *testing.T) {
	selection := ResolveVirtualModelSelection("thane:premium", map[string]string{"channel": "api"}, VirtualModelRuntime{
		PremiumQualityFloor: 9,
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
	if selection.RoutingFactors[FactorQualityFloor] != "9" {
		t.Fatalf("quality_floor = %q, want 9", selection.RoutingFactors[FactorQualityFloor])
	}
	if selection.RoutingFactors[HintVirtualModel] != "thane:premium" {
		t.Fatalf("virtual_model = %q, want thane:premium", selection.RoutingFactors[HintVirtualModel])
	}
	for _, hint := range []string{FactorQualityFloor, FactorLocalOnly, FactorPreferSpeed, FactorMission} {
		if got := selection.RoutingFactors[DelegateHintKey(hint)]; got != "" {
			t.Fatalf("delegate %s = %q, want empty", hint, got)
		}
	}
	if got := selection.RoutingFactors[HintDelegateModel]; got != "" {
		t.Fatalf("delegate model = %q, want empty", got)
	}
}

func TestResolveVirtualModelSelection_OpsAddsDelegatePolicy(t *testing.T) {
	selection := ResolveVirtualModelSelection("thane:ops", map[string]string{"channel": "api"}, VirtualModelRuntime{
		PremiumQualityFloor: 9,
	}, slog.Default())

	if !selection.Known {
		t.Fatal("Known = false, want true")
	}
	if selection.CanonicalName != "thane:ops" {
		t.Fatalf("CanonicalName = %q, want thane:ops", selection.CanonicalName)
	}
	if selection.RoutingFactors[HintVirtualModel] != "thane:ops" {
		t.Fatalf("virtual_model = %q, want thane:ops", selection.RoutingFactors[HintVirtualModel])
	}
	if selection.RoutingFactors[DelegateHintKey(FactorQualityFloor)] != "9" {
		t.Fatalf("delegate quality_floor = %q, want 9", selection.RoutingFactors[DelegateHintKey(FactorQualityFloor)])
	}
	if selection.RoutingFactors[DelegateHintKey(FactorLocalOnly)] != "false" {
		t.Fatalf("delegate local_only = %q, want false", selection.RoutingFactors[DelegateHintKey(FactorLocalOnly)])
	}
	if selection.RoutingFactors[DelegateHintKey(FactorPreferSpeed)] != "false" {
		t.Fatalf("delegate prefer_speed = %q, want false", selection.RoutingFactors[DelegateHintKey(FactorPreferSpeed)])
	}
}

func TestOverlayDelegateHints(t *testing.T) {
	explicitModel, merged := OverlayDelegateHints(map[string]string{
		FactorLocalOnly:   "true",
		FactorPreferSpeed: "true",
	}, map[string]string{
		HintDelegateModel:                      "claude-opus-4-20250514",
		DelegateHintKey(FactorQualityFloor):    "10",
		DelegateHintKey(FactorLocalOnly):       "false",
		DelegateHintKey(FactorPreferSpeed):     "false",
		DelegateHintKey(FactorMission):         "device_control",
		DelegateHintKey(FactorModelPreference): "spark/gpt-oss:120b",
	})

	if explicitModel != "claude-opus-4-20250514" {
		t.Fatalf("explicitModel = %q, want claude-opus-4-20250514", explicitModel)
	}
	if merged[FactorQualityFloor] != "10" {
		t.Fatalf("quality_floor = %q, want 10", merged[FactorQualityFloor])
	}
	if merged[FactorLocalOnly] != "false" {
		t.Fatalf("local_only = %q, want false", merged[FactorLocalOnly])
	}
	if merged[FactorPreferSpeed] != "false" {
		t.Fatalf("prefer_speed = %q, want false", merged[FactorPreferSpeed])
	}
	if merged[FactorMission] != "device_control" {
		t.Fatalf("mission = %q, want device_control", merged[FactorMission])
	}
	if merged[FactorModelPreference] != "spark/gpt-oss:120b" {
		t.Fatalf("model_preference = %q, want spark/gpt-oss:120b", merged[FactorModelPreference])
	}
}
