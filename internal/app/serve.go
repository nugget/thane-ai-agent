package app

import (
	"context"
	"fmt"
	"time"
)

// Serve starts the API server(s), registers signal handlers for graceful
// shutdown, and blocks until the server stops. It returns nil on clean
// shutdown and a non-nil error only when the server fails unexpectedly.
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

	// Start optional servers before the shutdown goroutine so they are
	// available to drain when shutdown fires.
	if a.ollamaServer != nil {
		go func() {
			if err := a.ollamaServer.Start(ctx); err != nil {
				a.logger.Error("ollama API server failed", "error", err)
			}
		}()
	}

	if a.carddavServer != nil {
		if err := a.carddavServer.Start(ctx); err != nil {
			a.logger.Error("carddav server failed to start", "error", err)
		}
	}

	go func() {
		<-ctx.Done()
		a.logger.Info("shutdown signal received")

		// Archive conversation before shutdown
		a.loop.ShutdownArchive("default")

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
		if a.carddavServer != nil {
			if err := a.carddavServer.Shutdown(shutdownCtx); err != nil {
				a.logger.Error("carddav server shutdown failed", "error", err)
			}
		}
		if shutdownCtx.Err() == context.DeadlineExceeded {
			a.logger.Warn("server shutdown timed out; some connections may have been forcefully terminated")
		}
	}()

	// Start the primary API server. This blocks until the server is shut
	// down (via context cancellation or fatal error).
	if err := a.server.Start(ctx); err != nil {
		if ctx.Err() == nil {
			return fmt.Errorf("server failed: %w", err)
		}
	}

	a.logger.Info("Thane stopped")
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
