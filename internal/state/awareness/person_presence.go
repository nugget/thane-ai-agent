package awareness

import (
	"encoding/json"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant/contextfmt"
)

// PersonPresenceFields is the presence tracker's view of a person,
// spliced onto the raw Home Assistant row when a subscription renders a
// person entity.
//
// It deliberately carries no state or timestamp. The watched row already
// holds authoritative live state read this turn, and overlaying the
// tracker's copy would let a stale room ride along with a fresh state.
// Only what HA cannot say appears here: who this is, and where in the
// house they are.
type PersonPresenceFields struct {
	Contact      string `json:"contact,omitempty"`
	ContactID    string `json:"contact_id,omitempty"`
	TrustZone    string `json:"trust_zone,omitempty"`
	IsOperator   bool   `json:"is_operator,omitempty"`
	Room         string `json:"room,omitempty"`
	RoomProvider string `json:"room_provider,omitempty"`
	RoomSource   string `json:"room_source,omitempty"`
	RoomConflict bool   `json:"room_conflict,omitempty"`
}

func (f PersonPresenceFields) empty() bool {
	return f == PersonPresenceFields{}
}

// PersonPresenceSource resolves a person entity to the tracker's view of
// it. The bool reports whether that person is tracked at all.
//
// A function value rather than a package dependency: the presence tracker
// is constructed after the providers that render its people, so this is
// bound to a closure that reads it at render time.
type PersonPresenceSource func(entityID string) (PersonPresenceFields, bool)

// attachPersonPresence splices presence onto a rendered person row.
//
// The splice is textual, mirroring [contextfmt.AttachMetadata], because
// round-tripping through map[string]any re-marshals with sorted keys and
// would scatter contact identity through the row — the presence block
// puts identity first on purpose, since the model reasons about people.
//
// Room is gated on the live state saying home, and dropped when the
// tracker reports conflicting observations. The tracker clears rooms only
// on a literal "not_home", so a person sitting in a named zone can still
// hold a room they left hours ago; the live row is the authority on
// whether they are home to have one.
func attachPersonPresence(formatted, haState string, fields PersonPresenceFields) string {
	if !strings.EqualFold(haState, "home") || fields.RoomConflict {
		fields.Room, fields.RoomProvider, fields.RoomSource = "", "", ""
	}
	if !strings.EqualFold(haState, "home") {
		fields.RoomConflict = false
	}
	if fields.empty() {
		return formatted
	}

	trimmed := strings.TrimSpace(formatted)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return formatted
	}
	if !json.Valid([]byte(trimmed)) {
		return formatted
	}
	encoded, err := json.Marshal(fields)
	if err != nil || len(encoded) < 2 {
		return formatted
	}
	// Strip the object braces and splice the members in, so the base
	// row's own key order survives.
	members := string(encoded[1 : len(encoded)-1])
	if members == "" {
		return formatted
	}
	if trimmed == "{}" {
		return "{" + members + "}"
	}
	return trimmed[:len(trimmed)-1] + "," + members + "}"
}

// enrichPersonPresence adds tracker presence to a person row. Non-person
// entities, untracked people, and an unwired tracker all pass through.
func enrichPersonPresence(formatted string, state *homeassistant.State, presence PersonPresenceSource) string {
	if presence == nil || state == nil || contextfmt.EntityDomain(state.EntityID) != "person" {
		return formatted
	}
	fields, ok := presence(state.EntityID)
	if !ok {
		return formatted
	}
	return attachPersonPresence(formatted, state.State, fields)
}

// SubscriptionOption configures optional behaviour shared by the entity
// subscription providers.
//
// A construction option rather than a setter: presence enrichment is
// wiring, fixed for the provider's life, and the architecture baseline
// counts exported mutator growth deliberately. Variadic, so existing
// call sites are untouched.
type SubscriptionOption func(*subscriptionOptions)

type subscriptionOptions struct {
	presence PersonPresenceSource
}

func newSubscriptionOptions(opts []SubscriptionOption) subscriptionOptions {
	var resolved subscriptionOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&resolved)
		}
	}
	return resolved
}

// WithPersonPresence enriches rendered person entities with the presence
// tracker's contact-rooted view: who the person is, and which room they
// are in while home. Without it a person renders as raw Home Assistant
// state, which cannot tell "at the mailbox" from "in another state".
func WithPersonPresence(source PersonPresenceSource) SubscriptionOption {
	return func(o *subscriptionOptions) { o.presence = source }
}
