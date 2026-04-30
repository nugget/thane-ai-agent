package entitycontext

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// coverContext is the JSON structure for cover entity context.
// Cover state is already semantic (open/closed/opening/closing/stopped)
// but current_position carries information that the bare state hides:
// a blind at "open" with position 30 is meaningfully different from
// fully open. Tilt position is included for venetian-style covers.
type coverContext struct {
	Entity       string `json:"entity"`
	Name         string `json:"name,omitempty"`
	State        string `json:"state"`
	DeviceClass  string `json:"device_class,omitempty"`
	Position     any    `json:"position,omitempty"`
	Tilt         any    `json:"tilt_position,omitempty"`
	AssumedState bool   `json:"assumed_state,omitempty"`
	Since        string `json:"since"`
}

func formatCover(state *homeassistant.State, now time.Time) string {
	cc := coverContext{
		Entity:       state.EntityID,
		State:        state.State,
		DeviceClass:  attrString(state.Attributes, "device_class"),
		Position:     roundAttr(state.Attributes["current_position"], 0),
		Tilt:         roundAttr(state.Attributes["current_tilt_position"], 0),
		AssumedState: attrBool(state.Attributes, "assumed_state"),
		Since:        promptfmt.FormatDeltaOnly(state.LastChanged, now),
	}
	if name, ok := state.Attributes["friendly_name"].(string); ok && name != "" {
		cc.Name = name
	}
	return promptfmt.MarshalCompact(cc)
}
