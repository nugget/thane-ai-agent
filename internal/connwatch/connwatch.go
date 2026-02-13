// Package connwatch provides service-level health monitoring with
// exponential backoff for external dependencies (Home Assistant, Ollama, etc).
//
// This is distinct from httpkit's transport-level retry, which handles
// sub-second transient dial errors (macOS ARP races, issue #53). connwatch
// handles multi-second to multi-minute outages: service restarts, network
// partitions, and macOS Local Network permission delays (issue #96).
//
// Each Watcher probes a single service in two phases:
//  1. Startup: exponential backoff (2s, 4s, 8s, ... capped at 60s)
//  2. Background: periodic polling (every 60s) with state-transition callbacks
package connwatch

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// ProbeFunc checks whether a service is reachable. Return nil if healthy.
type ProbeFunc func(ctx context.Context) error

// BackoffConfig controls the exponential backoff behavior.
type BackoffConfig struct {
	// InitialDelay is the delay before the first retry (default: 2s).
	InitialDelay time.Duration

	// MaxDelay is the ceiling for backoff growth (default: 60s).
	MaxDelay time.Duration

	// Multiplier scales the delay after each retry (default: 2.0).
	Multiplier float64

	// MaxRetries is the maximum number of startup probe attempts (default: 10).
	MaxRetries int

	// PollInterval is the background check interval after startup
	// retries are exhausted or after a successful connection (default: 60s).
	PollInterval time.Duration

	// ProbeTimeout limits how long each individual probe call may take (default: 10s).
	ProbeTimeout time.Duration
}

// DefaultBackoffConfig returns the backoff schedule from issue #96:
// 2s, 4s, 8s, 16s, 32s, 60s (capped), with 10 startup retries and
// 60-second background polling.
func DefaultBackoffConfig() BackoffConfig {
	return BackoffConfig{
		InitialDelay: 2 * time.Second,
		MaxDelay:     60 * time.Second,
		Multiplier:   2.0,
		MaxRetries:   10,
		PollInterval: 60 * time.Second,
		ProbeTimeout: 10 * time.Second,
	}
}

// WatcherConfig configures a single service watcher.
type WatcherConfig struct {
	// Name is a human-readable identifier for logging (e.g., "homeassistant").
	Name string

	// Probe checks service health. Must be safe for concurrent use.
	Probe ProbeFunc

	// Backoff controls retry timing. Use DefaultBackoffConfig() as a starting point.
	Backoff BackoffConfig

	// OnReady is called when the service transitions from not-ready to ready.
	// Called in a separate goroutine; must not block indefinitely. Optional.
	OnReady func()

	// OnDown is called when the service transitions from ready to not-ready.
	// Called in a separate goroutine; must not block indefinitely. Optional.
	OnDown func(err error)

	// Logger for structured logging. Uses slog.Default() if nil.
	Logger *slog.Logger
}

// ServiceStatus is the health status of a watched service, suitable for
// JSON serialization in health endpoints.
type ServiceStatus struct {
	Name      string    `json:"name"`
	Ready     bool      `json:"ready"`
	LastCheck time.Time `json:"last_check"`
	LastError string    `json:"last_error,omitempty"`
}

// Watcher monitors a single service's health.
type Watcher struct {
	config WatcherConfig
	ready  atomic.Bool
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	lastErr   error
	lastCheck time.Time
}

// IsReady reports whether the watched service is currently reachable.
func (w *Watcher) IsReady() bool {
	return w.ready.Load()
}

// LastError returns the most recent probe error, or nil if healthy.
func (w *Watcher) LastError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastErr
}

// Status returns the current health status.
func (w *Watcher) Status() ServiceStatus {
	w.mu.Lock()
	defer w.mu.Unlock()

	s := ServiceStatus{
		Name:      w.config.Name,
		Ready:     w.ready.Load(),
		LastCheck: w.lastCheck,
	}
	if w.lastErr != nil {
		s.LastError = w.lastErr.Error()
	}
	return s
}

// Wait blocks until the watcher goroutine exits (context cancelled or Stop called).
func (w *Watcher) Wait() {
	<-w.done
}

// Stop cancels the watcher and waits for its goroutine to exit.
func (w *Watcher) Stop() {
	w.cancel()
	<-w.done
}

// run is the main goroutine. Phase 1: startup probe with exponential backoff.
// Phase 2: periodic background polling with state-transition callbacks.
func (w *Watcher) run(ctx context.Context) {
	defer close(w.done)

	cfg := w.config.Backoff
	logger := w.config.Logger

	// Phase 1: startup probe with exponential backoff.
	delay := cfg.InitialDelay
	for attempt := 1; attempt <= cfg.MaxRetries; attempt++ {
		err := w.probe(ctx)
		w.recordResult(err)

		if err == nil {
			// Connected on startup.
			w.ready.Store(true)
			logger.Info("service connected",
				"service", w.config.Name,
				"after_attempts", attempt,
			)
			if w.config.OnReady != nil {
				go w.config.OnReady()
			}
			break
		}

		if attempt == cfg.MaxRetries {
			logger.Info("startup connection failed, entering background polling",
				"service", w.config.Name,
				"attempts", attempt,
				"error", err,
			)
			break
		}

		logger.Debug("startup probe failed, retrying",
			"service", w.config.Name,
			"attempt", attempt,
			"max_retries", cfg.MaxRetries,
			"next_delay", delay.String(),
			"error", err,
		)

		if !sleepCtx(ctx, delay) {
			return // context cancelled
		}

		// Grow delay with ceiling.
		delay = time.Duration(float64(delay) * cfg.Multiplier)
		if delay > cfg.MaxDelay {
			delay = cfg.MaxDelay
		}
	}

	// Phase 2: background periodic polling.
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := w.probe(ctx)
			w.recordResult(err)
			wasReady := w.ready.Load()

			if wasReady && err != nil {
				// Transition: ready → down.
				w.ready.Store(false)
				logger.Info("service became unreachable",
					"service", w.config.Name,
					"error", err,
				)
				if w.config.OnDown != nil {
					go w.config.OnDown(err)
				}
			} else if !wasReady && err == nil {
				// Transition: down → ready.
				w.ready.Store(true)
				logger.Info("service recovered",
					"service", w.config.Name,
				)
				if w.config.OnReady != nil {
					go w.config.OnReady()
				}
			} else if !wasReady && err != nil {
				logger.Debug("service still unreachable",
					"service", w.config.Name,
					"error", err,
				)
			}
		}
	}
}

// probe calls the configured ProbeFunc with a timeout.
func (w *Watcher) probe(ctx context.Context) error {
	timeout := w.config.Backoff.ProbeTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return w.config.Probe(probeCtx)
}

// recordResult stores the probe outcome under the mutex.
func (w *Watcher) recordResult(err error) {
	w.mu.Lock()
	w.lastErr = err
	w.lastCheck = time.Now()
	w.mu.Unlock()
}

// sleepCtx sleeps for d or until ctx is cancelled. Returns false if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Manager coordinates multiple service watchers.
type Manager struct {
	mu       sync.RWMutex
	watchers map[string]*Watcher
	logger   *slog.Logger
}

// NewManager creates a connection watch manager.
func NewManager(logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		watchers: make(map[string]*Watcher),
		logger:   logger,
	}
}

// Watch registers and starts a new service watcher. The watcher runs in a
// background goroutine until ctx is cancelled or Stop is called.
//
// Panics if Name is empty or Probe is nil — these are programming errors
// that should be caught during development, not silently ignored at runtime.
// Zero-value BackoffConfig fields are replaced with defaults.
func (m *Manager) Watch(ctx context.Context, cfg WatcherConfig) *Watcher {
	if cfg.Name == "" {
		panic("connwatch: WatcherConfig.Name must not be empty")
	}
	if cfg.Probe == nil {
		panic("connwatch: WatcherConfig.Probe must not be nil")
	}
	if cfg.Logger == nil {
		cfg.Logger = m.logger
	}

	// Apply defaults for zero-value backoff fields.
	defaults := DefaultBackoffConfig()
	if cfg.Backoff.InitialDelay <= 0 {
		cfg.Backoff.InitialDelay = defaults.InitialDelay
	}
	if cfg.Backoff.MaxDelay <= 0 {
		cfg.Backoff.MaxDelay = defaults.MaxDelay
	}
	if cfg.Backoff.Multiplier <= 0 {
		cfg.Backoff.Multiplier = defaults.Multiplier
	}
	if cfg.Backoff.MaxRetries <= 0 {
		cfg.Backoff.MaxRetries = defaults.MaxRetries
	}
	if cfg.Backoff.PollInterval <= 0 {
		cfg.Backoff.PollInterval = defaults.PollInterval
	}
	if cfg.Backoff.ProbeTimeout <= 0 {
		cfg.Backoff.ProbeTimeout = defaults.ProbeTimeout
	}

	watchCtx, cancel := context.WithCancel(ctx)
	w := &Watcher{
		config: cfg,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	go w.run(watchCtx)

	m.mu.Lock()
	m.watchers[cfg.Name] = w
	m.mu.Unlock()

	return w
}

// Status returns the health status of all watched services.
func (m *Manager) Status() map[string]ServiceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := make(map[string]ServiceStatus, len(m.watchers))
	for name, w := range m.watchers {
		status[name] = w.Status()
	}
	return status
}

// Stop shuts down all watchers and waits for their goroutines to exit.
func (m *Manager) Stop() {
	m.mu.RLock()
	watchers := make([]*Watcher, 0, len(m.watchers))
	for _, w := range m.watchers {
		watchers = append(watchers, w)
	}
	m.mu.RUnlock()

	for _, w := range watchers {
		w.Stop()
	}
}
