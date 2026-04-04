package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/models"
	routepkg "github.com/nugget/thane-ai-agent/internal/router"
)

func matchesResourceQuery(res models.RegistryResourceSnapshot, query string) bool {
	return mrContainsFold(res.ID, query) ||
		mrContainsFold(res.Provider, query) ||
		mrContainsFold(res.URL, query) ||
		mrContainsFold(res.PolicyReason, query) ||
		mrContainsFold(res.LastError, query)
}

func matchesDeploymentQuery(dep models.RegistryDeploymentSnapshot, query string) bool {
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

func mrStringArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func mrIntArg(args map[string]any, key string, def int) int {
	if v, ok := mrIntArgOK(args, key); ok {
		return v
	}
	return def
}

func mrIntArgOK(args map[string]any, key string) (int, bool) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		if v != float64(int(v)) {
			return 0, false
		}
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func mrBoolArg(args map[string]any, key string) bool {
	v, _ := mrBoolArgOK(args, key)
	return v
}

func mrHasBoolArg(args map[string]any, key string) bool {
	_, ok := mrBoolArgOK(args, key)
	return ok
}

func mrBoolArgOK(args map[string]any, key string) (bool, bool) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return false, false
	}
	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
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
	if mission := strings.TrimSpace(mrStringArg(args, "mission")); mission != "" {
		hints[routepkg.HintMission] = mission
	}
	if channel := strings.TrimSpace(mrStringArg(args, "channel")); channel != "" {
		hints[routepkg.HintChannel] = channel
	}
	if pref := strings.TrimSpace(mrStringArg(args, "model_preference")); pref != "" {
		hints[routepkg.HintModelPreference] = pref
	}
	if v, ok := mrIntArgOK(args, "quality_floor"); ok && v > 0 {
		hints[routepkg.HintQualityFloor] = strconv.Itoa(v)
	}
	if v, ok := mrBoolArgOK(args, "local_only"); ok {
		hints[routepkg.HintLocalOnly] = strconv.FormatBool(v)
	}
	if v, ok := mrBoolArgOK(args, "prefer_speed"); ok {
		hints[routepkg.HintPreferSpeed] = strconv.FormatBool(v)
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
