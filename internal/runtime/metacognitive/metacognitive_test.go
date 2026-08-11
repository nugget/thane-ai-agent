package metacognitive

import (
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

// --- Hydration tests ---
//
// The definition itself lives in loops/metacognitive.md; its shape is
// covered by the boot-path assertions in
// internal/app/coreloop_docs_parity_test.go. What remains here is the
// hydration seam a document cannot express.

func TestHydrateSpecDefaultsTheName(t *testing.T) {
	cfg := testConfig()
	spec := HydrateSpec(loop.Spec{}, cfg)
	if spec.Name != DefinitionName {
		t.Errorf("Name = %q, want %q defaulted", spec.Name, DefinitionName)
	}
	named := HydrateSpec(loop.Spec{Name: "custom"}, cfg)
	if named.Name != "custom" {
		t.Errorf("Name = %q, want the declared name kept", named.Name)
	}
}
