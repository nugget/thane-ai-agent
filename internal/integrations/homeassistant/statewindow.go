package homeassistant

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// StateWindowEntry records a single state transition observed from the Home
// Assistant WebSocket event stream. DeviceClass is the effective
// device_class at event time (carrying the operator's "Show as"
// override), kept so the render can translate raw states into
// class-aware labels.
type StateWindowEntry struct {
	EntityID    string
	OldState    string
	NewState    string
	DeviceClass string
	Timestamp   time.Time
}

// Per-entity retention bounds (#1210). The shared window churns fast
// in a busy house; these rings let a subscription render its own
// entity's recent transitions regardless of what else was chattering.
const (
	// perEntityTransitionCap bounds each entity's transition ring.
	perEntityTransitionCap = 32
	// maxTrackedEntities bounds how many entities carry rings at
	// once; the least-recently-updated entity is evicted on overflow.
	// Entities only reach the window through the ingestion filter, so
	// this is a backstop, not the primary cardinality control.
	maxTrackedEntities = 512
)

// Transition is one class-aware state change from an entity's
// retention ring: From/To already speak the same translated
// vocabulary as every other entity-emitting surface. Returned by
// [StateWindowProvider.RecentTransitions] newest first.
type Transition struct {
	From string
	To   string
	At   time.Time
}

// entityRing is one entity's bounded transition history plus the
// last-write stamp the eviction policy keys on.
type entityRing struct {
	entries   []StateWindowEntry // circular buffer, pre-allocated
	head      int
	count     int
	lastWrite time.Time
}

// StateWindowProvider maintains a rolling window of recent state changes and
// implements [agent.TagContextProvider]. It is safe for concurrent use:
// HandleStateChange writes under a write lock while TagContext reads
// under a read lock. Alongside the shared window it keeps bounded
// per-entity transition rings so subscription renders can ask for one
// entity's recent changes without racing the shared buffer's churn.
type StateWindowProvider struct {
	mu        sync.RWMutex
	entries   []StateWindowEntry // circular buffer, pre-allocated
	head      int                // next write position
	count     int                // entries currently stored (≤ len(entries))
	perEntity map[string]*entityRing
	maxAge    time.Duration
	nowFunc   func() time.Time
	logger    *slog.Logger
	// translate maps (domain, deviceClass, rawState) to the class-aware
	// label the model should read (garage_door "on" → "open"). Injected
	// at construction — the canonical table lives in contextfmt, which
	// imports this package, so the dependency must point inward. Nil
	// means raw states pass through untranslated.
	translate func(domain, deviceClass, state string) string
}

// NewStateWindowProvider creates a state window provider with the given
// buffer capacity and maximum entry age. translate is the class-aware
// state translation applied when rendering transitions (normally
// contextfmt.SemanticState, so a garage_door reads closed→open rather
// than off→on — the same language every other entity-emitting surface
// uses); nil renders raw states. Entries older than maxAge are filtered
// out at read time in TagContext.
func NewStateWindowProvider(maxEntries int, maxAge time.Duration, translate func(domain, deviceClass, state string) string, logger *slog.Logger) *StateWindowProvider {
	if maxEntries <= 0 {
		maxEntries = 50
	}
	if maxAge <= 0 {
		maxAge = 30 * time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &StateWindowProvider{
		entries:   make([]StateWindowEntry, maxEntries),
		perEntity: make(map[string]*entityRing),
		maxAge:    maxAge,
		translate: translate,
		nowFunc:   time.Now,
		logger:    logger,
	}
}

// TagContextBucket places recent Home Assistant state changes in live
// state.
func (p *StateWindowProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketLiveState
}

// HandleStateChange records a state transition in the circular buffer.
// It matches the homeassistant.StateWatchHandler function signature and
// can be composed directly into the state watcher handler chain.
func (p *StateWindowProvider) HandleStateChange(entityID, oldState, newState, deviceClass string) {
	// Filter no-op transitions (device tracker refreshes, etc.).
	if oldState == newState {
		return
	}

	now := p.nowFunc()
	entry := StateWindowEntry{
		EntityID:    entityID,
		OldState:    oldState,
		NewState:    newState,
		DeviceClass: deviceClass,
		Timestamp:   now,
	}

	p.mu.Lock()
	p.entries[p.head] = entry
	p.head = (p.head + 1) % len(p.entries)
	if p.count < len(p.entries) {
		p.count++
	}
	p.recordPerEntity(entry)
	p.mu.Unlock()
}

// recordPerEntity appends the entry to its entity's retention ring,
// creating the ring (and evicting the least-recently-updated entity
// when the tracked-entity cap is hit) as needed. Caller holds p.mu.
func (p *StateWindowProvider) recordPerEntity(entry StateWindowEntry) {
	ring, ok := p.perEntity[entry.EntityID]
	if !ok {
		if len(p.perEntity) >= maxTrackedEntities {
			var (
				oldestID string
				oldestAt time.Time
			)
			for id, r := range p.perEntity {
				if oldestID == "" || r.lastWrite.Before(oldestAt) {
					oldestID, oldestAt = id, r.lastWrite
				}
			}
			delete(p.perEntity, oldestID)
		}
		ring = &entityRing{entries: make([]StateWindowEntry, perEntityTransitionCap)}
		p.perEntity[entry.EntityID] = ring
	}
	ring.entries[ring.head] = entry
	ring.head = (ring.head + 1) % len(ring.entries)
	if ring.count < len(ring.entries) {
		ring.count++
	}
	ring.lastWrite = entry.Timestamp
}

// RecentTransitions returns up to limit of entityID's retained state
// changes, newest first, in the class-aware vocabulary. A positive
// window keeps only changes within the trailing window; zero means
// the retention ring's count bound is the only limit. matched is the
// number of retained changes that passed the window filter BEFORE
// limit was applied, so callers can advertise truncation honestly. A
// limit <= 0 returns every match.
func (p *StateWindowProvider) RecentTransitions(entityID string, limit int, window time.Duration) (transitions []Transition, matched int) {
	now := p.nowFunc()

	p.mu.RLock()
	defer p.mu.RUnlock()

	ring, ok := p.perEntity[entityID]
	if !ok || ring.count == 0 {
		return nil, 0
	}
	var cutoff time.Time
	if window > 0 {
		cutoff = now.Add(-window)
	}
	bufLen := len(ring.entries)
	for i := 0; i < ring.count; i++ {
		idx := (ring.head - 1 - i + bufLen) % bufLen
		e := ring.entries[idx]
		if window > 0 && e.Timestamp.Before(cutoff) {
			// Ring is newest-first from here; everything older also
			// fails the window.
			break
		}
		matched++
		if limit > 0 && len(transitions) >= limit {
			continue
		}
		from, to := e.OldState, e.NewState
		if p.translate != nil {
			domain, _, _ := strings.Cut(e.EntityID, ".")
			from = p.translate(domain, e.DeviceClass, from)
			to = p.translate(domain, e.DeviceClass, to)
		}
		transitions = append(transitions, Transition{From: from, To: to, At: e.Timestamp})
	}
	return transitions, matched
}

// stateChangeJSON is the compact JSON structure for a state transition.
type stateChangeJSON struct {
	Entity string `json:"entity"`
	From   string `json:"from"`
	To     string `json:"to"`
	Ago    string `json:"ago"`
}

// TagContext returns a formatted context block listing recent state
// changes as compact JSON for injection into the agent's system prompt.
// Entries older than maxAge are excluded. Returns an empty string when
// no valid entries exist. Implements [agent.TagContextProvider];
// registered via RegisterAlwaysContextProvider.
func (p *StateWindowProvider) TagContext(_ context.Context, _ agentctx.ContextRequest) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.count == 0 {
		return "", nil
	}

	now := p.nowFunc()
	cutoff := now.Add(-p.maxAge)
	bufLen := len(p.entries)

	// Collect valid entries in reverse chronological order (newest first).
	var entries []stateChangeJSON
	for i := 0; i < p.count; i++ {
		idx := (p.head - 1 - i + bufLen) % bufLen
		e := p.entries[idx]
		if e.Timestamp.Before(cutoff) {
			continue
		}
		from, to := e.OldState, e.NewState
		if p.translate != nil {
			// Render class-aware labels so the model reads closed→open,
			// not off→on, for a garage_door — matching every other
			// entity-emitting surface. An entity's first observation has
			// an empty OldState; translation passes unmapped values
			// through, so that stays empty.
			domain, _, _ := strings.Cut(e.EntityID, ".")
			from = p.translate(domain, e.DeviceClass, from)
			to = p.translate(domain, e.DeviceClass, to)
		}
		entries = append(entries, stateChangeJSON{
			Entity: e.EntityID,
			From:   from,
			To:     to,
			Ago:    promptfmt.FormatDeltaOnly(e.Timestamp, now),
		})
	}

	if len(entries) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("### Recent State Changes\n\n")
	for _, entry := range entries {
		data, _ := json.Marshal(entry)
		sb.Write(data)
		sb.WriteByte('\n')
	}

	return sb.String(), nil
}
