package router

import (
	"strconv"
)

// LoopSeed is the germination point of an agent loop invocation. It
// bundles routing, context, and capability configuration into a
// reusable object that can be stored, serialized, and used to
// construct an agent.Request.
//
// Wake subscriptions, scheduled tasks, and channel bridges each store
// a LoopSeed describing how the resulting agent loop should be
// configured. Callers use [LoopSeed.Hints] to build the routing hints
// map, then construct the agent.Request with the seed's fields.
type LoopSeed struct {
	// Source identifies the originating subsystem ("wake", "signal",
	// "scheduler", "email", etc.). Appears in routing hints.
	Source string `json:"source"`

	// Mission describes the task context for the router: "conversation",
	// "device_control", "automation", "anticipation", "background".
	Mission string `json:"mission,omitempty"`

	// Model is a soft model preference (e.g., "claude-sonnet-4-20250514").
	// The router treats this as a suggestion, not an override.
	Model string `json:"model,omitempty"`

	// LocalOnly restricts routing to free/local models when true.
	// Nil uses the router default.
	LocalOnly *bool `json:"local_only,omitempty"`

	// QualityFloor is the minimum model quality rating (1-10).
	// Zero uses the router default.
	QualityFloor int `json:"quality_floor,omitempty"`

	// DelegationGating controls delegation-first tool gating.
	// "enabled" or "disabled"; empty uses the default.
	DelegationGating string `json:"delegation_gating,omitempty"`

	// ExcludeTools lists tool names to withhold from this invocation.
	ExcludeTools []string `json:"exclude_tools,omitempty"`

	// SeedTags are capability tags to pre-activate at loop start.
	SeedTags []string `json:"seed_tags,omitempty"`

	// KBRefs lists knowledge base articles to pre-load on wake.
	// Values are fact keys ("routine/motion_protocol") or
	// KB-relative file paths ("dossiers/dan.md").
	KBRefs []string `json:"kb_refs,omitempty"`

	// ContextEntities lists HA entity IDs to snapshot and inject
	// into the wake message (e.g., ["sensor.temperature", "light.living_room"]).
	ContextEntities []string `json:"context_entities,omitempty"`

	// Context is free-form instructions or reasoning to inject into
	// the wake message ("instructions you left for yourself").
	Context string `json:"context,omitempty"`

	// ExtraHints holds source-specific routing hints that don't have
	// dedicated fields (e.g., "sender", "task", "event_type").
	ExtraHints map[string]string `json:"extra_hints,omitempty"`
}

// Hints builds the complete routing hints map from the seed's fields.
func (s *LoopSeed) Hints() map[string]string {
	h := make(map[string]string, 8)

	if s.Source != "" {
		h["source"] = s.Source
	}
	if s.Mission != "" {
		h[HintMission] = s.Mission
	}
	if s.Model != "" {
		h[HintModelPreference] = s.Model
	}
	if s.LocalOnly != nil {
		if *s.LocalOnly {
			h[HintLocalOnly] = "true"
		} else {
			h[HintLocalOnly] = "false"
		}
	}
	if s.QualityFloor > 0 {
		h[HintQualityFloor] = strconv.Itoa(s.QualityFloor)
	}
	if s.DelegationGating != "" {
		h[HintDelegationGating] = s.DelegationGating
	}

	for k, v := range s.ExtraHints {
		h[k] = v
	}

	return h
}
