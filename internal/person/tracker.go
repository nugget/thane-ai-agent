// Package person tracks presence state for configured household members
// and provides context injection into the agent's system prompt.
package person

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
)

// Person represents the current presence state of a tracked household
// member. State is typically "home", "not_home", or a zone name like
// "zone.work".
type Person struct {
	EntityID     string
	FriendlyName string
	State        string
	Since        time.Time
}

// StateGetter abstracts the Home Assistant REST client for fetching
// entity state. Using an interface keeps the tracker testable without
// a real HA instance.
type StateGetter interface {
	GetState(ctx context.Context, entityID string) (*homeassistant.State, error)
}

// Tracker maintains in-memory presence state for configured person
// entities and provides a context block for system prompt injection.
// It implements both the StateWatchHandler function signature (for
// receiving WebSocket state changes) and the agent.ContextProvider
// interface (for context injection).
type Tracker struct {
	people map[string]*Person // entity_id → Person
	order  []string           // insertion order for deterministic output
	mu     sync.RWMutex
	loc    *time.Location
	logger *slog.Logger
}

// NewTracker creates a person tracker for the given entity IDs. All
// entities start in "Unknown" state until Initialize is called. The
// timezone is an IANA location string (e.g., "America/Chicago"); an
// empty or invalid timezone falls back to the system local timezone.
func NewTracker(entityIDs []string, timezone string, logger *slog.Logger) *Tracker {
	if logger == nil {
		logger = slog.Default()
	}

	loc := time.Local
	if timezone != "" {
		if parsed, err := time.LoadLocation(timezone); err == nil {
			loc = parsed
		} else {
			logger.Warn("invalid timezone for person tracker, using local", "timezone", timezone, "error", err)
		}
	}

	people := make(map[string]*Person, len(entityIDs))
	order := make([]string, 0, len(entityIDs))
	for _, id := range entityIDs {
		people[id] = &Person{
			EntityID:     id,
			FriendlyName: friendlyNameFromEntityID(id),
			State:        "Unknown",
		}
		order = append(order, id)
	}

	return &Tracker{
		people: people,
		order:  order,
		loc:    loc,
		logger: logger,
	}
}

// Initialize fetches the current state of all tracked entities from the
// Home Assistant REST API. Entities that fail to load are logged and
// left in "Unknown" state. This method is idempotent and safe to call
// from a connwatch OnReady callback on every reconnection.
//
// Network I/O is performed without holding the lock so that GetContext
// and HandleStateChange are not blocked during initialization.
func (t *Tracker) Initialize(ctx context.Context, ha StateGetter) error {
	// Read entity order without the lock — order is immutable after
	// construction, so this is safe.
	ids := t.EntityIDs()

	// Fetch all states outside the lock to avoid blocking readers
	// during network I/O.
	type fetchResult struct {
		id    string
		state *homeassistant.State
		err   error
	}
	results := make([]fetchResult, 0, len(ids))
	for _, id := range ids {
		state, err := ha.GetState(ctx, id)
		results = append(results, fetchResult{id: id, state: state, err: err})
	}

	// Apply fetched results under the lock.
	t.mu.Lock()
	defer t.mu.Unlock()

	var firstErr error
	for _, r := range results {
		if r.err != nil {
			t.logger.Warn("failed to fetch person state",
				"entity_id", r.id,
				"error", r.err,
			)
			if firstErr == nil {
				firstErr = fmt.Errorf("fetch %s: %w", r.id, r.err)
			}
			continue
		}

		p := t.people[r.id]
		p.State = r.state.State
		p.Since = r.state.LastChanged

		if name, ok := r.state.Attributes["friendly_name"].(string); ok && name != "" {
			p.FriendlyName = name
		}

		t.logger.Debug("person state initialized",
			"entity_id", r.id,
			"friendly_name", p.FriendlyName,
			"state", p.State,
			"since", p.Since,
		)
	}

	return firstErr
}

// HandleStateChange updates the tracked person's state when a
// state_changed event is received. It matches the
// homeassistant.StateWatchHandler function signature. Untracked
// entities and no-change events are silently ignored.
func (t *Tracker) HandleStateChange(entityID, _, newState string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	p, ok := t.people[entityID]
	if !ok {
		return
	}

	if p.State == newState {
		return
	}

	t.logger.Debug("person state changed",
		"entity_id", entityID,
		"friendly_name", p.FriendlyName,
		"old_state", p.State,
		"new_state", newState,
	)

	p.State = newState
	p.Since = time.Now()
}

// GetContext returns a formatted presence block for injection into the
// agent's system prompt. Returns an empty string if no entities are
// tracked. This method satisfies the agent.ContextProvider interface.
func (t *Tracker) GetContext(_ context.Context, _ string) (string, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if len(t.order) == 0 {
		return "", nil
	}

	var sb strings.Builder
	sb.WriteString("### People & Presence\n\n")

	for _, id := range t.order {
		p := t.people[id]
		displayState := formatState(p.State)
		displayName := titleCase(p.FriendlyName)

		if p.State == "Unknown" || p.Since.IsZero() {
			fmt.Fprintf(&sb, "- **%s**: Unknown\n", displayName)
		} else {
			since := p.Since.In(t.loc).Format("Jan 2, 3:04 PM")
			fmt.Fprintf(&sb, "- **%s**: %s (since %s)\n", displayName, displayState, since)
		}
	}

	return sb.String(), nil
}

// EntityIDs returns a copy of the tracked entity IDs. This is used to
// auto-merge person entities into the state watcher's entity filter
// globs so that person state changes are delivered regardless of the
// user's subscribe.entity_globs configuration. The returned slice is
// a defensive copy; callers cannot mutate internal state.
func (t *Tracker) EntityIDs() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	ids := make([]string, len(t.order))
	copy(ids, t.order)
	return ids
}

// formatState converts a Home Assistant person state to a
// human-readable display string. "not_home" becomes "Away"; other
// states are title-cased.
func formatState(state string) string {
	if strings.EqualFold(state, "not_home") {
		return "Away"
	}
	return titleCase(state)
}

// titleCase capitalizes the first rune of a string.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}

// friendlyNameFromEntityID extracts a display name from an entity ID
// by stripping the domain prefix and replacing underscores with spaces.
// For example, "person.nugget" becomes "nugget".
func friendlyNameFromEntityID(id string) string {
	if idx := strings.IndexByte(id, '.'); idx >= 0 {
		return strings.ReplaceAll(id[idx+1:], "_", " ")
	}
	return id
}
