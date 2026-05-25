package api

import (
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/model/router"
)

// normalizeModelSelection resolves a virtual model name into its
// concrete model + routing factors + delegation_gating + system
// prompt. The systemPrompt return is reserved for future virtual
// models that want to inject prompt scaffolding; today it is always
// empty.
func normalizeModelSelection(rawModel string, hints map[string]string, premiumFloor int, logger *slog.Logger) (model string, factors map[string]string, delegationGating string, systemPrompt string) {
	selection := router.ResolveVirtualModelSelection(rawModel, hints, router.VirtualModelRuntime{
		PremiumQualityFloor: premiumFloor,
	}, logger)
	return selection.Model, selection.RoutingFactors, selection.DelegationGating, ""
}

// premiumQualityFloor returns the QualityFloor stamped on premium /
// ops virtual models. Falls back to 10 when no router is wired
// (test paths), so the result is always a usable int.
func premiumQualityFloor(rtr *router.Router) int {
	if rtr == nil {
		return 10
	}
	return rtr.MaxQuality()
}
