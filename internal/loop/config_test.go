package loop

import (
	"testing"
	"time"
)

func TestApplyDefaults(t *testing.T) {
	t.Parallel()

	t.Run("zero value gets all defaults", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Name: "test"}
		cfg.applyDefaults()

		if cfg.SleepMin != defaultSleepMin {
			t.Errorf("SleepMin = %v, want %v", cfg.SleepMin, defaultSleepMin)
		}
		if cfg.SleepMax != defaultSleepMax {
			t.Errorf("SleepMax = %v, want %v", cfg.SleepMax, defaultSleepMax)
		}
		if cfg.SleepDefault != defaultSleepDefault {
			t.Errorf("SleepDefault = %v, want %v", cfg.SleepDefault, defaultSleepDefault)
		}
		if cfg.Jitter != defaultJitter {
			t.Errorf("Jitter = %v, want %v", cfg.Jitter, defaultJitter)
		}
	})

	t.Run("explicit values preserved", func(t *testing.T) {
		t.Parallel()
		cfg := Config{
			Name:         "test",
			SleepMin:     10 * time.Second,
			SleepMax:     10 * time.Minute,
			SleepDefault: 2 * time.Minute,
			Jitter:       0.5,
		}
		cfg.applyDefaults()

		if cfg.SleepMin != 10*time.Second {
			t.Errorf("SleepMin = %v, want 10s", cfg.SleepMin)
		}
		if cfg.SleepMax != 10*time.Minute {
			t.Errorf("SleepMax = %v, want 10m", cfg.SleepMax)
		}
		if cfg.SleepDefault != 2*time.Minute {
			t.Errorf("SleepDefault = %v, want 2m", cfg.SleepDefault)
		}
		if cfg.Jitter != 0.5 {
			t.Errorf("Jitter = %v, want 0.5", cfg.Jitter)
		}
	})

	t.Run("supervisor prob default when supervisor enabled", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Name: "test", Supervisor: true}
		cfg.applyDefaults()

		if cfg.SupervisorProb != defaultSuperProb {
			t.Errorf("SupervisorProb = %v, want %v", cfg.SupervisorProb, defaultSuperProb)
		}
	})

	t.Run("supervisor prob not set when supervisor disabled", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Name: "test", Supervisor: false}
		cfg.applyDefaults()

		if cfg.SupervisorProb != 0 {
			t.Errorf("SupervisorProb = %v, want 0", cfg.SupervisorProb)
		}
	})
}
