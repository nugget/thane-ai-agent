package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func vocabTestServer(t *testing.T) *fakeHAServer {
	t.Helper()
	fake := newFakeHAServer(t)
	fake.areas = []map[string]any{
		{"area_id": "office", "name": "Office", "aliases": []string{"study"}},
	}
	fake.targetTriggers = []string{"light.turned_off", "light.turned_on", "motion.detected"}
	fake.targetConds = []string{"light.is_on"}
	fake.targetServices = []string{"light.turn_on", "light.turn_off", "light.toggle"}
	return fake
}

func TestHAAutomationVocabulary_ResolvesTargetAndReturnsVocabulary(t *testing.T) {
	fake := vocabTestServer(t)
	reg := fake.registry(t)

	raw, err := reg.Execute(context.Background(), "ha_automation_vocabulary", `{"target":{"area_id":"Office"}}`)
	if err != nil {
		t.Fatalf("ha_automation_vocabulary: %v", err)
	}
	var out haVocabularyResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, raw)
	}
	// Target name resolved to the registry id.
	if out.Target["area_id"] != "office" {
		t.Errorf("target.area_id = %v, want resolved \"office\"", out.Target["area_id"])
	}
	// All three vocabularies present and sorted.
	if len(out.Triggers) != 3 || out.Triggers[0] != "light.turned_off" {
		t.Errorf("triggers = %v, want 3 sorted", out.Triggers)
	}
	if len(out.Conditions) != 1 || len(out.Services) != 3 {
		t.Errorf("conditions/services = %v/%v", out.Conditions, out.Services)
	}
	if out.Note == "" {
		t.Error("result should carry authoring guidance note")
	}
}

func TestHAAutomationVocabulary_EmptyTargetCarriesGuidance(t *testing.T) {
	fake := vocabTestServer(t)
	fake.targetTriggers = []string{}
	fake.targetConds = []string{}
	fake.targetServices = []string{}
	reg := fake.registry(t)

	raw, err := reg.Execute(context.Background(), "ha_automation_vocabulary", `{"target":{"area_id":"office"}}`)
	if err != nil {
		t.Fatalf("vocabulary: %v", err)
	}
	var out haVocabularyResult
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Triggers) != 0 || out.Note == haVocabularyNote {
		t.Errorf("empty vocabulary should carry the diagnostic note, got %q", out.Note)
	}
}

func TestHAAutomationVocabulary_UnknownTargetFailsFast(t *testing.T) {
	fake := vocabTestServer(t)
	reg := fake.registry(t)

	if _, err := reg.Execute(context.Background(), "ha_automation_vocabulary", `{"target":{"area_id":"Atrium"}}`); err == nil {
		t.Error("unknown area should fail fast (reuses ha_call_service target resolution)")
	}
	if _, err := reg.Execute(context.Background(), "ha_automation_vocabulary", `{}`); err == nil {
		t.Error("missing target should error")
	}
}
