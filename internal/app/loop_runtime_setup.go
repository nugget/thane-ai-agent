package app

import (
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func (a *App) newLoopRegistry(logger *slog.Logger) *looppkg.Registry {
	registryOpts := []looppkg.RegistryOption{looppkg.WithRegistryLogger(logger)}
	if a != nil && a.cfg != nil && a.cfg.Loops.MaxRunning > 0 {
		registryOpts = append(registryOpts, looppkg.WithMaxLoops(a.cfg.Loops.MaxRunning))
	}
	return looppkg.NewRegistry(registryOpts...)
}

func (a *App) initLoopUsageStores(db *sql.DB, logger *slog.Logger) error {
	usageStore, err := usage.NewStore(db, logger)
	if err != nil {
		return fmt.Errorf("initialize usage store: %w", err)
	}
	a.usageStore = usageStore
	return nil
}
