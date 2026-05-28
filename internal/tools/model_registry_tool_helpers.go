package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	routepkg "github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

func matchesResourceQuery(res fleet.RegistryResourceSnapshot, query string) bool {
	return mrContainsFold(res.ID, query) ||
		mrContainsFold(res.Provider, query) ||
		mrContainsFold(res.URL, query) ||
		mrContainsFold(res.PolicyReason, query) ||
		mrContainsFold(res.LastError, query)
}

func matchesDeploymentQuery(dep fleet.RegistryDeploymentSnapshot, query string) bool {
	return mrContainsFold(dep.ID, query) ||
		mrContainsFold(dep.Model, query) ||
		mrContainsFold(dep.Provider, query) ||
		mrContainsFold(dep.Resource, query) ||
		mrContainsFold(dep.Source, query) ||
		mrContainsFold(dep.ModelType, query) ||
		mrContainsFold(dep.Publisher, query) ||
		mrContainsFold(dep.Family, query) ||
		mrContainsFold(dep.PolicyReason, query) ||
		mrContainsFold(dep.ResourcePolicyReason, query)
}

func mrContainsFold(value any, query string) bool {
	if query == "" {
		return true
	}
	return strings.Contains(strings.ToLower(fmt.Sprint(value)), query)
}

func mrSortedResourceHealthKeys(in map[string]routepkg.ResourceHealth) []string {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func mrMarshalToolJSON(v any) (string, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func mrExtractRouteHints(args map[string]any) map[string]string {
	hints := make(map[string]string)
	if raw, ok := args["hints"].(map[string]any); ok {
		for key, value := range raw {
			key = strings.TrimSpace(key)
			if key == "" || value == nil {
				continue
			}
			hints[key] = fmt.Sprint(value)
		}
	}
	if mission := strings.TrimSpace(toolargs.String(args, "mission")); mission != "" {
		hints[routepkg.FactorMission] = mission
	}
	if channel := strings.TrimSpace(toolargs.String(args, "channel")); channel != "" {
		hints[routepkg.FactorChannel] = channel
	}
	if pref := strings.TrimSpace(toolargs.String(args, "model_preference")); pref != "" {
		hints[routepkg.FactorModelPreference] = pref
	}
	if v, ok := toolargs.IntOK(args, "quality_floor"); ok && v > 0 {
		hints[routepkg.FactorQualityFloor] = strconv.Itoa(v)
	}
	if v, ok := toolargs.BoolOK(args, "local_only"); ok {
		hints[routepkg.FactorLocalOnly] = strconv.FormatBool(v)
	}
	if v, ok := toolargs.BoolOK(args, "prefer_speed"); ok {
		hints[routepkg.FactorPreferSpeed] = strconv.FormatBool(v)
	}
	if len(hints) == 0 {
		return nil
	}
	return hints
}

func mrParseRoutePriority(raw string) routepkg.Priority {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "background":
		return routepkg.PriorityBackground
	default:
		return routepkg.PriorityInteractive
	}
}

func mrPriorityString(p routepkg.Priority) string {
	if p == routepkg.PriorityBackground {
		return "background"
	}
	return "interactive"
}
