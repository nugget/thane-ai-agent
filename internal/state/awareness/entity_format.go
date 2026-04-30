package awareness

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant/entitycontext"
)

func formatEntityContext(state *homeassistant.State, now time.Time) string {
	return entitycontext.Format(state, now)
}

func formatFetchError(entityID string) string {
	return entitycontext.FormatFetchError(entityID)
}

func isSentinelState(state string) bool {
	return entitycontext.IsSentinelState(state)
}

func entityDomain(entityID string) string {
	return entitycontext.EntityDomain(entityID)
}

func attrString(attrs map[string]any, key string) string {
	return entitycontext.AttrString(attrs, key)
}

func semanticState(domain, deviceClass, state string) string {
	return entitycontext.SemanticState(domain, deviceClass, state)
}

func normalizeBrightness(v any) any {
	return entitycontext.NormalizeBrightness(v)
}

func statePrecision(deviceClass string) int {
	return entitycontext.StatePrecision(deviceClass)
}

func roundFloat(f float64, places int) string {
	return entitycontext.RoundFloat(f, places)
}

func hasActiveMedia(state string) bool {
	return entitycontext.HasActiveMedia(state)
}
