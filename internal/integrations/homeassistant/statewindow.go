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

// StateWindowProvider maintains a rolling window of recent state changes and
// implements [agent.TagContextProvider]. It is safe for concurrent use:
// HandleStateChange writes under a write lock while TagContext reads
// under a read lock.
type StateWindowProvider struct {
	mu      sync.RWMutex
	entries []StateWindowEntry // circular buffer, pre-allocated
	head    int                // next write position
	count   int                // entries currently stored (≤ len(entries))
	maxAge  time.Duration
	loc     *time.Location
	nowFunc func() time.Time
	logger  *slog.Logger
	// translate maps (domain, deviceClass, rawState) to the class-aware
	// label the model should read (garage_door "on" → "open"). Injected
	// at construction — the canonical table lives in contextfmt, which
	// imports this package, so the dependency must point inward. Nil
	// means raw states pass through untranslated.
	translate func(domain, deviceClass, state string) string
}

// NewStateWindowProvider creates a state window provider with the given
// buffer capacity and maximum entry age. The loc parameter controls the
// timezone used when formatting future-event timestamps in the context
// output; nil falls back to time.Local. translate is the class-aware
// state translation applied when rendering transitions (normally
// contextfmt.SemanticState, so a garage_door reads closed→open rather
// than off→on — the same language every other entity-emitting surface
// uses); nil renders raw states. Entries older than maxAge are filtered
// out at read time in TagContext.
func NewStateWindowProvider(maxEntries int, maxAge time.Duration, loc *time.Location, translate func(domain, deviceClass, state string) string, logger *slog.Logger) *StateWindowProvider {
	if maxEntries <= 0 {
		maxEntries = 50
	}
	if maxAge <= 0 {
		maxAge = 30 * time.Minute
	}
	if loc == nil {
		loc = time.Local
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &StateWindowProvider{
		entries:   make([]StateWindowEntry, maxEntries),
		maxAge:    maxAge,
		loc:       loc,
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

	p.mu.Lock()
	p.entries[p.head] = StateWindowEntry{
		EntityID:    entityID,
		OldState:    oldState,
		NewState:    newState,
		DeviceClass: deviceClass,
		Timestamp:   now,
	}
	p.head = (p.head + 1) % len(p.entries)
	if p.count < len(p.entries) {
		p.count++
	}
	p.mu.Unlock()
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
