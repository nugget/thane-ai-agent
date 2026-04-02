package router

import (
	"fmt"
	"strconv"
	"strings"
)

// LoopSeed captures the common routing and configuration parameters
// shared by all agent wake sites. It is serializable via both YAML
// (for config file embedding) and JSON (for API and tool payloads).
//
// Each field maps to a well-known routing hint or agent.Request
// property. Zero-value fields are omitted during serialization and
// ignored by [LoopSeed.Hints].
type LoopSeed struct {
	// Model sets an explicit model, bypassing the router. When empty,
	// the router selects based on the other hint fields.
	Model string `yaml:"model,omitempty" json:"model,omitempty"`

	// QualityFloor is the minimum model quality rating (1–10). Maps
	// to [HintQualityFloor].
	QualityFloor string `yaml:"quality_floor,omitempty" json:"quality_floor,omitempty"`

	// Mission describes the task context for routing. Maps to
	// [HintMission]. Common values: "conversation", "automation",
	// "device_control", "background".
	Mission string `yaml:"mission,omitempty" json:"mission,omitempty"`

	// LocalOnly restricts routing to free/local models when "true".
	// Maps to [HintLocalOnly].
	LocalOnly string `yaml:"local_only,omitempty" json:"local_only,omitempty"`

	// DelegationGating controls delegation-first tool gating. Set to
	// "disabled" for direct tool access. Maps to [HintDelegationGating].
	DelegationGating string `yaml:"delegation_gating,omitempty" json:"delegation_gating,omitempty"`

	// PreferSpeed favours faster models when "true". Maps to
	// [HintPreferSpeed].
	PreferSpeed string `yaml:"prefer_speed,omitempty" json:"prefer_speed,omitempty"`

	// ExcludeTools lists tool names to filter out of the agent run.
	ExcludeTools []string `yaml:"exclude_tools,omitempty" json:"exclude_tools,omitempty"`

	// SeedTags lists capability tags to activate at the start of the
	// agent run.
	SeedTags []string `yaml:"seed_tags,omitempty" json:"seed_tags,omitempty"`

	// ExtraHints carries arbitrary key-value routing hints that are
	// merged last, allowing callers to override or extend the typed
	// fields above.
	ExtraHints map[string]string `yaml:"extra_hints,omitempty" json:"extra_hints,omitempty"`

	// Instructions is extra text injected into the user message to
	// guide the agent's behaviour for this wake context.
	Instructions string `yaml:"instructions,omitempty" json:"instructions,omitempty"`
}

// Hints builds a routing hints map from the seed's typed fields.
// Only non-empty fields are included. ExtraHints are merged last and
// can override typed fields.
func (s *LoopSeed) Hints() map[string]string {
	h := make(map[string]string)

	if s.QualityFloor != "" {
		h[HintQualityFloor] = s.QualityFloor
	}
	if s.Mission != "" {
		h[HintMission] = s.Mission
	}
	if s.LocalOnly != "" {
		h[HintLocalOnly] = s.LocalOnly
	}
	if s.DelegationGating != "" {
		h[HintDelegationGating] = s.DelegationGating
	}
	if s.PreferSpeed != "" {
		h[HintPreferSpeed] = s.PreferSpeed
	}

	for k, v := range s.ExtraHints {
		h[k] = v
	}

	return h
}

// validBoolHints is the set of accepted values for boolean-style hint
// fields (LocalOnly, PreferSpeed). Empty string is always accepted
// (omitted field).
var validBoolHints = map[string]bool{
	"":      true,
	"true":  true,
	"false": true,
}

// validMissions is the set of known mission values. Empty is accepted.
var validMissions = map[string]bool{
	"":               true,
	"conversation":   true,
	"automation":     true,
	"device_control": true,
	"background":     true,
	"metacognitive":  true,
}

// validDelegationGating is the set of known delegation gating modes.
var validDelegationGating = map[string]bool{
	"":         true,
	"enabled":  true,
	"disabled": true,
}

// Validate checks that the seed's typed fields contain semantically
// valid values. It does not require any field to be set — an empty
// LoopSeed is valid. Returns nil on success.
func (s *LoopSeed) Validate() error {
	if s.QualityFloor != "" {
		n, err := strconv.Atoi(s.QualityFloor)
		if err != nil || n < 1 || n > 10 {
			return fmt.Errorf("quality_floor must be an integer 1–10, got %q", s.QualityFloor)
		}
	}
	if !validMissions[s.Mission] {
		return fmt.Errorf("mission must be one of conversation, automation, device_control, background, metacognitive; got %q", s.Mission)
	}
	if !validBoolHints[s.LocalOnly] {
		return fmt.Errorf("local_only must be \"true\" or \"false\", got %q", s.LocalOnly)
	}
	if !validDelegationGating[s.DelegationGating] {
		return fmt.Errorf("delegation_gating must be \"enabled\" or \"disabled\", got %q", s.DelegationGating)
	}
	if !validBoolHints[s.PreferSpeed] {
		return fmt.Errorf("prefer_speed must be \"true\" or \"false\", got %q", s.PreferSpeed)
	}
	return nil
}

// ValidateTopicFilter checks that an MQTT topic filter is syntactically
// valid per the MQTT v5 specification:
//   - Must not be empty
//   - '#' wildcard must be the last segment (and alone in its level)
//   - '+' wildcard must occupy an entire level
//   - No null characters (U+0000)
func ValidateTopicFilter(filter string) error {
	if filter == "" {
		return fmt.Errorf("topic filter must not be empty")
	}
	if strings.ContainsRune(filter, 0) {
		return fmt.Errorf("topic filter must not contain null characters")
	}

	parts := strings.Split(filter, "/")
	for i, p := range parts {
		if strings.Contains(p, "#") {
			if p != "#" {
				return fmt.Errorf("'#' wildcard must occupy an entire level, got %q in segment %d", p, i)
			}
			if i != len(parts)-1 {
				return fmt.Errorf("'#' wildcard must be the last segment, found at segment %d of %d", i, len(parts))
			}
		}
		if strings.Contains(p, "+") && p != "+" {
			return fmt.Errorf("'+' wildcard must occupy an entire level, got %q in segment %d", p, i)
		}
	}
	return nil
}
