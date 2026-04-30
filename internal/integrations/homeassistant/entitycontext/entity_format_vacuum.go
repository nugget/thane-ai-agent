package entitycontext

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// vacuumContext is the JSON structure for vacuum entities. Native HA
// vacuum states (cleaning, docked, idle, paused, returning, error)
// are already semantic; the model just needs them surfaced alongside
// battery and fan_speed so it can answer "is the robot working,
// stuck, or low?" without inferring from a state string alone.
type vacuumContext struct {
	Entity   string `json:"entity"`
	Name     string `json:"name,omitempty"`
	State    string `json:"state"`
	Battery  any    `json:"battery,omitempty"`
	FanSpeed string `json:"fan_speed,omitempty"`
	Status   string `json:"status,omitempty"`
	Since    string `json:"since"`
}

func formatVacuum(state *homeassistant.State, now time.Time) string {
	vc := vacuumContext{
		Entity:   state.EntityID,
		State:    state.State,
		Battery:  roundAttr(state.Attributes["battery_level"], 0),
		FanSpeed: attrString(state.Attributes, "fan_speed"),
		Status:   attrString(state.Attributes, "status"),
		Since:    promptfmt.FormatDeltaOnly(state.LastChanged, now),
	}
	if name, ok := state.Attributes["friendly_name"].(string); ok && name != "" {
		vc.Name = name
	}
	return promptfmt.MarshalCompact(vc)
}
