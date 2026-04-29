package awareness

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// lockContext is the JSON structure for lock entity context. Lock
// state is already semantic (locked/unlocked/locking/unlocking/jammed)
// so no translation is needed, but battery level matters: a smart
// lock at 8% is a chore that the model should surface proactively,
// and "jammed" is critical context that should not be confused with
// "unlocked".
type lockContext struct {
	Entity       string `json:"entity"`
	Name         string `json:"name,omitempty"`
	State        string `json:"state"`
	Battery      any    `json:"battery,omitempty"`
	AssumedState bool   `json:"assumed_state,omitempty"`
	Since        string `json:"since"`
}

func formatLock(state *homeassistant.State, now time.Time) string {
	lc := lockContext{
		Entity:       state.EntityID,
		State:        state.State,
		Battery:      roundAttr(state.Attributes["battery_level"], 0),
		AssumedState: attrBool(state.Attributes, "assumed_state"),
		Since:        promptfmt.FormatDeltaOnly(state.LastChanged, now),
	}
	if name, ok := state.Attributes["friendly_name"].(string); ok && name != "" {
		lc.Name = name
	}
	return promptfmt.MarshalCompact(lc)
}
