package contextfmt

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// isSentinelState reports whether the given state is one of the Home
// Assistant sentinel values that indicate the entity is not currently
// reporting a real state.
func isSentinelState(state string) bool {
	return state == "unavailable" || state == "unknown"
}

// IsSentinelState reports whether the given state is one of the Home
// Assistant sentinel values that indicate the entity is not currently
// reporting a real state.
func IsSentinelState(state string) bool {
	return isSentinelState(state)
}

// unavailableContext is the JSON structure for entities in a sentinel
// state. The shape deliberately omits any "state" field so the model
// cannot misread a sentinel string as a domain value (e.g. "unknown"
// being interpreted as a binary_sensor reading). Identity fields
// (name, device_class) are preserved so the model still knows what
// went offline.
type unavailableContext struct {
	Entity           string `json:"entity"`
	Name             string `json:"name,omitempty"`
	Available        bool   `json:"available"`
	Reason           string `json:"reason"`
	UnavailableSince string `json:"unavailable_since,omitempty"`
	DeviceClass      string `json:"device_class,omitempty"`
}

func formatUnavailable(state *homeassistant.State, now time.Time) string {
	uc := unavailableContext{
		Entity:      state.EntityID,
		Available:   false,
		Reason:      state.State, // "unavailable" or "unknown"
		DeviceClass: attrString(state.Attributes, "device_class"),
	}
	if name, ok := state.Attributes["friendly_name"].(string); ok && name != "" {
		uc.Name = name
	}
	if !state.LastChanged.IsZero() {
		uc.UnavailableSince = promptfmt.FormatDeltaOnly(state.LastChanged, now)
	}
	return promptfmt.MarshalCompact(uc)
}

// formatFetchError produces the structured availability payload for an
// entity whose state could not be fetched at all. Mirrors the shape of
// formatUnavailable so the model sees a consistent schema regardless
// of whether HA returned a sentinel state or the request itself failed.
func formatFetchError(entityID string) string {
	return promptfmt.MarshalCompact(unavailableContext{
		Entity:    entityID,
		Available: false,
		Reason:    "fetch_error",
	})
}

// FormatFetchError renders the structured availability payload for an entity
// whose state could not be fetched at all.
func FormatFetchError(entityID string) string {
	return formatFetchError(entityID)
}
