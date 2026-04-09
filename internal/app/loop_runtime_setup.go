package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/usage"
)

func (a *App) newLoopRegistry(logger *slog.Logger) *looppkg.Registry {
	registryOpts := []looppkg.RegistryOption{looppkg.WithRegistryLogger(logger)}
	if a != nil && a.cfg != nil && a.cfg.Loops.MaxRunning > 0 {
		registryOpts = append(registryOpts, looppkg.WithMaxLoops(a.cfg.Loops.MaxRunning))
	}
	return looppkg.NewRegistry(registryOpts...)
}

func (a *App) initLoopUsageStores(db *sql.DB) error {
	usageStore, err := usage.NewStore(db)
	if err != nil {
		return fmt.Errorf("initialize usage store: %w", err)
	}
	a.usageStore = usageStore

	loopObservationStore, err := newLoopObservationStore(db)
	if err != nil {
		return fmt.Errorf("initialize loop observation store: %w", err)
	}
	a.loopObservationStore = loopObservationStore
	return nil
}

func (a *App) loopTaskOutputSink() looppkg.OutputSink {
	return func(ctx context.Context, delivery looppkg.OutputDelivery) error {
		dispatcher := a.ensureLoopOutputDispatcher()
		if dispatcher == nil {
			return nil
		}
		return dispatcher.Deliver(ctx, delivery)
	}
}
