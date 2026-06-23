// Package memguard is an in-process memory guard. It watches the process's
// own memory use and, before a leak can OOM the host, (1) writes a heap
// profile so the leak can be diagnosed and (2) triggers a graceful shutdown so
// the supervising wrapper restarts thane clean.
//
// It exists because a leak in a single build (636aac6a) grew to ~6 GB and took
// down the whole macOS host in 2026-06. The guard turns that
// host-killing OOM into a benign restart with a heap profile on disk — and is
// a permanent safety valve regardless of any single leak.
//
// Restart policy deliberately lives in the supervising wrapper, not here: the
// guard only triggers the process's normal SIGTERM graceful-shutdown path and
// exits; the wrapper relaunches thane.
package memguard

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync/atomic"
	"syscall"
	"time"
)

const mib = 1 << 20

// Config controls the guard. Zero-valued numeric fields are filled with
// defaults by New.
type Config struct {
	SoftLimitMB int           // write a heap profile when memory crosses this
	HardLimitMB int           // graceful-restart when memory crosses this
	ProfileDir  string        // directory heap profiles are written to
	Interval    time.Duration // poll cadence
}

// Guard polls process memory on an interval and acts at two thresholds: a soft
// limit that writes a heap profile (once per process lifetime) and a hard
// limit that triggers a graceful restart.
type Guard struct {
	soft, hard uint64 // bytes
	interval   time.Duration
	profileDir string
	logger     *slog.Logger

	// Seams, swappable in tests so the guard can be exercised without real
	// memory pressure, signals, or profiling.
	readMem  func() uint64
	dumpHeap func(path string) error
	onHard   func()
	now      func() time.Time

	heapDumped bool
	fired      bool
	// tripped mirrors a hard-limit firing for cross-goroutine reads: the
	// poller sets it; Serve reads it after shutdown to pick the exit code.
	tripped atomic.Bool
}

// New builds a Guard, applying defaults for any unset numeric fields. Soft
// 1024 MB / hard 2048 MB / interval 15s are conservative for a process whose
// healthy footprint is a few hundred MB; tune via config.
func New(cfg Config, logger *slog.Logger) *Guard {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.SoftLimitMB <= 0 {
		cfg.SoftLimitMB = 1024
	}
	if cfg.HardLimitMB <= cfg.SoftLimitMB {
		cfg.HardLimitMB = cfg.SoftLimitMB * 2
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 15 * time.Second
	}
	if cfg.ProfileDir == "" {
		cfg.ProfileDir = os.TempDir()
	}
	g := &Guard{
		soft:       uint64(cfg.SoftLimitMB) * mib,
		hard:       uint64(cfg.HardLimitMB) * mib,
		interval:   cfg.Interval,
		profileDir: cfg.ProfileDir,
		logger:     logger,
		dumpHeap:   writeHeapProfile,
		now:        time.Now,
	}
	g.readMem = func() uint64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		// Sys is total memory obtained from the OS — the closest cheap proxy
		// for RSS, and it captures both heap-object and goroutine-stack growth.
		return m.Sys
	}
	g.onHard = func() {
		// Trigger the process's existing graceful-shutdown path: main.go's
		// signal.Notify handler cancels the root context, which stops all
		// goroutines (this guard included) and runs shutdown; the wrapper
		// then restarts thane. A failed signal is logged, not swallowed.
		if p, err := os.FindProcess(os.Getpid()); err == nil {
			if err := p.Signal(syscall.SIGTERM); err != nil {
				g.logger.Error("memory guard: failed to signal graceful shutdown", "error", err)
			}
		}
	}
	return g
}

// Tripped reports whether the guard reached its hard limit and initiated a
// restart. It is read after graceful shutdown to choose the process exit code:
// a memory-limit restart is a failure condition even though the shutdown itself
// is clean, so the supervising wrapper must see a non-zero exit and relaunch.
func (g *Guard) Tripped() bool { return g.tripped.Load() }

// Start runs the guard until ctx is cancelled. Run it in a goroutine.
func (g *Guard) Start(ctx context.Context) {
	g.logger.Info("memory guard active",
		"soft_mb", g.soft/mib, "hard_mb", g.hard/mib,
		"interval", g.interval, "profile_dir", g.profileDir)
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			g.check(g.readMem())
		}
	}
}

// check applies the threshold logic to one memory reading. It is separated
// from the ticker loop so the behavior is unit-testable without real memory
// pressure. The heap profile is written at most once per process lifetime (the
// process restarts each leak cycle), and the hard action fires at most once.
func (g *Guard) check(mem uint64) {
	if g.fired {
		return
	}
	if !g.heapDumped && mem >= g.soft {
		g.heapDumped = true
		path := filepath.Join(g.profileDir,
			fmt.Sprintf("heap-%s-pid%d-%dMB.pprof",
				g.now().UTC().Format("20060102T150405Z"), os.Getpid(), mem/mib))
		if err := g.dumpHeap(path); err != nil {
			g.logger.Warn("memory guard: heap profile failed", "error", err, "mem_mb", mem/mib)
		} else {
			g.logger.Warn("memory guard: soft limit crossed; wrote heap profile",
				"mem_mb", mem/mib, "soft_mb", g.soft/mib, "path", path)
		}
	}
	if mem >= g.hard {
		g.fired = true
		g.tripped.Store(true)
		g.logger.Error("memory guard: hard limit reached; triggering graceful restart",
			"mem_mb", mem/mib, "hard_mb", g.hard/mib)
		g.onHard()
	}
}

// writeHeapProfile GCs (for up-to-date stats, per the pprof docs) and writes a
// heap profile to path with explicit 0o644 perms, creating the directory if
// needed. The Close error is surfaced — on some filesystems it is the only
// sign of a failed flush — unless a profile-write error already takes
// precedence.
func writeHeapProfile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	runtime.GC()
	if err := pprof.WriteHeapProfile(f); err != nil {
		f.Close() // best-effort; don't mask the write error
		return err
	}
	return f.Close()
}
