package ego

import (
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func TestParseConfig_Valid(t *testing.T) {
	jitter, supervisor := 0.2, 0.2
	raw := config.EgoConfig{
		Enabled:               true,
		MinSleep:              "30m",
		MaxSleep:              "24h",
		DefaultSleep:          "6h",
		Jitter:                &jitter,
		SupervisorProbability: &supervisor,
		Router:                config.RouterConfig{QualityFloor: 5},
		SupervisorRouter:      config.RouterConfig{QualityFloor: 8},
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

// --- Hydration tests ---
//
// The definition itself lives in the shipped loops/ document; its shape
// is covered by the boot-path assertions in
// internal/app/coreloop_docs_parity_test.go. What remains here is the
// hydration seam a document cannot express.

func TestHydrateSpecDefaultsTheName(t *testing.T) {
	spec := HydrateSpec(loop.Spec{}, Config{})
	if spec.Name != DefinitionName {
		t.Errorf("Name = %q, want %q defaulted", spec.Name, DefinitionName)
	}
	named := HydrateSpec(loop.Spec{Name: "custom"}, Config{})
	if named.Name != "custom" {
		t.Errorf("Name = %q, want the declared name kept", named.Name)
	}
}
