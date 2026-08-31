package homeassistant

import (
	"context"
	"sync"
	"time"
)

// stateReadCacheTTL bounds how stale a cached entity state may be
// served, regardless of how the entry arrived. The bound is deliberately
// uniform: the event feed that refreshes entries can silently miss
// updates three different ways (a reconnect window is not replayed, the
// event channel evicts oldest on overflow, and the ingest rate limiter
// suppresses a chatty entity's newest changes), so no entry is trusted
// past the TTL no matter its source. Expiry falls through to one REST
// fetch, which repopulates the entry for every concurrent reader.
const stateReadCacheTTL = 30 * time.Second

// stateReadCacheMaxEntries caps the cache. Read-through entries are
// bounded by the subscription sets that fetch them and event entries by
// the ingest filter, so the cap is a backstop, not a working limit.
const stateReadCacheMaxEntries = 1024

// stateReader is the slice of the REST client the cache decorates —
// structurally identical to awareness.StateGetter, declared locally so
// the dependency points from awareness to this package, not back.
type stateReader interface {
	GetState(ctx context.Context, entityID string) (*State, error)
	GetStates(ctx context.Context) ([]State, error)
	GetStateHistory(ctx context.Context, entityID string, startTime, endTime time.Time) ([]State, error)
	GetWeatherForecasts(ctx context.Context, entityID, forecastType string) ([]map[string]any, error)
}

// StateReadCache decorates per-entity state reads with a short-TTL
// cache refreshed opportunistically from the state_changed event feed.
// It exists for the serial context-assembly walk (#1473): the watchlist
// renderers pay one REST round-trip per concrete subscribed entity per
// turn for state that mostly just flowed through our own WebSocket.
// With the cache, entities on the ingest feed are served push-fresh
// from memory, and everything else amortizes one fetch per TTL across
// every concurrently assembling loop. Bulk, history, and forecast reads
// pass through untouched — the glob path already memoizes per render.
//
// Returned states are shared and must be treated as read-only, the same
// contract the event fan-out already imposes.
type StateReadCache struct {
	inner stateReader
	ttl   time.Duration
	now   func() time.Time // injectable clock; nil uses time.Now

	mu      sync.RWMutex
	entries map[string]stateCacheEntry
}

type stateCacheEntry struct {
	state *State
	at    time.Time
}

// NewStateReadCache wraps inner (the REST client in production) with
// the read cache. Register [StateReadCache.HandleStateChange] on the
// state watcher to keep ingest-covered entities push-fresh; the cache
// works (TTL-bounded) without it.
func NewStateReadCache(inner stateReader) *StateReadCache {
	return &StateReadCache{
		inner:   inner,
		ttl:     stateReadCacheTTL,
		entries: make(map[string]stateCacheEntry),
	}
}

func (c *StateReadCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// GetState serves the cached entity state when it is younger than the
// TTL, otherwise fetches through and repopulates. A fetch error passes
// through unchanged — a stale entry is never substituted for a live
// failure, so sentinel handling downstream keeps seeing what it sees
// today.
func (c *StateReadCache) GetState(ctx context.Context, entityID string) (*State, error) {
	now := c.clock()
	c.mu.RLock()
	entry, ok := c.entries[entityID]
	c.mu.RUnlock()
	if ok && now.Sub(entry.at) < c.ttl {
		return entry.state, nil
	}

	state, err := c.inner.GetState(ctx, entityID)
	if err != nil {
		return nil, err
	}
	c.store(entityID, cloneState(state), now)
	return state, nil
}

// HandleStateChange is a [StateChangeHandler]: it refreshes the cache
// from the event feed. It runs synchronously on the state-watcher loop,
// so it only clones and stores. NewState is non-nil by the watcher's
// contract.
func (c *StateReadCache) HandleStateChange(change StateChangedData) {
	if change.NewState == nil {
		return
	}
	c.store(change.NewState.EntityID, cloneState(change.NewState), c.clock())
}

// store inserts under the cap: at capacity it first sweeps expired
// entries, then evicts the single stalest one — the cap is a backstop
// against an unexpectedly wide event feed, not a tuning knob.
func (c *StateReadCache) store(entityID string, state *State, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[entityID]; !exists && len(c.entries) >= stateReadCacheMaxEntries {
		for id, entry := range c.entries {
			if at.Sub(entry.at) >= c.ttl {
				delete(c.entries, id)
			}
		}
		if len(c.entries) >= stateReadCacheMaxEntries {
			oldestID, oldestAt := "", at
			for id, entry := range c.entries {
				if entry.at.Before(oldestAt) {
					oldestID, oldestAt = id, entry.at
				}
			}
			if oldestID != "" {
				delete(c.entries, oldestID)
			}
		}
	}
	c.entries[entityID] = stateCacheEntry{state: state, at: at}
}

// cloneState copies the state and shallow-copies its attributes map, so
// a stored entry cannot be mutated through the shared event payload
// (nested attribute values keep the fan-out's read-only contract).
func cloneState(s *State) *State {
	if s == nil {
		return nil
	}
	clone := *s
	if s.Attributes != nil {
		clone.Attributes = make(map[string]any, len(s.Attributes))
		for k, v := range s.Attributes {
			clone.Attributes[k] = v
		}
	}
	return &clone
}

// GetStates passes through: the bulk snapshot is fetched at most once
// per render and memoized there, and populating 15k entities into a
// per-entity cache would trade a bounded map for the whole firehose.
func (c *StateReadCache) GetStates(ctx context.Context) ([]State, error) {
	return c.inner.GetStates(ctx)
}

// GetStateHistory passes through: history is a recorder query, not
// current state.
func (c *StateReadCache) GetStateHistory(ctx context.Context, entityID string, startTime, endTime time.Time) ([]State, error) {
	return c.inner.GetStateHistory(ctx, entityID, startTime, endTime)
}

// GetWeatherForecasts passes through: forecasts are not state_changed
// payloads and have their own cadence.
func (c *StateReadCache) GetWeatherForecasts(ctx context.Context, entityID, forecastType string) ([]map[string]any, error) {
	return c.inner.GetWeatherForecasts(ctx, entityID, forecastType)
}
