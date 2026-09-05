package contacts

import (
	"context"
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
	// Contact identity first: the model reasons about people, and
	// ContactID is what lets it chain into contact_whereabouts without
	// gambling on name resolution.
	Contact    string `json:"contact,omitempty"`
	ContactID  string `json:"contact_id,omitempty"`
	TrustZone  string `json:"trust_zone,omitempty"`
	IsOperator bool   `json:"is_operator,omitempty"`

	Entity string `json:"entity"`
	Name   string `json:"name"`
	State  string `json:"state"`
	// StateSince is when this person entity last changed state. While
	// away that is when they left home — it is not how long they have
	// been wherever they are now. It was named "since", which invited
	// exactly that misreading in an always-on block with no tool call to
	// blame and no hedge available.
	StateSince   string `json:"state_since,omitempty"`
	Room         string `json:"room,omitempty"`
	RoomProvider string `json:"room_provider,omitempty"`
	RoomSource   string `json:"room_source,omitempty"`
	RoomConflict bool   `json:"room_conflict,omitempty"`
}

// ContactIdentity is the contact a tracked person entity resolves to.
// The tracker holds no contact store; app wiring supplies a resolver so
// the projection can be contact-rooted without the dependency.
type ContactIdentity struct {
	ID         string
	Name       string
	TrustZone  string
	IsOperator bool
}

// PersonPresenceInput is the state of one tracked person.
//
// A struct rather than a parameter list: the previous signature took
// eight positional primitives, and contact identity would have made it
// twelve.
type PersonPresenceInput struct {
	EntityID     string
	Name         string
	State        string
	StateSince   time.Time
	Room         string
	RoomProvider string
	RoomSource   string
	RoomConflict bool
	Contact      *ContactIdentity
	Now          time.Time
}

// FormatPersonPresence formats a tracked person as compact JSON with
// delta-annotated timestamps.
func FormatPersonPresence(in PersonPresenceInput) string {
	displayState := in.State
	if strings.EqualFold(in.State, "not_home") {
		displayState = "away"
	}
	room, roomProvider, roomSource := in.Room, in.RoomProvider, in.RoomSource
	roomConflict := in.RoomConflict
	if !strings.EqualFold(in.State, "home") || roomConflict {
		room = ""
		roomProvider = ""
		roomSource = ""
	}
	if !strings.EqualFold(in.State, "home") {
		roomConflict = false
	}
	pc := PersonPresenceContext{
		Entity:       in.EntityID,
		Name:         in.Name,
		State:        displayState,
		Room:         room,
		RoomProvider: roomProvider,
		RoomSource:   roomSource,
		RoomConflict: roomConflict,
	}
	if !in.StateSince.IsZero() {
		pc.StateSince = promptfmt.FormatDeltaOnly(in.StateSince, in.Now)
	}
	if c := in.Contact; c != nil {
		pc.Contact = c.Name
		pc.ContactID = c.ID
		pc.TrustZone = c.TrustZone
		pc.IsOperator = c.IsOperator
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
	// lastUpdated prevents a reconnect snapshot fetched before a live HA event
	// from overwriting that newer event after slower registry I/O completes.
	lastUpdated time.Time
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

	// contactResolver maps a person entity to the contact it belongs to.
	// Supplied by app wiring; nil in tests and in deployments with no
	// contact store, where the block degrades to entity-only.
	contactResolver func(entityID string) (ContactIdentity, bool)

	linkedByPerson  map[string][]string
	ownersByTracker map[string][]string
	trackerPlatform map[string]string
	// trackerUpdated retains provider timestamps even after withdrawals so a
	// delayed home event cannot resurrect an older room claim.
	trackerUpdated map[string]time.Time
	ingestObserver func([]string)
	linkedObserver func([]string)

	mu     sync.RWMutex
	loc    *time.Location
	logger *slog.Logger
}

// PresenceOption configures a tracker at construction.
//
// An option rather than a setter: the resolver is wiring, fixed for the
// tracker's life, and the architecture baseline counts mutator growth
// deliberately. Variadic so the existing call sites are untouched.
type PresenceOption func(*PresenceTracker)

// WithContactResolver supplies the entity-to-contact projection that
// makes the presence block contact-rooted. Omitted, the block renders
// entity-only rather than inventing an identity.
func WithContactResolver(fn func(entityID string) (ContactIdentity, bool)) PresenceOption {
	return func(t *PresenceTracker) { t.contactResolver = fn }
}

// NewPresenceTracker creates a person tracker for the given entity IDs. All
// entities start in "Unknown" state until Initialize is called. The
// timezone is an IANA location string (e.g., "America/Chicago"); an
// empty or invalid timezone falls back to the system local timezone.
func NewPresenceTracker(entityIDs []string, timezone string, logger *slog.Logger, opts ...PresenceOption) *PresenceTracker {
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

	t := &PresenceTracker{
		people:          people,
		order:           order,
		linkedByPerson:  make(map[string][]string),
		ownersByTracker: make(map[string][]string),
		trackerPlatform: make(map[string]string),
		trackerUpdated:  make(map[string]time.Time),
		loc:             loc,
		logger:          logger,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
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

		in := PersonPresenceInput{
			EntityID:     p.EntityID,
			Name:         displayName,
			State:        p.State,
			StateSince:   p.Since,
			Room:         p.Room,
			RoomProvider: p.RoomProvider,
			RoomSource:   p.RoomSource,
			RoomConflict: p.roomConflict,
			Now:          now,
		}
		if t.contactResolver != nil {
			if c, ok := t.contactResolver(p.EntityID); ok {
				in.Contact = &c
			}
		}
		// Unknown renders as JSON too. It used to be a markdown bullet
		// carrying only a display name, so on the one turn the model most
		// needs a handle it had nothing to chain into.
		if p.State == "Unknown" || p.Since.IsZero() {
			in.State = "unknown"
			in.StateSince = time.Time{}
			in.Room, in.RoomProvider, in.RoomSource = "", "", ""
			in.RoomConflict = false
		}
		sb.WriteString(FormatPersonPresence(in))
		sb.WriteByte('\n')
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
