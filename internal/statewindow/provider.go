// Package statewindow maintains a rolling window of Home Assistant state
// changes and injects them into the agent's system prompt. The window
// uses a circular buffer with dual eviction: count-based (buffer
// capacity) and age-based (configurable max age applied at read time).
package statewindow

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Entry records a single state transition observed from the Home
// Assistant WebSocket event stream.
type Entry struct {
	EntityID  string
	OldState  string
	NewState  string
	Timestamp time.Time
}

// Provider maintains a rolling window of recent state changes and
// implements the agent.ContextProvider interface. It is safe for
// concurrent use: HandleStateChange writes under a write lock while
// GetContext reads under a read lock.
type Provider struct {
	mu      sync.RWMutex
	entries []Entry // circular buffer, pre-allocated
	head    int     // next write position
	count   int     // entries currently stored (≤ len(entries))
	maxAge  time.Duration
	loc     *time.Location
	nowFunc func() time.Time
	logger  *slog.Logger
}

// NewProvider creates a state window provider with the given buffer
// capacity and maximum entry age. The loc parameter controls the
// timezone used for ISO 8601 timestamps in the context output; nil
// falls back to time.Local. Entries older than maxAge are filtered
// out at read time in GetContext.
func NewProvider(maxEntries int, maxAge time.Duration, loc *time.Location, logger *slog.Logger) *Provider {
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
	return &Provider{
		entries: make([]Entry, maxEntries),
		maxAge:  maxAge,
		loc:     loc,
		nowFunc: time.Now,
		logger:  logger,
	}
}

// HandleStateChange records a state transition in the circular buffer.
// It matches the homeassistant.StateWatchHandler function signature and
// can be composed directly into the state watcher handler chain.
func (p *Provider) HandleStateChange(entityID, oldState, newState string) {
	now := p.nowFunc()

	p.mu.Lock()
	p.entries[p.head] = Entry{
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

// GetContext returns a formatted context block listing recent state
// changes for injection into the agent's system prompt. Entries older
// than maxAge are excluded. Returns an empty string when no valid
// entries exist.
func (p *Provider) GetContext(_ context.Context, _ string) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.count == 0 {
		return "", nil
	}

	now := p.nowFunc()
	cutoff := now.Add(-p.maxAge)
	bufLen := len(p.entries)

	// Collect valid entries in reverse chronological order (newest first).
	// The newest entry is at (head-1) mod bufLen, walking backwards.
	var lines []string
	for i := 0; i < p.count; i++ {
		idx := (p.head - 1 - i + bufLen) % bufLen
		e := p.entries[idx]
		if e.Timestamp.Before(cutoff) {
			continue
		}
		ts := e.Timestamp.In(p.loc).Format(time.RFC3339)
		lines = append(lines, fmt.Sprintf("- %s: %s → %s (%s)", e.EntityID, e.OldState, e.NewState, ts))
	}

	if len(lines) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("### Recent State Changes\n\n")
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}

	return sb.String(), nil
}
