package contextfmt

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// alarmContext is the JSON structure for alarm_control_panel context.
// Panel state is already semantic (disarmed, armed_home, armed_away,
// armed_night, armed_vacation, arming, pending, triggered), so no
// translation is needed — the curation is the attributes a security
// decision actually wants: who changed it last, and whether disarming
// needs a code (the model should never suggest an arm/disarm flow the
// panel will refuse). Attribute names per the current HA
// alarm_control_panel documentation.
type alarmContext struct {
	Entity          string `json:"entity"`
	Name            string `json:"name,omitempty"`
	State           string `json:"state"`
	ChangedBy       string `json:"changed_by,omitempty"`
	CodeArmRequired bool   `json:"code_arm_required,omitempty"`
	Since           string `json:"since"`
}

func formatAlarmControlPanel(state *homeassistant.State, now time.Time) string {
	ac := alarmContext{
		Entity:          state.EntityID,
		State:           state.State,
		ChangedBy:       attrString(state.Attributes, "changed_by"),
		CodeArmRequired: attrBool(state.Attributes, "code_arm_required"),
		Since:           promptfmt.FormatDeltaOnly(state.LastChanged, now),
	}
	if name, ok := state.Attributes["friendly_name"].(string); ok && name != "" {
		ac.Name = name
	}
	return promptfmt.MarshalCompact(ac)
}
