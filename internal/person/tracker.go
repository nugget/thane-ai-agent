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
// "zone.work". Room fields are populated by an external poller (e.g.,
// UniFi AP associations) when available.
type Person struct {
	EntityID     string
	FriendlyName string
	State        string
	Since        time.Time
	DeviceMACs   []string  // configured MAC addresses for this person
	Room         string    // inferred from AP association (e.g., "office")
	RoomSince    time.Time // when the current room was first detected
	RoomSource   string    // AP name that determined the room (e.g., "ap-hor-office")
}

// StateGetter abstracts the Home Assistant REST client for fetching
// entity state. Using an interface keeps the tracker testable without
// a real HA instance.
type StateGetter interface {
	GetState(ctx context.Context, entityID string) (*homeassistant.State, error)
}

// RoomObserver is called when a tracked person's room changes.
// Parameters are the person's entity ID, the new room name (may be
// empty when cleared), and the AP or source name that determined
// the room. Observers are called outside the tracker's lock.
type RoomObserver func(entityID, room, source string)

// Tracker maintains in-memory presence state for configured person
// entities and provides a context block for system prompt injection.
// It implements both the StateWatchHandler function signature (for
// receiving WebSocket state changes) and the agent.ContextProvider
// interface (for context injection).
type Tracker struct {
	people    map[string]*Person // entity_id → Person
	order     []string           // insertion order for deterministic output
	observers []RoomObserver     // called on room changes
	mu        sync.RWMutex
	loc       *time.Location
	logger    *slog.Logger
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
// entities and no-change events are silently ignored. Room data is
// cleared when a person transitions to "not_home".
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

	// Clear room data when person leaves home — room presence is
	// only meaningful while at home.
	if strings.EqualFold(newState, "not_home") {
		p.Room = ""
		p.RoomSince = time.Time{}
		p.RoomSource = ""
	}
}

// GetContext returns a formatted presence block for injection into the
// agent's system prompt. Returns an empty string if no entities are
// tracked. This method satisfies the agent.ContextProvider interface.
//
// Output uses nested markdown with ISO 8601 timestamps for efficient
// model consumption. Fields are only emitted when they have values.
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
		displayName := TitleCase(p.FriendlyName)

		fmt.Fprintf(&sb, "- **%s**:\n", displayName)

		if p.State == "Unknown" || p.Since.IsZero() {
			sb.WriteString("  - Unknown\n")
		} else {
			displayState := formatState(p.State)
			since := p.Since.In(t.loc).Format(time.RFC3339)
			fmt.Fprintf(&sb, "  - %s since %s\n", displayState, since)
		}

		if p.Room != "" {
			fmt.Fprintf(&sb, "  - Room: %s\n", p.Room)
		}

		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// OnRoomChange registers a callback that fires whenever a tracked
// person's room changes. Observers are called outside the tracker's
// lock so they may perform blocking I/O (e.g., MQTT publishes).
// Must be called before the poller starts.
func (t *Tracker) OnRoomChange(fn RoomObserver) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.observers = append(t.observers, fn)
}

// UpdateRoom sets the room for a tracked person. If the room is
// unchanged, no update occurs. When a person transitions to not_home,
// HandleStateChange clears room data automatically; callers may also
// pass an empty room to clear it explicitly. The source is the AP name
// or other identifier that determined the room.
//
// Registered [RoomObserver] callbacks are invoked after the state
// update, outside the lock, so they may perform blocking operations.
func (t *Tracker) UpdateRoom(entityID, room, source string) {
	var notify bool

	t.mu.Lock()
	p, ok := t.people[entityID]
	if !ok {
		t.mu.Unlock()
		return
	}

	if p.Room == room {
		t.mu.Unlock()
		return
	}

	t.logger.Debug("person room changed",
		"entity_id", entityID,
		"friendly_name", p.FriendlyName,
		"old_room", p.Room,
		"new_room", room,
		"source", source,
	)

	p.Room = room
	p.RoomSource = source
	if room != "" {
		p.RoomSince = time.Now()
	} else {
		p.RoomSince = time.Time{}
	}

	notify = len(t.observers) > 0
	// Copy observer slice reference under lock. The slice is append-only
	// and only grows before the poller starts, so reading it after unlock
	// is safe.
	obs := t.observers
	t.mu.Unlock()

	if notify {
		for _, fn := range obs {
			fn(entityID, room, source)
		}
	}
}

// SetDeviceMACs configures the MAC addresses associated with a tracked
// person. These MACs are used by the UniFi poller to determine which
// person a wireless device belongs to. Must be called before the poller
// starts. Untracked entities are silently ignored.
func (t *Tracker) SetDeviceMACs(entityID string, macs []string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	p, ok := t.people[entityID]
	if !ok {
		return
	}
	p.DeviceMACs = macs
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
	return TitleCase(state)
}

// TitleCase capitalizes the first rune of a string.
func TitleCase(s string) string {
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
