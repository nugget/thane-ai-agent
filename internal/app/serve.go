package app

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/memguard"
)

// Serve starts the API server(s), registers signal handlers for graceful
// shutdown, and blocks until the server stops. It returns nil on clean
// shutdown. A non-nil error means one of two things: the server failed
// unexpectedly, or the memory guard tripped its hard limit and triggered
// an intentional restart so the supervising wrapper relaunches the
// process. The latter is a deliberate self-restart, not a failure.
//
// Cleanup of all resources opened during [New] is handled by
// [App.shutdown], which Serve defers at entry.
func (a *App) Serve(ctx context.Context) error {
	defer a.Close()

	// Periodic cleanup of expired opstate keys (issue #457). Expired
	// keys are already invisible on read; this reclaims storage.
	// Launched after signal.NotifyContext so the goroutine stops on
	// SIGINT/SIGTERM before opStore.Close() runs.
	go func() {
		const cleanupInterval = 1 * time.Hour
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 30*time.Second)
				n, err := a.opStore.DeleteExpired(cleanupCtx)
				cleanupCancel()
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					a.logger.Warn("opstate expired cleanup failed", "error", err)
				} else if n > 0 {
					a.logger.Info("opstate expired keys cleaned up", "deleted", n)
				}
			}
		}
	}()

	// Memory guard: write a heap profile and restart before a leak can OOM the
	// host. Opt-in. A hard-limit trip makes Serve return an error below so the
	// process exits non-zero — a memory-limit restart is a failure even though
	// the shutdown is clean — and the supervising wrapper relaunches thane.
	var guard *memguard.Guard
	if a.cfg.MemoryGuard.Enabled {
		profileDir := a.cfg.MemoryGuard.ProfileDir
		if profileDir == "" {
			profileDir = filepath.Join(a.cfg.DataDir, "profiles")
		}
		guard = memguard.New(memguard.Config{
			SoftLimitMB: a.cfg.MemoryGuard.SoftLimitMB,
			HardLimitMB: a.cfg.MemoryGuard.HardLimitMB,
			ProfileDir:  profileDir,
			Interval:    time.Duration(a.cfg.MemoryGuard.IntervalSeconds) * time.Second,
		}, a.logger)
		// Stored on the App so observability surfaces (system_health) can
		// read live memory against the limits; unset when the guard is off.
		a.memGuard.Store(guard)
		go guard.Start(ctx)
	}

	// Start optional servers before the shutdown goroutine so they are
	// available to drain when shutdown fires.
	if a.ollamaServer != nil {
		go func() {
			if err := a.ollamaServer.Start(ctx); err != nil {
				a.logger.Error("ollama API server failed", "error", err)
			}
		}()
	}

	if a.openaiServer != nil {
		go func() {
			if err := a.openaiServer.Start(ctx); err != nil {
				a.logger.Error("openai API server failed", "error", err)
			}
		}()
	}

	if a.carddavServer != nil {
		if err := a.carddavServer.Start(ctx); err != nil {
			a.logger.Error("carddav server failed to start", "error", err)
		}
	}

	if a.edgeServer != nil {
		go func() {
			if err := a.edgeServer.Start(ctx); err != nil {
				a.logger.Error("https front door failed", "error", err)
			}
		}()
	}

	// Serve synchronizes with these tasks before returning: everything
	// after the server drain below — archiving conversations, ending
	// sessions, the shutdown checkpoint — reads the stores that the
	// deferred a.shutdown() closes the moment Serve returns. Without
	// the synchronization, those tail steps race the close and lose
	// ("sql: database is closed" at every restart), which meant the
	// shutdown checkpoint silently never happened. The abort path
	// covers the fatal-server-error return, where the goroutine would
	// otherwise stay parked on ctx.Done and fire the whole tail into
	// closed stores when the command caller's deferred cancel finally
	// runs.
	tasks := newShutdownTasks(a.logger, shutdownTasksTimeout)
	tasks.watch(ctx, func() {
		a.logger.Info("shutdown signal received")

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		if err := a.server.Shutdown(shutdownCtx); err != nil {
			a.logger.Error("server shutdown failed", "error", err)
		}
		if a.ollamaServer != nil {
			if err := a.ollamaServer.Shutdown(shutdownCtx); err != nil {
				a.logger.Error("ollama server shutdown failed", "error", err)
			}
		}
		if a.openaiServer != nil {
			if err := a.openaiServer.Shutdown(shutdownCtx); err != nil {
				a.logger.Error("openai server shutdown failed", "error", err)
			}
		}
		if a.carddavServer != nil {
			if err := a.carddavServer.Shutdown(shutdownCtx); err != nil {
				a.logger.Error("carddav server shutdown failed", "error", err)
			}
		}
		if a.edgeServer != nil {
			if err := a.edgeServer.Shutdown(shutdownCtx); err != nil {
				a.logger.Error("https front door shutdown failed", "error", err)
			}
		}
		if shutdownCtx.Err() == context.DeadlineExceeded {
			a.logger.Warn("server shutdown timed out; some connections may have been forcefully terminated")
		}

		// Archive any still-active conversations now that the servers have
		// stopped accepting work and in-flight requests have drained.
		if a.archiveAdapter != nil && a.loop != nil {
			activeConversationIDs := a.archiveAdapter.ActiveConversationIDs()
			if len(activeConversationIDs) > 0 {
				a.logger.Info("archiving active conversations before shutdown", "count", len(activeConversationIDs))
				for _, conversationID := range activeConversationIDs {
					a.loop.ShutdownArchive(conversationID)
				}
			}
		}

		// Publish MQTT offline status before disconnecting.
		if a.mqttPub != nil {
			offlineCtx, offlineCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer offlineCancel()
			if err := a.mqttPub.Stop(offlineCtx); err != nil {
				a.logger.Error("mqtt shutdown failed", "error", err)
			}
		}

		if _, err := a.checkpointer.CreateShutdown(); err != nil {
			a.logger.Error("failed to create shutdown checkpoint", "error", err)
		}
	})

	// Start the primary API server. This blocks until the server is shut
	// down (via context cancellation or fatal error).
	if err := a.server.Start(ctx); err != nil {
		if ctx.Err() == nil {
			tasks.finish(true)
			return fmt.Errorf("server failed: %w", err)
		}
	}

	// finish(fatal) when ctx is somehow still alive: Start returning nil
	// without a cancelled ctx should not happen, but if it does, the
	// watcher must be released without running tail work — Serve is
	// about to close the stores either way.
	tasks.finish(ctx.Err() == nil)

	a.logger.Info("Thane stopped")
	if guard != nil && guard.Tripped() {
		return fmt.Errorf("memory guard reached its hard limit and triggered a restart")
	}
	return nil
}

// shutdown releases all resources opened during [New] in the correct
// order (reverse of initialization). It is called via defer at the start
// of [Serve] so it runs regardless of how Serve exits.
//
// Shutdown proceeds in two phases:
//
//  1. Cross-cutting stops: loopRegistry and connMgr are stopped
//     explicitly so that all background goroutines drain before
//     any resources they depend on are released.
//  2. LIFO closer stack: closers registered by [New] (resources) and
//     [StartWorkers] (workers) are drained in reverse order. Workers
//     registered later stop before resources registered earlier close.
func (a *App) shutdown() {
	// Phase 1: cross-cutting stops. These must run before any
	// resource closers because loops and watchers use those resources.
	if a.loopRegistry != nil {
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
		a.loopRegistry.ShutdownAll(shutCtx)
		shutCancel()
	}
	if a.connMgr != nil {
		a.connMgr.Stop()
	}

	// Phase 2: drain the closer stack in LIFO order.
	for i := len(a.closers) - 1; i >= 0; i-- {
		c := a.closers[i]
		a.logger.Debug("closing", "name", c.name)
		c.fn()
	}
	a.closers = nil // release references
}
