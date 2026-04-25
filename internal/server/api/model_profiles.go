package api

import (
	"fmt"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/model/router"
)

func normalizeModelSelection(rawModel string, hints map[string]string, premiumFloor string, logger *slog.Logger) (string, map[string]string, string) {
	selection := router.ResolveVirtualModelSelection(rawModel, hints, router.VirtualModelRuntime{
		PremiumQualityFloor: premiumFloor,
	}, logger)
	return selection.Model, selection.Hints, ""
}

func premiumQualityFloor(rtr *router.Router) string {
	if rtr == nil {
		return "10"
	}
	return fmt.Sprintf("%d", rtr.MaxQuality())
}
