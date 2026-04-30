package contextfmt

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// fanContext is the JSON structure for fan entities. Fans expose a
// percentage (typically 0-100), preset_mode (e.g. eco/sleep/turbo),
// direction (forward/reverse for ceiling fans), and oscillating.
// When the fan is off, those running attributes describe the last
// on-state and are omitted to avoid misleading the model about
// current behavior.
type fanContext struct {
	Entity       string `json:"entity"`
	Name         string `json:"name,omitempty"`
	State        string `json:"state"`
	Percentage   any    `json:"percentage,omitempty"`
	PresetMode   string `json:"preset_mode,omitempty"`
	Direction    string `json:"direction,omitempty"`
	Oscillating  bool   `json:"oscillating,omitempty"`
	AssumedState bool   `json:"assumed_state,omitempty"`
	Since        string `json:"since"`
}

func formatFan(state *homeassistant.State, now time.Time) string {
	fc := fanContext{
		Entity:       state.EntityID,
		State:        state.State,
		AssumedState: attrBool(state.Attributes, "assumed_state"),
		Since:        promptfmt.FormatDeltaOnly(state.LastChanged, now),
	}
	if name, ok := state.Attributes["friendly_name"].(string); ok && name != "" {
		fc.Name = name
	}
	if state.State == "on" {
		fc.Percentage = roundAttr(state.Attributes["percentage"], 0)
		fc.PresetMode = attrString(state.Attributes, "preset_mode")
		fc.Direction = attrString(state.Attributes, "direction")
		fc.Oscillating = attrBool(state.Attributes, "oscillating")
	}
	return promptfmt.MarshalCompact(fc)
}
