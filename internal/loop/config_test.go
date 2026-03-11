package loop

import (
	"testing"
	"time"
)

func TestApplyDefaults(t *testing.T) {
	t.Parallel()

	t.Run("zero value gets sleep defaults", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Name: "test"}
		cfg.applyDefaults()

		if cfg.SleepMin != DefaultSleepMin {
			t.Errorf("SleepMin = %v, want %v", cfg.SleepMin, DefaultSleepMin)
		}
		if cfg.SleepMax != DefaultSleepMax {
			t.Errorf("SleepMax = %v, want %v", cfg.SleepMax, DefaultSleepMax)
		}
		if cfg.SleepDefault != DefaultSleepDefault {
			t.Errorf("SleepDefault = %v, want %v", cfg.SleepDefault, DefaultSleepDefault)
		}
	})

	t.Run("nil jitter gets default", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Name: "test"}
		cfg.applyDefaults()

		if cfg.Jitter == nil || *cfg.Jitter != DefaultJitter {
			t.Errorf("Jitter = %v, want %v", cfg.Jitter, DefaultJitter)
		}
	})

	t.Run("explicit zero jitter means disabled", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Name: "test", Jitter: Float64Ptr(0)}
		cfg.applyDefaults()

		if cfg.Jitter == nil || *cfg.Jitter != 0 {
			t.Errorf("Jitter = %v, want ptr(0) (explicitly disabled)", cfg.Jitter)
		}
	})

	t.Run("explicit values preserved", func(t *testing.T) {
		t.Parallel()
		cfg := Config{
			Name:         "test",
			SleepMin:     10 * time.Second,
			SleepMax:     10 * time.Minute,
			SleepDefault: 2 * time.Minute,
			Jitter:       Float64Ptr(0.5),
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
		if cfg.Jitter == nil || *cfg.Jitter != 0.5 {
			t.Errorf("Jitter = %v, want 0.5", cfg.Jitter)
		}
	})

	t.Run("supervisor prob not auto-defaulted", func(t *testing.T) {
		t.Parallel()
		cfg := Config{Name: "test", Supervisor: true}
		cfg.applyDefaults()

		if cfg.SupervisorProb != 0 {
			t.Errorf("SupervisorProb = %v, want 0 (callers opt in explicitly)", cfg.SupervisorProb)
		}
	})
}
