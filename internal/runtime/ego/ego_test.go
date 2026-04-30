package ego

import (
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func TestParseConfig_Valid(t *testing.T) {
	raw := config.EgoConfig{
		Enabled:               true,
		MinSleep:              "30m",
		MaxSleep:              "24h",
		DefaultSleep:          "6h",
		Jitter:                0.2,
		SupervisorProbability: 0.2,
		Router:                config.EgoRouterConfig{QualityFloor: 5},
		SupervisorRouter:      config.EgoRouterConfig{QualityFloor: 8},
	}

	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	if cfg.MinSleep != 30*time.Minute {
		t.Errorf("MinSleep = %v, want 30m", cfg.MinSleep)
	}
	if cfg.MaxSleep != 24*time.Hour {
		t.Errorf("MaxSleep = %v, want 24h", cfg.MaxSleep)
	}
	if cfg.DefaultSleep != 6*time.Hour {
		t.Errorf("DefaultSleep = %v, want 6h", cfg.DefaultSleep)
	}
	if cfg.StateFile != stateFileName {
		t.Errorf("StateFile = %q, want %q", cfg.StateFile, stateFileName)
	}
	if cfg.QualityFloor != 5 {
		t.Errorf("QualityFloor = %d, want 5", cfg.QualityFloor)
	}
	if cfg.SupervisorQualityFloor != 8 {
		t.Errorf("SupervisorQualityFloor = %d, want 8", cfg.SupervisorQualityFloor)
	}
	if cfg.SupervisorProbability != 0.2 {
		t.Errorf("SupervisorProbability = %v, want 0.2", cfg.SupervisorProbability)
	}
}

func TestParseConfig_InvalidDuration(t *testing.T) {
	cases := []struct {
		name string
		raw  config.EgoConfig
	}{
		{"bad_min", config.EgoConfig{MinSleep: "junk", MaxSleep: "24h", DefaultSleep: "6h"}},
		{"bad_max", config.EgoConfig{MinSleep: "30m", MaxSleep: "junk", DefaultSleep: "6h"}},
		{"bad_default", config.EgoConfig{MinSleep: "30m", MaxSleep: "24h", DefaultSleep: "junk"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseConfig(tc.raw); err == nil {
				t.Fatal("ParseConfig: want error, got nil")
			}
		})
	}
}

func TestDefinitionSpec_Outputs(t *testing.T) {
	cfg := Config{
		Enabled:                true,
		MinSleep:               30 * time.Minute,
		MaxSleep:               24 * time.Hour,
		DefaultSleep:           6 * time.Hour,
		Jitter:                 0.2,
		SupervisorProbability:  0.2,
		QualityFloor:           5,
		SupervisorQualityFloor: 8,
	}
	spec := DefinitionSpec(cfg)

	if spec.Name != DefinitionName {
		t.Errorf("Name = %q, want %q", spec.Name, DefinitionName)
	}
	if spec.Operation != loop.OperationService {
		t.Errorf("Operation = %q, want %q", spec.Operation, loop.OperationService)
	}
	if len(spec.Outputs) != 1 {
		t.Fatalf("Outputs len = %d, want 1", len(spec.Outputs))
	}
	out := spec.Outputs[0]
	if out.Ref != "core:ego.md" {
		t.Errorf("Outputs[0].Ref = %q, want core:ego.md", out.Ref)
	}
	if out.Type != loop.OutputTypeMaintainedDocument {
		t.Errorf("Outputs[0].Type = %q, want maintained_document", out.Type)
	}
	if out.EffectiveMode() != loop.OutputModeReplace {
		t.Errorf("Outputs[0].Mode = %q, want replace", out.EffectiveMode())
	}
	if !strings.HasPrefix(out.ToolName(), "replace_output_") {
		t.Errorf("ToolName = %q, want replace_output_* prefix", out.ToolName())
	}
	if !spec.Supervisor {
		t.Error("Supervisor should be enabled when SupervisorProbability > 0")
	}
	if spec.SleepMin != 30*time.Minute {
		t.Errorf("SleepMin = %v, want 30m", spec.SleepMin)
	}
	if spec.Profile.Mission != "ego" {
		t.Errorf("Profile.Mission = %q, want ego", spec.Profile.Mission)
	}
	if spec.Profile.DelegationGating != "disabled" {
		t.Errorf("Profile.DelegationGating = %q, want disabled", spec.Profile.DelegationGating)
	}
	if len(spec.Profile.InitialTags) != 1 || spec.Profile.InitialTags[0] != "ego" {
		t.Errorf("Profile.InitialTags = %v, want [ego]", spec.Profile.InitialTags)
	}
}

func TestHydrateSpec_AttachesTaskBuilder(t *testing.T) {
	cfg := Config{Enabled: true}
	spec := HydrateSpec(DefinitionSpec(cfg), cfg)

	if spec.TaskBuilder == nil {
		t.Fatal("TaskBuilder should be attached after HydrateSpec")
	}
	prompt, err := spec.TaskBuilder(nil, false)
	if err != nil {
		t.Fatalf("TaskBuilder returned error: %v", err)
	}
	if prompt == "" {
		t.Error("TaskBuilder returned empty prompt")
	}
	supervisorPrompt, _ := spec.TaskBuilder(nil, true)
	if len(supervisorPrompt) <= len(prompt) {
		t.Error("supervisor prompt should be longer than normal prompt")
	}
}
