package memguard

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCheckThresholds drives check() across the soft and hard boundaries and
// asserts the two invariants that matter: the heap profile is written exactly
// once (on the first soft crossing), and the hard action fires exactly once.
func TestCheckThresholds(t *testing.T) {
	var dumps []string
	var hardCalls int
	g := New(Config{SoftLimitMB: 1000, HardLimitMB: 2000, ProfileDir: t.TempDir(), Interval: time.Hour}, nil)
	g.dumpHeap = func(p string) error { dumps = append(dumps, p); return nil }
	g.onHard = func() { hardCalls++ }
	g.now = func() time.Time { return time.Unix(0, 0).UTC() }

	g.check(500 * mib) // below soft — no action
	if len(dumps) != 0 || hardCalls != 0 {
		t.Fatalf("acted below soft: dumps=%d hard=%d", len(dumps), hardCalls)
	}

	g.check(1200 * mib) // first soft crossing — one dump, no hard
	if len(dumps) != 1 {
		t.Fatalf("soft crossing dumps=%d, want 1", len(dumps))
	}
	if hardCalls != 0 {
		t.Fatalf("hard fired at soft crossing")
	}

	g.check(1500 * mib) // still above soft — no second dump
	if len(dumps) != 1 {
		t.Fatalf("dumped again above soft: dumps=%d, want 1", len(dumps))
	}

	g.check(2100 * mib) // hard crossing — fire once
	if hardCalls != 1 {
		t.Fatalf("hard hardCalls=%d, want 1", hardCalls)
	}

	g.check(3000 * mib) // already fired — no further action
	if hardCalls != 1 {
		t.Fatalf("hard fired again after firing: %d", hardCalls)
	}
}

// TestNewDefaults verifies unset/invalid limits are repaired and hard > soft.
func TestNewDefaults(t *testing.T) {
	g := New(Config{}, nil)
	if g.soft == 0 || g.hard <= g.soft {
		t.Fatalf("bad defaults: soft=%d hard=%d", g.soft, g.hard)
	}
	// A hard <= soft is nonsensical; New must lift hard above soft.
	g2 := New(Config{SoftLimitMB: 500, HardLimitMB: 400}, nil)
	if g2.hard <= g2.soft {
		t.Fatalf("hard not lifted above soft: soft=%d hard=%d", g2.soft, g2.hard)
	}
}

// TestWriteHeapProfile confirms a real, non-empty heap profile lands at a
// nested path (directory auto-created).
func TestWriteHeapProfile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "profiles", "heap.pprof")
	if err := writeHeapProfile(p); err != nil {
		t.Fatalf("writeHeapProfile: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatalf("heap profile is empty")
	}
}
