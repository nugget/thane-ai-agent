package metacognitive

import (
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// --- Test helpers ---

func testConfig() Config {
	return Config{
		Enabled:                true,
		StateFile:              "metacognitive.md",
		MinSleep:               2 * time.Minute,
		MaxSleep:               30 * time.Minute,
		DefaultSleep:           10 * time.Minute,
		Jitter:                 0.0, // deterministic by default
		SupervisorProbability:  0.0,
		QualityFloor:           3,
		SupervisorQualityFloor: 8,
	}
}

// --- ParseConfig tests ---

func TestParseConfig_Valid(t *testing.T) {
	jitter, supervisor := 0.2, 0.1
	raw := config.MetacognitiveConfig{
		Enabled:               true,
		MinSleep:              "2m",
		MaxSleep:              "30m",
		DefaultSleep:          "10m",
		Jitter:                &jitter,
		SupervisorProbability: &supervisor,
		Router:                config.RouterConfig{QualityFloor: 3},
		SupervisorRouter:      config.RouterConfig{QualityFloor: 8},
	}

	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	if cfg.MinSleep != 2*time.Minute {
		t.Errorf("MinSleep = %v, want 2m", cfg.MinSleep)
	}
	if cfg.MaxSleep != 30*time.Minute {
		t.Errorf("MaxSleep = %v, want 30m", cfg.MaxSleep)
	}
	if cfg.DefaultSleep != 10*time.Minute {
		t.Errorf("DefaultSleep = %v, want 10m", cfg.DefaultSleep)
	}
	if cfg.StateFile != "metacognitive.md" {
		t.Errorf("StateFile = %q, want %q", cfg.StateFile, "metacognitive.md")
	}
	if cfg.QualityFloor != 3 {
		t.Errorf("QualityFloor = %d, want 3", cfg.QualityFloor)
	}
	if cfg.SupervisorQualityFloor != 8 {
		t.Errorf("SupervisorQualityFloor = %d, want 8", cfg.SupervisorQualityFloor)
	}
}

func TestParseConfig_InvalidDuration(t *testing.T) {
	tests := []struct {
		name string
		raw  config.MetacognitiveConfig
	}{
		{"bad_min_sleep", config.MetacognitiveConfig{MinSleep: "bogus", MaxSleep: "30m", DefaultSleep: "10m"}},
		{"bad_max_sleep", config.MetacognitiveConfig{MinSleep: "2m", MaxSleep: "bogus", DefaultSleep: "10m"}},
		{"bad_default_sleep", config.MetacognitiveConfig{MinSleep: "2m", MaxSleep: "30m", DefaultSleep: "bogus"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseConfig(tt.raw)
			if err == nil {
				t.Error("ParseConfig should fail for invalid duration")
			}
		})
	}
}

// --- Hydrated spec/config projection tests ---

func TestHydratedSpec(t *testing.T) {
	cfg := testConfig()
	spec := HydrateSpec(DefinitionSpec(cfg), cfg)
	if spec.Name != "metacognitive" {
		t.Errorf("Name = %q, want metacognitive", spec.Name)
	}
	if spec.Operation != loop.OperationService {
		t.Errorf("Operation = %q, want %q", spec.Operation, loop.OperationService)
	}
	if spec.Completion != loop.CompletionNone {
		t.Errorf("Completion = %q, want %q", spec.Completion, loop.CompletionNone)
	}
	if spec.Profile.Mission != "metacognitive" {
		t.Errorf("Profile.Mission = %q, want metacognitive", spec.Profile.Mission)
	}
	if spec.Profile.DelegationGating != "disabled" {
		t.Errorf("Profile.DelegationGating = %q, want disabled", spec.Profile.DelegationGating)
	}
	if spec.Profile.ExtraHints["source"] != "metacognitive" {
		t.Errorf("Profile.ExtraHints[source] = %q, want metacognitive", spec.Profile.ExtraHints["source"])
	}
	if len(spec.Tags) != 1 || spec.Tags[0] != "metacognitive" {
		t.Errorf("Tags = %v, want [metacognitive]", spec.Tags)
	}
	if len(spec.Outputs) != 1 {
		t.Fatalf("Outputs len = %d, want 1", len(spec.Outputs))
	}
	if spec.Outputs[0].Name != "metacognitive_state" {
		t.Errorf("Outputs[0].Name = %q, want metacognitive_state", spec.Outputs[0].Name)
	}
	if spec.Outputs[0].Ref != "self:metacognitive.md" {
		t.Errorf("Outputs[0].Ref = %q, want self:metacognitive.md", spec.Outputs[0].Ref)
	}
}

func TestDefinitionSpecPersistable(t *testing.T) {
	cfg := testConfig()

	spec := DefinitionSpec(cfg)
	if spec.Name != DefinitionName {
		t.Errorf("Name = %q, want %q", spec.Name, DefinitionName)
	}
	if spec.TaskBuilder != nil || spec.PostIterate != nil || spec.Setup != nil {
		t.Fatal("DefinitionSpec should not include runtime hooks")
	}
	if err := spec.ValidatePersistable(); err != nil {
		t.Fatalf("ValidatePersistable: %v", err)
	}
}

func TestHydratedConfig(t *testing.T) {
	cfg := testConfig()
	spec := HydrateSpec(DefinitionSpec(cfg), cfg)
	lc := spec.ToConfig()

	if lc.Name != "metacognitive" {
		t.Errorf("Name = %q, want metacognitive", lc.Name)
	}
	if lc.SleepMin != cfg.MinSleep {
		t.Errorf("SleepMin = %v, want %v", lc.SleepMin, cfg.MinSleep)
	}
	if lc.SleepMax != cfg.MaxSleep {
		t.Errorf("SleepMax = %v, want %v", lc.SleepMax, cfg.MaxSleep)
	}
	if lc.SleepDefault != cfg.DefaultSleep {
		t.Errorf("SleepDefault = %v, want %v", lc.SleepDefault, cfg.DefaultSleep)
	}
	if lc.Jitter == nil || *lc.Jitter != cfg.Jitter {
		t.Errorf("Jitter = %v, want %v", lc.Jitter, cfg.Jitter)
	}
	if lc.TaskBuilder != nil {
		t.Error("metacognitive loop is declarative; no TaskBuilder expected")
	}
	if !strings.Contains(lc.Task, "Metacognitive loop iteration") {
		t.Error("lc.Task should be the metacognitive base prompt")
	}
	if lc.PostIterate != nil {
		t.Error("metacognitive attaches no PostIterate hook; its iteration history is the state document's own signed history")
	}
	// Profile-derived routing factors (mission/source) and DelegationGating
	// are applied by the live loop at request time via Profile.RequestOptions,
	// not by ToConfig — so they are asserted on the Spec in TestHydratedSpec
	// rather than on this bare Config projection.

	// Verify ExcludeTools contains key entries.
	excluded := make(map[string]bool)
	for _, name := range lc.ExcludeTools {
		excluded[name] = true
	}
	for _, want := range []string{"file_grep", "file_write", "exec"} {
		if !excluded[want] {
			t.Errorf("expected %q in ExcludeTools", want)
		}
	}
}

func TestHydratedConfig_Task(t *testing.T) {
	cfg := testConfig()
	spec := HydrateSpec(DefinitionSpec(cfg), cfg)
	lc := spec.ToConfig()

	// The prompt is now a static Task (no TaskBuilder); current state
	// comes from the declared output context, not inlined into the prompt.
	if lc.TaskBuilder != nil {
		t.Error("metacognitive loop is declarative; no TaskBuilder expected")
	}
	if !strings.Contains(lc.Task, "replace_output_metacognitive_state") {
		t.Error("Task should mention the generated output tool")
	}
	if !strings.Contains(lc.Task, "Declared Durable") {
		t.Error("Task should point to the declared output context")
	}
	if strings.Contains(lc.Task, "does not exist yet") {
		t.Error("Task should not carry the old first-iteration placeholder")
	}
}

func TestMetacogExcludeTools_ExcludesLoopCreation(t *testing.T) {
	// thane_loop_create is Core (#1106 A) so it bypasses the loops tag gate the
	// ego can't activate; a reflective loop must not stand up new durable loops.
	found := false
	for _, n := range metacogExcludeTools {
		if n == "thane_loop_create" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("metacogExcludeTools must exclude thane_loop_create; got %v", metacogExcludeTools)
	}
}
