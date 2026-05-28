package homeassistant

import (
	"sync"
	"time"
)

// defaultRegistryCacheTTL is the default freshness window for cached HA
// registry snapshots. The entity/device/area/label/floor registries
// change only on Home Assistant configuration edits, so a short window
// collapses the repeated (multi-MB at 15k+ entities) registry pulls that
// metadata-bearing tool calls would otherwise issue into a single fetch
// per window. Live entity state is deliberately NOT cached here — it is
// volatile and read through one bulk GetStates per call.
const defaultRegistryCacheTTL = 30 * time.Second

// cachedSlice is a single-flight TTL cache for one registry snapshot.
// The fetch runs while the lock is held so concurrent callers for the
// same registry coalesce onto one Home Assistant request rather than
// stampeding it.
//
// The cached slice is shared read-only with every caller inside the TTL
// window; callers MUST NOT mutate the returned slice or its elements.
// All current consumers ([EntityMetadataResolver], area activity) treat
// registry data as immutable.
type cachedSlice[T any] struct {
	mu        sync.Mutex
	value     []T
	fetchedAt time.Time
	valid     bool
}

// get returns the cached value when it is still fresh, otherwise it
// fetches, stores, and returns. A ttl <= 0 disables caching and always
// fetches.
func (c *cachedSlice[T]) get(ttl time.Duration, now time.Time, fetch func() ([]T, error)) ([]T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.valid && ttl > 0 && now.Sub(c.fetchedAt) < ttl {
		return c.value, nil
	}
	value, err := fetch()
	if err != nil {
		return nil, err
	}
	c.value = value
	c.fetchedAt = now
	c.valid = true
	return value, nil
}

// invalidate drops any cached value so the next get refetches. Used
// after a mutation that the cache would otherwise serve stale.
func (c *cachedSlice[T]) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.valid = false
	c.value = nil
}

// registryCache holds the TTL-bounded snapshots of the Home Assistant
// topology registries shared across all native HA tool calls. ttl is
// set once at client construction (and optionally overridden at wiring
// time) before any getter runs, so it carries no separate lock.
type registryCache struct {
	ttl      time.Duration
	areas    cachedSlice[Area]
	entities cachedSlice[EntityRegistryEntry]
	devices  cachedSlice[DeviceRegistryEntry]
	labels   cachedSlice[LabelRegistryEntry]
	floors   cachedSlice[FloorRegistryEntry]
}
