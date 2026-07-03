package contextfmt

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// eventContext is the JSON structure for event entity context (the
// 2026.7 event domain: doorbell rings, button presses, motion events).
// An event entity's raw state is the ISO timestamp of the last firing —
// exactly the shape the model-facing conventions say never to emit.
// The projection leads with what fired (event_type) and expresses when
// as the standard since delta; the raw timestamp never appears.
type eventContext struct {
	Entity      string `json:"entity"`
	Name        string `json:"name,omitempty"`
	Event       string `json:"event"`
	DeviceClass string `json:"device_class,omitempty"`
	Since       string `json:"since"`
}

func formatEvent(state *homeassistant.State, now time.Time) string {
	ec := eventContext{
		Entity:      state.EntityID,
		Event:       attrString(state.Attributes, "event_type"),
		DeviceClass: attrString(state.Attributes, "device_class"),
		Since:       promptfmt.FormatDeltaOnly(state.LastChanged, now),
	}
	// The state itself is the firing timestamp; when it parses and
	// LastChanged is missing or zero, derive the delta from it so the
	// "when" survives sparse inputs.
	if state.LastChanged.IsZero() {
		if fired, err := time.Parse(time.RFC3339Nano, state.State); err == nil {
			ec.Since = promptfmt.FormatDeltaOnly(fired, now)
		}
	}
	if name, ok := state.Attributes["friendly_name"].(string); ok && name != "" {
		ec.Name = name
	}
	return promptfmt.MarshalCompact(ec)
}
