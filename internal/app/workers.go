package app

import (
	"context"
	"fmt"
)

// deferWorker enqueues a function to be executed by [StartWorkers].
// The function receives the context passed to StartWorkers and should
// return a non-nil error only when the failure is fatal to startup.
func (a *App) deferWorker(fn func(ctx context.Context) error) {
	a.pendingWorkers = append(a.pendingWorkers, fn)
}

// StartWorkers launches all background goroutines and persistent loops
// that were deferred during [New]. It must be called exactly once,
// after New returns and before [App.Serve].
//
// Workers are started in the order they were registered (matching the
// original initialization order in New). If any worker returns a fatal
// error, StartWorkers returns immediately without starting remaining
// workers — the caller should treat this as a startup failure.
func (a *App) StartWorkers(ctx context.Context) error {
	for i, fn := range a.pendingWorkers {
		if err := fn(ctx); err != nil {
			return fmt.Errorf("start worker %d: %w", i, err)
		}
	}
	a.pendingWorkers = nil // release closures
	return nil
}
