package archivist

import (
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func TestParseConfig_Valid(t *testing.T) {
	jitter, supervisor := 0.2, 0.1
	raw := config.ArchivistConfig{
		Enabled:               true,
		MinSleep:              "15m",
		MaxSleep:              "12h",
		DefaultSleep:          "1h",
		Jitter:                &jitter,
		SupervisorProbability: &supervisor,
		Router:                config.RouterConfig{QualityFloor: 5},
		SupervisorRouter:      config.RouterConfig{QualityFloor: 8},
	}

	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	if cfg.MinSleep != 15*time.Minute {
		t.Errorf("MinSleep = %v, want 15m", cfg.MinSleep)
	}
	if cfg.MaxSleep != 12*time.Hour {
		t.Errorf("MaxSleep = %v, want 12h", cfg.MaxSleep)
	}
	if cfg.DefaultSleep != time.Hour {
		t.Errorf("DefaultSleep = %v, want 1h", cfg.DefaultSleep)
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
	if cfg.SupervisorProbability != 0.1 {
		t.Errorf("SupervisorProbability = %v, want 0.1", cfg.SupervisorProbability)
	}
}

func TestParseConfig_InvalidDuration(t *testing.T) {
	cases := []struct {
		name string
		raw  config.ArchivistConfig
	}{
		{"bad_min", config.ArchivistConfig{MinSleep: "junk", MaxSleep: "12h", DefaultSleep: "1h"}},
		{"bad_max", config.ArchivistConfig{MinSleep: "15m", MaxSleep: "junk", DefaultSleep: "1h"}},
		{"bad_default", config.ArchivistConfig{MinSleep: "15m", MaxSleep: "12h", DefaultSleep: "junk"}},
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
