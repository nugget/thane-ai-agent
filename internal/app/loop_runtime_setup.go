package app

import (
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
	return nil
}
