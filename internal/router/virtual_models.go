package router

import (
	"log/slog"
	"strings"
)

const (
	// HintVirtualModel records the canonical virtual model / execution policy
	// selected by a higher-level entrypoint such as the Ollama-compatible API.
	HintVirtualModel = "virtual_model"

	// HintDelegateModel is an explicit delegate model override carried through
	// request hints. When set, delegate loops bypass router selection.
	HintDelegateModel = "delegate_model"
)

const delegateHintPrefix = "delegate_"

// VirtualModelRuntime contains runtime-derived values that influence virtual
// model expansion. The shape is intentionally small today but is designed to
// grow as the live model registry contributes more dynamic overlay state.
type VirtualModelRuntime struct {
	PremiumQualityFloor string
}

// VirtualModel describes an end-to-end execution policy exposed through
// virtual model names such as "thane:premium".
//
// TopLevel controls the initial orchestrator loop. Delegate controls child
// loops launched via thane_delegate. Future revisions may also derive these
// policies dynamically from the live registry without changing callers.
type VirtualModel struct {
	Name        string
	Description string
	Exposed     bool
	Aliases     []string
	TopLevel    LoopProfile
	Delegate    LoopProfile
}

// VirtualModelSelection is the resolved effect of a caller-supplied model
// string after virtual model expansion.
type VirtualModelSelection struct {
	RequestedName string
	CanonicalName string
	Description   string
	Known         bool
	Model         string
	Hints         map[string]string
}

// DelegateHintKey returns the request-hint key used to carry a delegate-time
// override for the supplied routing hint.
func DelegateHintKey(hint string) string {
	return delegateHintPrefix + hint
}

// OverlayDelegateHints merges delegate-policy overrides carried in a parent
// request-hint map onto a delegate profile's base router hints.
//
// The returned explicitModel, when non-empty, instructs the delegate to bypass
// router selection and use that exact model.
func OverlayDelegateHints(base map[string]string, inherited map[string]string) (explicitModel string, merged map[string]string) {
	merged = make(map[string]string, len(base))
	for k, v := range base {
		merged[k] = v
	}
	if len(inherited) == 0 {
		return "", merged
	}

	explicitModel = strings.TrimSpace(inherited[HintDelegateModel])
	for _, hint := range []string{
		HintQualityFloor,
		HintMission,
		HintLocalOnly,
		HintDelegationGating,
		HintPreferSpeed,
		HintModelPreference,
	} {
		if v, ok := inherited[DelegateHintKey(hint)]; ok {
			merged[hint] = strings.TrimSpace(v)
		}
	}

	return explicitModel, merged
}

// ExposedVirtualModels returns the runtime-expanded virtual models intended for
// user-facing discovery such as Ollama's /api/tags.
func ExposedVirtualModels(runtime VirtualModelRuntime) []VirtualModel {
	all := builtinVirtualModels(runtime)
	out := make([]VirtualModel, 0, len(all))
	for _, model := range all {
		if model.Exposed {
			out = append(out, model)
		}
	}
	return out
}

// ResolveVirtualModelSelection expands a caller-supplied model string into a
// canonical virtual model policy or preserves the explicit deployment name when
// no virtual model matched.
func ResolveVirtualModelSelection(rawModel string, baseHints map[string]string, runtime VirtualModelRuntime, logger *slog.Logger) VirtualModelSelection {
	if logger == nil {
		logger = slog.Default()
	}

	outHints := make(map[string]string, len(baseHints)+8)
	for k, v := range baseHints {
		outHints[k] = v
	}

	modelName := strings.TrimSpace(rawModel)
	all := builtinVirtualModels(runtime)
	index := make(map[string]VirtualModel, len(all)*2)
	for _, model := range all {
		index[model.Name] = model
		for _, alias := range model.Aliases {
			index[alias] = model
		}
	}

	spec, ok := index[modelName]
	if modelName == "" {
		spec, ok = index["thane:latest"]
	}
	if !ok {
		if strings.HasPrefix(modelName, "thane:") {
			logger.Warn("unknown thane profile, using default routing", "profile", modelName)
			return VirtualModelSelection{
				RequestedName: rawModel,
				Known:         false,
				Model:         "",
				Hints:         outHints,
			}
		}
		return VirtualModelSelection{
			RequestedName: rawModel,
			Known:         false,
			Model:         modelName,
			Hints:         outHints,
		}
	}

	if modelName != "" && modelName != "thane" && modelName != spec.Name {
		logger.Warn("virtual model alias used", "alias", modelName, "canonical", spec.Name)
	}

	for k, v := range spec.TopLevel.Hints() {
		outHints[k] = v
	}
	for k, v := range spec.Delegate.Hints() {
		outHints[DelegateHintKey(k)] = v
	}
	if spec.Delegate.Model != "" {
		outHints[HintDelegateModel] = spec.Delegate.Model
	}
	outHints[HintVirtualModel] = spec.Name

	return VirtualModelSelection{
		RequestedName: rawModel,
		CanonicalName: spec.Name,
		Description:   spec.Description,
		Known:         true,
		Model:         spec.TopLevel.Model,
		Hints:         outHints,
	}
}

func builtinVirtualModels(runtime VirtualModelRuntime) []VirtualModel {
	premiumFloor := strings.TrimSpace(runtime.PremiumQualityFloor)
	if premiumFloor == "" {
		premiumFloor = "10"
	}

	return []VirtualModel{
		{
			Name:        "thane:latest",
			Description: "adaptive general assistant (default routing)",
			Exposed:     true,
			Aliases:     []string{"thane", "thane:balanced"},
		},
		{
			Name:        "thane:premium",
			Description: "frontier-first assistant (frontier orchestrator, adaptive delegates)",
			Exposed:     true,
			Aliases:     []string{"thane:best", "thane:thinking"},
			TopLevel: LoopProfile{
				QualityFloor: premiumFloor,
				LocalOnly:    "false",
				PreferSpeed:  "false",
			},
		},
		{
			Name:        "thane:ops",
			Description: "frontier operator mode (direct tools, frontier delegates)",
			Exposed:     true,
			TopLevel: LoopProfile{
				QualityFloor:     premiumFloor,
				LocalOnly:        "false",
				PreferSpeed:      "false",
				DelegationGating: "disabled",
			},
			Delegate: LoopProfile{
				QualityFloor: premiumFloor,
				LocalOnly:    "false",
				PreferSpeed:  "false",
			},
		},
		{
			Name:        "thane:assist",
			Description: "fast local-first assistant for device control and voice-style requests",
			Exposed:     true,
			Aliases:     []string{"thane:command", "thane:homeassistant", "thane:fast"},
			TopLevel: LoopProfile{
				Mission:      "device_control",
				QualityFloor: "4",
				LocalOnly:    "true",
				PreferSpeed:  "true",
			},
			Delegate: LoopProfile{
				Mission:      "device_control",
				QualityFloor: "4",
				LocalOnly:    "true",
				PreferSpeed:  "true",
			},
		},
		{
			Name:        "thane:local",
			Description: "strictly local-only mode",
			Exposed:     true,
			TopLevel: LoopProfile{
				QualityFloor: "1",
				LocalOnly:    "true",
				PreferSpeed:  "true",
			},
			Delegate: LoopProfile{
				QualityFloor: "1",
				LocalOnly:    "true",
				PreferSpeed:  "true",
			},
		},
		{
			Name:        "thane:event",
			Description: "local automation and fire-and-forget events",
			Aliases:     []string{"thane:trigger"},
			TopLevel: LoopProfile{
				Mission:      "automation",
				QualityFloor: "1",
				LocalOnly:    "true",
				PreferSpeed:  "true",
			},
			Delegate: LoopProfile{
				Mission:      "automation",
				QualityFloor: "1",
				LocalOnly:    "true",
				PreferSpeed:  "true",
			},
		},
		{
			Name:        "thane:peer",
			Description: "agent-to-agent conversational work",
			Aliases:     nil,
			TopLevel: LoopProfile{
				Mission: "conversation",
			},
		},
	}
}
