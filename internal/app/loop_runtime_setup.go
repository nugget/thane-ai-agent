package app

import (
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func (a *App) newLoopRegistry(logger *slog.Logger) *looppkg.Registry {
	registryOpts := []looppkg.RegistryOption{looppkg.WithRegistryLogger(logger)}
	if a != nil && a.cfg != nil && a.cfg.Loops.MaxRunning > 0 {
		registryOpts = append(registryOpts, looppkg.WithMaxLoops(a.cfg.Loops.MaxRunning))
	}
	if a != nil {
		registryOpts = append(registryOpts, looppkg.WithRegistryChangeHook(a.publishLoopTopologyChange))
	}
	return looppkg.NewRegistry(registryOpts...)
}

// publishLoopTopologyChange emits a loop-topology event so live consumers
// (the web console graph) re-sync when the graph's membership changes —
// covering adds, removals, and reparents of any loop, including inert
// container nodes that never emit a goroutine lifecycle event. a.eventBus is
// read lazily (at loop register/deregister time, well after startup) so the
// hook works regardless of construction order.
func (a *App) publishLoopTopologyChange(loopID string) {
	if a == nil || a.eventBus == nil {
		return
	}
	a.eventBus.Publish(events.Event{
		Timestamp: time.Now(),
		Source:    events.SourceLoop,
		Kind:      events.KindLoopTopology,
		Data:      map[string]any{"loop_id": loopID},
	})
}

func (a *App) initLoopUsageStores(db *sql.DB, logger *slog.Logger) error {
	usageStore, err := usage.NewStore(db, logger)
	if err != nil {
		return fmt.Errorf("initialize usage store: %w", err)
	}
	a.usageStore = usageStore
	return nil
}
