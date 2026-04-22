package homeassistant

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/promptfmt"
)

// StateWindowEntry records a single state transition observed from the Home
// Assistant WebSocket event stream.
type StateWindowEntry struct {
	EntityID  string
	OldState  string
	NewState  string
	Timestamp time.Time
}

// StateWindowProvider maintains a rolling window of recent state changes and
// implements the agent.ContextProvider interface. It is safe for
// concurrent use: HandleStateChange writes under a write lock while
// GetContext reads under a read lock.
type StateWindowProvider struct {
	mu      sync.RWMutex
	entries []StateWindowEntry // circular buffer, pre-allocated
	head    int                // next write position
	count   int                // entries currently stored (≤ len(entries))
	maxAge  time.Duration
	loc     *time.Location
	nowFunc func() time.Time
	logger  *slog.Logger
}

// NewStateWindowProvider creates a state window provider with the given
// buffer capacity and maximum entry age. The loc parameter controls the
// timezone used when formatting future-event timestamps in the context
// output; nil falls back to time.Local. Entries older than maxAge are
// filtered out at read time in GetContext.
func NewStateWindowProvider(maxEntries int, maxAge time.Duration, loc *time.Location, logger *slog.Logger) *StateWindowProvider {
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
		entries: make([]StateWindowEntry, maxEntries),
		maxAge:  maxAge,
		loc:     loc,
		nowFunc: time.Now,
		logger:  logger,
	}
}

// HandleStateChange records a state transition in the circular buffer.
// It matches the homeassistant.StateWatchHandler function signature and
// can be composed directly into the state watcher handler chain.
func (p *StateWindowProvider) HandleStateChange(entityID, oldState, newState string) {
	// Filter no-op transitions (device tracker refreshes, etc.).
	if oldState == newState {
		return
	}

	now := p.nowFunc()

	p.mu.Lock()
	p.entries[p.head] = StateWindowEntry{
		EntityID:  entityID,
		OldState:  oldState,
		NewState:  newState,
		Timestamp: now,
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

// GetContext returns a formatted context block listing recent state
// changes as compact JSON for injection into the agent's system prompt.
// Entries older than maxAge are excluded. Returns an empty string when
// no valid entries exist.
func (p *StateWindowProvider) GetContext(_ context.Context, _ string) (string, error) {
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
		entries = append(entries, stateChangeJSON{
			Entity: e.EntityID,
			From:   e.OldState,
			To:     e.NewState,
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
