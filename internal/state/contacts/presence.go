package contacts

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// PersonPresenceContext is the JSON structure emitted for each tracked
// person in context output. Richer than the default entity JSON
// because the tracker has provider-attributed room data.
type PersonPresenceContext struct {
	Entity       string `json:"entity"`
	Name         string `json:"name"`
	State        string `json:"state"`
	Since        string `json:"since"`
	Room         string `json:"room,omitempty"`
	RoomProvider string `json:"room_provider,omitempty"`
	RoomSource   string `json:"room_source,omitempty"`
	RoomConflict bool   `json:"room_conflict,omitempty"`
}

// FormatPersonPresence formats a tracked person as compact JSON with
// delta-annotated timestamps.
func FormatPersonPresence(
	entityID, name, state string,
	since time.Time,
	room, roomProvider, roomSource string,
	roomConflict bool,
	now time.Time,
) string {
	displayState := state
	if strings.EqualFold(state, "not_home") {
		displayState = "away"
	}
	if !strings.EqualFold(state, "home") || roomConflict {
		room = ""
		roomProvider = ""
		roomSource = ""
	}
	if !strings.EqualFold(state, "home") {
		roomConflict = false
	}
	pc := PersonPresenceContext{
		Entity:       entityID,
		Name:         name,
		State:        displayState,
		Since:        promptfmt.FormatDeltaOnly(since, now),
		Room:         room,
		RoomProvider: roomProvider,
		RoomSource:   roomSource,
		RoomConflict: roomConflict,
	}
	return promptfmt.MarshalCompact(pc)
}

// Person represents the current presence state of a tracked household
// member. State is typically "home", "not_home", or a zone name like
// "zone.work". Room fields resolve provider-attributed observations such as
// UniFi AP associations and linked Bermuda device trackers.
type Person struct {
	EntityID     string
	FriendlyName string
	State        string
	Since        time.Time
	DeviceMACs   []string  // configured MAC addresses for this person
	Room         string    // resolved room when current observations agree
	RoomSince    time.Time // when the current resolved room began
	RoomProvider string    // shared provider, empty for cross-provider consensus
	RoomSource   string    // shared provider evidence, empty when evidence differs

	roomObservations map[roomObservationKey]RoomObservation
	roomConflict     bool
}

// RoomObserver is called when a tracked person's room observation changes.
// A withdrawal carries an empty room and retains the provider and source of
// the observation being removed. Observers are called outside the tracker's
// lock.
type RoomObserver func(entityID, room, provider, source string)

// PresenceTracker maintains in-memory presence state for configured
// person entities and provides a context block for system prompt
// injection. It implements both the StateWatchHandler function
// signature (for receiving WebSocket state changes) and the
// [agent.TagContextProvider] interface (registered via
// RegisterAlwaysContextProvider).
type PresenceTracker struct {
	people    map[string]*Person // entity_id → Person
	order     []string           // insertion order for deterministic output
	observers []RoomObserver     // called on room changes

	linkedByPerson  map[string][]string
	ownersByTracker map[string][]string
	trackerPlatform map[string]string
	ingestObserver  func([]string)

	mu     sync.RWMutex
	loc    *time.Location
	logger *slog.Logger
}

// NewPresenceTracker creates a person tracker for the given entity IDs. All
// entities start in "Unknown" state until Initialize is called. The
// timezone is an IANA location string (e.g., "America/Chicago"); an
// empty or invalid timezone falls back to the system local timezone.
func NewPresenceTracker(entityIDs []string, timezone string, logger *slog.Logger) *PresenceTracker {
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
			EntityID:         id,
			FriendlyName:     friendlyNameFromEntityID(id),
			State:            "Unknown",
			roomObservations: make(map[roomObservationKey]RoomObservation),
		}
		order = append(order, id)
	}

	return &PresenceTracker{
		people:          people,
		order:           order,
		linkedByPerson:  make(map[string][]string),
		ownersByTracker: make(map[string][]string),
		trackerPlatform: make(map[string]string),
		loc:             loc,
		logger:          logger,
	}
}

// TagContextBucket places current person presence in live state.
func (t *PresenceTracker) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketLiveState
}

// TagContext returns a formatted presence block for injection into the
// agent's system prompt. Returns an empty string if no entities are
// tracked. Implements [agent.TagContextProvider]; registered via
// RegisterAlwaysContextProvider.
//
// People with known state are formatted as compact JSON with delta-
// annotated timestamps following #458 conventions. People with unknown
// or unset state are rendered as plain markdown text.
func (t *PresenceTracker) TagContext(_ context.Context, _ agentctx.ContextRequest) (string, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if len(t.order) == 0 {
		return "", nil
	}

	now := time.Now()

	var sb strings.Builder
	sb.WriteString("### People & Presence\n\n")

	for _, id := range t.order {
		p := t.people[id]
		displayName := TitleCase(p.FriendlyName)

		if p.State == "Unknown" || p.Since.IsZero() {
			fmt.Fprintf(&sb, "- **%s**: unknown\n", displayName)
		} else {
			sb.WriteString(FormatPersonPresence(
				p.EntityID, displayName, p.State, p.Since,
				p.Room, p.RoomProvider, p.RoomSource, p.roomConflict, now,
			))
			sb.WriteByte('\n')
		}
	}

	return sb.String(), nil
}

// SetDeviceMACs configures the MAC addresses associated with a tracked
// person. These MACs are used by the UniFi poller to determine which
// person a wireless device belongs to. Must be called before the poller
// starts. Untracked entities are silently ignored.
func (t *PresenceTracker) SetDeviceMACs(entityID string, macs []string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	p, ok := t.people[entityID]
	if !ok {
		return
	}
	p.DeviceMACs = macs
}

// PersonSnapshot is a read-only copy of one tracked person's live state, for
// consumers that join presence onto other views (#1450). Room is populated
// only when every current observation agrees; RoomConflict distinguishes
// disagreement from no room data. RoomObservations is a deterministic,
// defensive copy of the evidence retained by the tracker.
type PersonSnapshot struct {
	EntityID         string            `json:"entity_id"`
	FriendlyName     string            `json:"friendly_name"`
	State            string            `json:"state"`
	Since            time.Time         `json:"since"`
	Room             string            `json:"room,omitempty"`
	RoomSince        time.Time         `json:"room_since,omitempty"`
	RoomProvider     string            `json:"room_provider,omitempty"`
	RoomSource       string            `json:"room_source,omitempty"`
	RoomConflict     bool              `json:"room_conflict,omitempty"`
	RoomObservations []RoomObservation `json:"room_observations,omitempty"`
}

// Snapshot returns the current state of one tracked person entity.
// The boolean reports whether the entity is tracked at all.
func (t *PresenceTracker) Snapshot(entityID string) (PersonSnapshot, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	p, ok := t.people[entityID]
	if !ok {
		return PersonSnapshot{}, false
	}
	return PersonSnapshot{
		EntityID:         p.EntityID,
		FriendlyName:     p.FriendlyName,
		State:            p.State,
		Since:            p.Since,
		Room:             p.Room,
		RoomSince:        p.RoomSince,
		RoomProvider:     p.RoomProvider,
		RoomSource:       p.RoomSource,
		RoomConflict:     p.roomConflict,
		RoomObservations: sortedRoomObservations(p.roomObservations),
	}, true
}

// EntityIDs returns a copy of the tracked entity IDs. This is used to
// auto-merge person entities into the state watcher's entity filter
// globs so that person state changes are delivered regardless of the
// user's subscribe.entity_globs configuration. The returned slice is
// a defensive copy; callers cannot mutate internal state.
func (t *PresenceTracker) EntityIDs() []string {
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
