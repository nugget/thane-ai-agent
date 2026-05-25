package api

import (
	"fmt"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/model/router"
)

// normalizeModelSelection resolves a virtual model name into its
// concrete model + routing factors + delegation_gating + system
// prompt. The systemPrompt return is reserved for future virtual
// models that want to inject prompt scaffolding; today it is always
// empty.
func normalizeModelSelection(rawModel string, hints map[string]string, premiumFloor string, logger *slog.Logger) (model string, factors map[string]string, delegationGating string, systemPrompt string) {
	selection := router.ResolveVirtualModelSelection(rawModel, hints, router.VirtualModelRuntime{
		PremiumQualityFloor: premiumFloor,
	}, logger)
	return selection.Model, selection.RoutingFactors, selection.DelegationGating, ""
}

func premiumQualityFloor(rtr *router.Router) string {
	if rtr == nil {
		return "10"
	}
	return fmt.Sprintf("%d", rtr.MaxQuality())
}
