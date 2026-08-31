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

	mu       sync.RWMutex
	entries  map[string]stateCacheEntry
	inflight map[string]*inflightFetch
}

type stateCacheEntry struct {
	state *State
	at    time.Time
}

// inflightFetch coalesces concurrent misses for one entity: the first
// caller fetches, everyone else waits on done. state/err are written
// before done closes, so readers after <-done see them without a lock.
type inflightFetch struct {
	done  chan struct{}
	state *State
	err   error
}

// stateReadFetchTimeout bounds the shared read-through fetch. The fetch
// runs on a detached context on purpose: it serves every caller waiting
// on the same entity, so one caller's cancellation must not poison the
// result for the rest — an abandoning caller just stops waiting.
const stateReadFetchTimeout = 15 * time.Second

// NewStateReadCache wraps inner (the REST client in production) with
// the read cache. Register [StateReadCache.HandleStateChange] on the
// state watcher to keep ingest-covered entities push-fresh; the cache
// works (TTL-bounded) without it.
func NewStateReadCache(inner stateReader) *StateReadCache {
	return &StateReadCache{
		inner:    inner,
		ttl:      stateReadCacheTTL,
		entries:  make(map[string]stateCacheEntry),
		inflight: make(map[string]*inflightFetch),
	}
}

func (c *StateReadCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// GetState serves the cached entity state when it is younger than the
// TTL, otherwise fetches through and repopulates. Concurrent misses for
// the same entity coalesce onto one fetch — at a TTL boundary every
// assembling loop misses together, and without coalescing the cache
// would recreate exactly the per-loop REST fan-out it exists to
// amortize. A fetch error passes through unchanged — a stale entry is
// never substituted for a live failure, so sentinel handling downstream
// keeps seeing what it sees today. A caller whose ctx dies stops
// waiting; the shared fetch completes on its own detached bound and
// still repopulates for everyone else.
func (c *StateReadCache) GetState(ctx context.Context, entityID string) (*State, error) {
	now := c.clock()
	c.mu.RLock()
	entry, ok := c.entries[entityID]
	c.mu.RUnlock()
	if ok && now.Sub(entry.at) < c.ttl {
		return entry.state, nil
	}

	c.mu.Lock()
	// Re-check under the write lock: an event or a completed fetch may
	// have landed while we waited for it.
	if entry, ok := c.entries[entityID]; ok && c.clock().Sub(entry.at) < c.ttl {
		c.mu.Unlock()
		return entry.state, nil
	}
	if fl, ok := c.inflight[entityID]; ok {
		c.mu.Unlock()
		select {
		case <-fl.done:
			return fl.state, fl.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	fl := &inflightFetch{done: make(chan struct{})}
	c.inflight[entityID] = fl
	c.mu.Unlock()

	go c.fetch(entityID, fl)

	select {
	case <-fl.done:
		return fl.state, fl.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// fetch performs the shared read-through for one entity and completes
// the in-flight slot. The store is timestamped at fetch start, so an
// event that arrives mid-flight (stamped later) wins over this
// response in the monotonic store.
func (c *StateReadCache) fetch(entityID string, fl *inflightFetch) {
	ctx, cancel := context.WithTimeout(context.Background(), stateReadFetchTimeout)
	defer cancel()
	at := c.clock()
	state, err := c.inner.GetState(ctx, entityID)
	if err == nil {
		c.store(entityID, cloneState(state), at)
	}
	fl.state, fl.err = state, err
	c.mu.Lock()
	delete(c.inflight, entityID)
	c.mu.Unlock()
	close(fl.done)
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

// store installs an entry, monotonic per entity and bounded overall. A
// write stamped strictly older than the existing entry is dropped: a
// slow fetch completion (stamped at fetch start) must not roll the
// cache back over an event that arrived mid-flight, and an older
// concurrent fetch must not clobber a newer one. Same-instant writes
// overwrite, so the in-order event stream always advances the entry
// even under a coarse test clock. Under the cap, expired entries sweep
// first and then the single stalest entry is evicted — seeded from the
// map, never from the incoming timestamp, so an old-stamped insert
// still makes room for itself.
func (c *StateReadCache) store(entityID string, state *State, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, exists := c.entries[entityID]; exists {
		if at.Before(existing.at) {
			return
		}
		c.entries[entityID] = stateCacheEntry{state: state, at: at}
		return
	}
	if len(c.entries) >= stateReadCacheMaxEntries {
		now := c.clock()
		for id, entry := range c.entries {
			if now.Sub(entry.at) >= c.ttl {
				delete(c.entries, id)
			}
		}
		if len(c.entries) >= stateReadCacheMaxEntries {
			var oldestID string
			var oldestAt time.Time
			for id, entry := range c.entries {
				if oldestID == "" || entry.at.Before(oldestAt) {
					oldestID, oldestAt = id, entry.at
				}
			}
			delete(c.entries, oldestID)
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
