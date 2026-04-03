package api

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/openclaw"
	"github.com/nugget/thane-ai-agent/internal/router"
)

func normalizeModelSelection(rawModel string, hints map[string]string, premiumFloor string, openClawCfg *config.OpenClawConfig, logger *slog.Logger) (string, map[string]string, string) {
	if logger == nil {
		logger = slog.Default()
	}
	if premiumFloor == "" {
		premiumFloor = "10"
	}

	outHints := make(map[string]string, len(hints)+4)
	for k, v := range hints {
		outHints[k] = v
	}

	model := strings.TrimSpace(rawModel)
	systemPrompt := ""

	switch model {
	case "", "thane", "thane:latest":
		model = ""
	case "thane:trigger":
		model = ""
		outHints[router.HintLocalOnly] = "true"
		outHints[router.HintQualityFloor] = "1"
		outHints[router.HintMission] = "automation"
	case "thane:command":
		model = ""
		outHints[router.HintMission] = "device_control"
	case "thane:premium":
		model = ""
		outHints[router.HintQualityFloor] = premiumFloor
	case "thane:ops":
		model = ""
		outHints[router.HintQualityFloor] = premiumFloor
		outHints[router.HintDelegationGating] = "disabled"
	case "thane:peer":
		model = ""
		outHints[router.HintMission] = "conversation"
	case "thane:local":
		model = ""
		outHints[router.HintQualityFloor] = "1"
		outHints[router.HintModelPreference] = ""
		outHints[router.HintLocalOnly] = "true"
	case "thane:openclaw":
		model = ""
		if openClawCfg != nil {
			outHints[router.HintQualityFloor] = premiumFloor
			outHints[router.HintMission] = "openclaw"
			if prompt, err := openclaw.BuildSystemPrompt(openClawCfg, false); err == nil {
				systemPrompt = prompt
			} else {
				logger.Warn("openclaw prompt build failed, falling back to default", "error", err)
			}
		} else {
			logger.Warn("thane:openclaw requested but openclaw config not set, using default routing")
		}
	case "thane:thinking":
		logger.Warn("deprecated profile, use thane:premium", "profile", model)
		model = ""
		outHints[router.HintQualityFloor] = premiumFloor
	case "thane:balanced":
		logger.Warn("deprecated profile, use thane:latest", "profile", model)
		model = ""
	case "thane:fast":
		logger.Warn("deprecated profile, use thane:command", "profile", model)
		model = ""
		outHints[router.HintMission] = "device_control"
	case "thane:homeassistant":
		logger.Warn("deprecated profile, use thane:command", "profile", model)
		model = ""
		outHints[router.HintMission] = "device_control"
	default:
		if strings.HasPrefix(model, "thane:") {
			logger.Warn("unknown thane profile, using default routing", "profile", model)
			model = ""
		}
	}

	return model, outHints, systemPrompt
}

func premiumQualityFloor(rtr *router.Router) string {
	if rtr == nil {
		return "10"
	}
	return fmt.Sprintf("%d", rtr.MaxQuality())
}
