package app

import (
	"context"
	"fmt"
)

// pendingWorker pairs a descriptive name with its start function so
// errors from [StartWorkers] identify the failing subsystem.
type pendingWorker struct {
	name string
	fn   func(ctx context.Context) error
}

// deferWorker enqueues a named function to be executed by [StartWorkers].
// The function receives the context passed to StartWorkers and should
// return a non-nil error only when the failure is fatal to startup.
func (a *App) deferWorker(name string, fn func(ctx context.Context) error) {
	a.pendingWorkers = append(a.pendingWorkers, pendingWorker{name: name, fn: fn})
}

// StartWorkers launches all background goroutines and persistent loops
// that were deferred during [New]. It is intended to be called once,
// after New returns and before [App.Serve], as part of application startup.
//
// Workers are started in the order they were registered (matching the
// original initialization order in New). If any worker returns a fatal
// error, StartWorkers returns immediately without starting remaining
// workers — the caller should treat this as a startup failure. Subsequent
// calls after all pending workers have been started do not start any
// additional workers and return nil.
func (a *App) StartWorkers(ctx context.Context) error {
	for _, w := range a.pendingWorkers {
		if err := w.fn(ctx); err != nil {
			return fmt.Errorf("start worker %q: %w", w.name, err)
		}
	}
	a.pendingWorkers = nil // release closures
	return nil
}

// Close releases all resources opened during [New]. It is safe to call
// multiple times and safe to call even if [StartWorkers] or [Serve]
// were never called — only non-nil resources are cleaned up. Callers
// should defer Close immediately after a successful New to guarantee
// cleanup on all exit paths.
func (a *App) Close() {
	a.closeOnce.Do(a.shutdown)
}
