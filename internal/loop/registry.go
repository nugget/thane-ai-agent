package loop

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
)

// Registry tracks all active loops and provides visibility into what is
// running. It enforces concurrency limits and coordinates graceful
// shutdown.
type Registry struct {
	mu       sync.RWMutex
	loops    map[string]*Loop
	maxLoops int
	logger   *slog.Logger
}

// RegistryOption configures a Registry.
type RegistryOption func(*Registry)

// WithMaxLoops sets the maximum number of concurrent loops the registry
// will allow. Zero means unlimited.
func WithMaxLoops(n int) RegistryOption {
	return func(r *Registry) {
		r.maxLoops = n
	}
}

// WithRegistryLogger sets the logger for registry operations. Nil is
// ignored (keeps slog.Default()).
func WithRegistryLogger(l *slog.Logger) RegistryOption {
	return func(r *Registry) {
		if l != nil {
			r.logger = l
		}
	}
}

// NewRegistry creates a new loop registry.
func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{
		loops:  make(map[string]*Loop),
		logger: slog.Default(),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Register adds a loop to the registry. Returns an error if the loop's
// ID is already registered or the concurrency limit would be exceeded.
// The loop is not started — call [Loop.Start] after registering.
func (r *Registry) Register(l *Loop) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.loops[l.id]; exists {
		return fmt.Errorf("loop ID %q already registered", l.id)
	}
	if r.maxLoops > 0 && len(r.loops) >= r.maxLoops {
		return fmt.Errorf("concurrency limit reached (%d loops)", r.maxLoops)
	}

	r.loops[l.id] = l
	r.logger.Info("loop registered",
		"loop_id", l.id,
		"loop_name", l.config.Name,
		"active_loops", len(r.loops),
	)
	return nil
}

// Deregister removes a loop from the registry. Safe to call for a loop
// that is not registered (no-op).
func (r *Registry) Deregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.loops[id]; !exists {
		return
	}
	delete(r.loops, id)
	r.logger.Info("loop deregistered",
		"loop_id", id,
		"active_loops", len(r.loops),
	)
}

// Get returns the loop with the given ID, or nil if not found.
func (r *Registry) Get(id string) *Loop {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.loops[id]
}

// GetByName returns the first loop with the given name, or nil if not
// found. If multiple loops share a name, the result is undefined.
func (r *Registry) GetByName(name string) *Loop {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, l := range r.loops {
		if l.config.Name == name {
			return l
		}
	}
	return nil
}

// List returns a snapshot of all registered loops sorted by name.
func (r *Registry) List() []*Loop {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Loop, 0, len(r.loops))
	for _, l := range r.loops {
		result = append(result, l)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].config.Name < result[j].config.Name
	})
	return result
}

// Statuses returns a snapshot of all registered loop statuses sorted by
// name.
func (r *Registry) Statuses() []Status {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Status, 0, len(r.loops))
	for _, l := range r.loops {
		result = append(result, l.Status())
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// ActiveCount returns the number of registered loops.
func (r *Registry) ActiveCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.loops)
}

// ShutdownAll cancels all registered loops and waits for them to drain.
// The provided context controls the maximum time to wait; if it expires,
// remaining loops are abandoned. Returns the number of loops that were
// stopped.
func (r *Registry) ShutdownAll(ctx context.Context) int {
	r.mu.RLock()
	loops := make([]*Loop, 0, len(r.loops))
	for _, l := range r.loops {
		loops = append(loops, l)
	}
	r.mu.RUnlock()

	r.logger.Info("shutting down all loops", "count", len(loops))

	// Fire all cancellations in parallel (non-blocking).
	for _, l := range loops {
		l.cancel0()
	}

	// Wait for each loop to finish, respecting the context deadline.
	// Loops that were never started (Done()==nil) are treated as
	// already drained.
	stopped := 0
	for _, l := range loops {
		done := l.Done()
		if done == nil {
			// Never started — just deregister.
			stopped++
			r.Deregister(l.id)
			continue
		}
		select {
		case <-done:
			stopped++
			r.Deregister(l.id)
		case <-ctx.Done():
			r.logger.Warn("shutdown context expired, abandoning remaining loops",
				"stopped", stopped,
				"remaining", len(loops)-stopped,
			)
			return stopped
		}
	}

	r.logger.Info("all loops shut down", "stopped", stopped)
	return stopped
}

// SpawnLoop creates a new loop with the given config, registers it, and
// starts it. This is the primary entry point for creating loops. Returns
// the loop ID on success.
func (r *Registry) SpawnLoop(ctx context.Context, cfg Config, deps Deps) (string, error) {
	l, err := New(cfg, deps)
	if err != nil {
		return "", fmt.Errorf("create loop %q: %w", cfg.Name, err)
	}

	// Call Setup before Register/Start so the caller can register
	// tools or perform other initialization that needs *Loop.
	if cfg.Setup != nil {
		cfg.Setup(l)
	}

	if err := r.Register(l); err != nil {
		return "", err
	}

	if err := l.Start(ctx); err != nil {
		r.Deregister(l.id)
		return "", fmt.Errorf("start loop %q: %w", cfg.Name, err)
	}

	// Automatically deregister the loop when its goroutine exits so
	// that naturally completed loops (MaxIter, MaxDuration, context
	// cancellation) do not consume registry capacity.
	go func(id string, done <-chan struct{}) {
		<-done
		r.Deregister(id)
	}(l.id, l.Done())

	return l.id, nil
}

// StopLoop stops a loop by ID and deregisters it once the goroutine
// has exited. Returns an error if the loop is not found. If the
// goroutine does not exit within 10 seconds (the Stop timeout), the
// loop remains registered to avoid orphaning a running goroutine.
func (r *Registry) StopLoop(id string) error {
	l := r.Get(id)
	if l == nil {
		return fmt.Errorf("loop %q not found", id)
	}

	l.Stop()

	// Only deregister if the goroutine actually exited. Stop() waits
	// up to 10s internally; if Done is closed, it exited cleanly.
	done := l.Done()
	if done == nil {
		// Never started — safe to deregister.
		r.Deregister(id)
		return nil
	}

	select {
	case <-done:
		r.Deregister(id)
	default:
		r.logger.Warn("loop goroutine still running after Stop, keeping registered",
			"loop_id", id,
		)
	}

	return nil
}
