package awareness

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant/contextfmt"
)

func formatEntityContext(state *homeassistant.State, now time.Time) string {
	return contextfmt.Format(state, now)
}

func formatFetchError(entityID string) string {
	return contextfmt.FormatFetchError(entityID)
}

func isSentinelState(state string) bool {
	return contextfmt.IsSentinelState(state)
}

func entityDomain(entityID string) string {
	return contextfmt.EntityDomain(entityID)
}

func attrString(attrs map[string]any, key string) string {
	return contextfmt.AttrString(attrs, key)
}

func semanticState(domain, deviceClass, state string) string {
	return contextfmt.SemanticState(domain, deviceClass, state)
}

func normalizeBrightness(v any) any {
	return contextfmt.NormalizeBrightness(v)
}

func statePrecision(deviceClass string) int {
	return contextfmt.StatePrecision(deviceClass)
}

func roundFloat(f float64, places int) string {
	return contextfmt.RoundFloat(f, places)
}

func hasActiveMedia(state string) bool {
	return contextfmt.HasActiveMedia(state)
}
