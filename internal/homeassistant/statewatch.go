package homeassistant

import (
	"context"
	"encoding/json"
	"log/slog"
	"path"
	"sync"
	"time"
)

// StateWatchHandler is called for each state change that passes the
// entity filter and rate limiter. The handler receives the entity ID,
// old state string, and new state string.
type StateWatchHandler func(entityID, oldState, newState string)

// EntityFilter selects which entity IDs to process using glob patterns.
// An empty filter matches all entities.
type EntityFilter struct {
	patterns []string
	logger   *slog.Logger
}

// NewEntityFilter creates an entity filter from glob patterns. Patterns
// use [path.Match] syntax (e.g., "person.*", "binary_sensor.*door*").
// An empty pattern list means all entities match.
func NewEntityFilter(globs []string, logger *slog.Logger) *EntityFilter {
	if logger == nil {
		logger = slog.Default()
	}
	return &EntityFilter{patterns: globs, logger: logger}
}

// Match reports whether the entity ID matches at least one pattern.
// If no patterns are configured, Match always returns true.
func (f *EntityFilter) Match(entityID string) bool {
	if len(f.patterns) == 0 {
		return true
	}
	for _, pat := range f.patterns {
		matched, err := path.Match(pat, entityID)
		if err != nil {
			f.logger.Debug("glob match error", "pattern", pat, "entity_id", entityID, "error", err)
			continue
		}
		if matched {
			return true
		}
	}
	return false
}

// EntityRateLimiter enforces a per-entity sliding window rate limit.
// A limit of zero disables rate limiting entirely.
type EntityRateLimiter struct {
	limit    int
	window   time.Duration
	mu       sync.Mutex
	counters map[string][]time.Time
}

// NewEntityRateLimiter creates a rate limiter that allows at most
// perMinute events per entity within a one-minute sliding window.
// A perMinute value of zero disables rate limiting.
func NewEntityRateLimiter(perMinute int) *EntityRateLimiter {
	return &EntityRateLimiter{
		limit:    perMinute,
		window:   time.Minute,
		counters: make(map[string][]time.Time),
	}
}

// Allow reports whether a state change for the given entity should be
// processed. When the rate limit is zero (disabled), Allow always
// returns true.
func (r *EntityRateLimiter) Allow(entityID string) bool {
	if r.limit <= 0 {
		return true
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-r.window)

	// Prune expired entries.
	timestamps := r.counters[entityID]
	valid := timestamps[:0]
	for _, ts := range timestamps {
		if ts.After(cutoff) {
			valid = append(valid, ts)
		}
	}

	if len(valid) >= r.limit {
		r.counters[entityID] = valid
		return false
	}

	r.counters[entityID] = append(valid, now)
	return true
}

// Cleanup removes counters for entities whose timestamps have all
// expired. This prevents unbounded growth of the counters map when
// entity IDs are dynamically generated or frequently added/removed.
func (r *EntityRateLimiter) Cleanup() {
	if r.limit <= 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := time.Now().Add(-r.window)
	for entityID, timestamps := range r.counters {
		if len(timestamps) == 0 {
			delete(r.counters, entityID)
			continue
		}
		// If the most recent timestamp is expired, the whole entry is stale.
		if timestamps[len(timestamps)-1].Before(cutoff) {
			delete(r.counters, entityID)
		}
	}
}

// StateWatcher reads state_changed events from a Home Assistant
// WebSocket event channel, applies entity filtering and rate limiting,
// and dispatches matching events to a handler.
type StateWatcher struct {
	events  <-chan Event
	filter  *EntityFilter
	limiter *EntityRateLimiter
	handler StateWatchHandler
	logger  *slog.Logger
}

// NewStateWatcher creates a state watcher that consumes events from the
// given channel. The filter and limiter control which events reach the
// handler. A nil filter or limiter disables that stage.
func NewStateWatcher(events <-chan Event, filter *EntityFilter, limiter *EntityRateLimiter, handler StateWatchHandler, logger *slog.Logger) *StateWatcher {
	if logger == nil {
		logger = slog.Default()
	}
	if filter == nil {
		filter = NewEntityFilter(nil, logger)
	}
	if limiter == nil {
		limiter = NewEntityRateLimiter(0)
	}
	return &StateWatcher{
		events:  events,
		filter:  filter,
		limiter: limiter,
		handler: handler,
		logger:  logger,
	}
}

// Run reads events from the channel until the context is cancelled or
// the channel is closed. It blocks the calling goroutine.
func (w *StateWatcher) Run(ctx context.Context) {
	w.logger.Info("state watcher started")
	defer w.logger.Info("state watcher stopped")

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.events:
			if !ok {
				return
			}
			w.handleEvent(ev)
		}
	}
}

// handleEvent processes a single event from the channel.
func (w *StateWatcher) handleEvent(ev Event) {
	if ev.Type != "state_changed" {
		return
	}

	var data StateChangedData
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		w.logger.Debug("failed to unmarshal state_changed data", "error", err)
		return
	}

	// Skip entity removals (NewState is nil when an entity is deleted).
	if data.NewState == nil {
		return
	}

	if !w.filter.Match(data.EntityID) {
		return
	}

	if !w.limiter.Allow(data.EntityID) {
		w.logger.Debug("rate limited state change", "entity_id", data.EntityID)
		return
	}

	oldState := ""
	if data.OldState != nil {
		oldState = data.OldState.State
	}

	w.handler(data.EntityID, oldState, data.NewState.State)
}
