package awareness

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// updateContext is the JSON structure for update entities (the
// dedicated update domain that replaced the binary_sensor:update
// device_class). State is translated from on/off into semantic
// labels — "update_available" / "up_to_date" — for the same reason
// binary_sensor states are translated: the model should not have
// to know that on means "yes, an update is waiting".
type updateContext struct {
	Entity           string `json:"entity"`
	Name             string `json:"name,omitempty"`
	State            string `json:"state"`
	InstalledVersion string `json:"installed_version,omitempty"`
	LatestVersion    string `json:"latest_version,omitempty"`
	InProgress       bool   `json:"in_progress,omitempty"`
	Since            string `json:"since"`
}

func formatUpdate(state *homeassistant.State, now time.Time) string {
	uc := updateContext{
		Entity:           state.EntityID,
		State:            updateStateLabel(state.State),
		InstalledVersion: attrString(state.Attributes, "installed_version"),
		LatestVersion:    attrString(state.Attributes, "latest_version"),
		InProgress:       attrBool(state.Attributes, "in_progress"),
		Since:            promptfmt.FormatDeltaOnly(state.LastChanged, now),
	}
	if name, ok := state.Attributes["friendly_name"].(string); ok && name != "" {
		uc.Name = name
	}
	return promptfmt.MarshalCompact(uc)
}

func updateStateLabel(raw string) string {
	switch raw {
	case "on":
		return "update_available"
	case "off":
		return "up_to_date"
	}
	return raw
}
