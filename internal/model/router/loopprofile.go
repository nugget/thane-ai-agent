package router

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// LoopProfile captures the common routing and configuration parameters
// shared by all agent wake sites. It is serializable via both YAML
// (for config file embedding) and JSON (for API and tool payloads).
//
// Each field maps to a well-known routing hint or agent.Request
// property. Zero-value fields are omitted during serialization and
// ignored by [LoopProfile.RoutingFactors].
type LoopProfile struct {
	// Model sets an explicit model, bypassing the router. When empty,
	// the router selects based on the other hint fields.
	Model string `yaml:"model,omitempty" json:"model,omitempty"`

	// QualityFloor is the minimum model quality rating (1–10).
	// Maps to [FactorQualityFloor]. Zero means "unset" — let the
	// router pick its own default.
	//
	// JSON/YAML accept both the canonical int form (`5`) and the
	// legacy stringified form (`"5"`) for backwards compatibility
	// with persisted overlay specs and operator YAML written
	// before the int conversion; see [LoopProfile.UnmarshalJSON].
	QualityFloor int `yaml:"quality_floor,omitempty" json:"quality_floor,omitempty"`

	// Mission describes the task context for routing. Maps to
	// [FactorMission]. Common values: "conversation", "automation",
	// "device_control", "background".
	Mission string `yaml:"mission,omitempty" json:"mission,omitempty"`

	// LocalOnly restricts routing to free/local models when "true".
	// Maps to [FactorLocalOnly].
	LocalOnly string `yaml:"local_only,omitempty" json:"local_only,omitempty"`

	// DelegationGating controls delegation-first tool gating. Set to
	// "disabled" for direct tool access. Typed field promoted out of
	// the routing factors map — it's a feature switch, not an input the
	// router scores on. Threaded through [RequestOptions.DelegationGating]
	// into the loop and agent request boundaries.
	DelegationGating string `yaml:"delegation_gating,omitempty" json:"delegation_gating,omitempty"`

	// PreferSpeed favours faster models when "true". Maps to
	// [FactorPreferSpeed].
	PreferSpeed string `yaml:"prefer_speed,omitempty" json:"prefer_speed,omitempty"`

	// ExcludeTools lists tool names to filter out of the agent run.
	ExcludeTools []string `yaml:"exclude_tools,omitempty" json:"exclude_tools,omitempty"`

	// ExtraHints carries arbitrary key-value routing hints that are
	// merged last, allowing callers to override or extend the typed
	// fields above.
	ExtraHints map[string]string `yaml:"extra_hints,omitempty" json:"extra_hints,omitempty"`

	// Instructions is extra text injected into the user message to
	// guide the agent's behaviour for this wake context.
	Instructions string `yaml:"instructions,omitempty" json:"instructions,omitempty"`
}

// loopProfileWire is the JSON wire representation used by
// [LoopProfile.UnmarshalJSON]. QualityFloor is decoded as
// [json.RawMessage] so both the canonical int form (`5`) and the
// legacy stringified form (`"5"`) survive the round-trip — needed
// for persisted overlay specs written before the int conversion.
type loopProfileWire struct {
	Model            string            `json:"model,omitempty"`
	QualityFloor     json.RawMessage   `json:"quality_floor,omitempty"`
	Mission          string            `json:"mission,omitempty"`
	LocalOnly        string            `json:"local_only,omitempty"`
	DelegationGating string            `json:"delegation_gating,omitempty"`
	PreferSpeed      string            `json:"prefer_speed,omitempty"`
	ExcludeTools     []string          `json:"exclude_tools,omitempty"`
	ExtraHints       map[string]string `json:"extra_hints,omitempty"`
	Instructions     string            `json:"instructions,omitempty"`
}

// UnmarshalJSON accepts the canonical int form for QualityFloor
// AND the legacy stringified form for backwards compatibility
// with persisted overlay specs / config blocks written before
// the field type changed. A missing or null QualityFloor decodes
// to 0 ("unset"). An empty string also decodes to 0. Anything
// else fails fast so a malformed value is loud rather than
// silently treated as zero.
func (s *LoopProfile) UnmarshalJSON(data []byte) error {
	var w loopProfileWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	*s = LoopProfile{
		Model:            w.Model,
		Mission:          w.Mission,
		LocalOnly:        w.LocalOnly,
		DelegationGating: w.DelegationGating,
		PreferSpeed:      w.PreferSpeed,
		ExcludeTools:     w.ExcludeTools,
		ExtraHints:       w.ExtraHints,
		Instructions:     w.Instructions,
	}
	if len(w.QualityFloor) == 0 || string(w.QualityFloor) == "null" {
		return nil
	}
	// Try the canonical int form first — succeeds on plain
	// integer JSON numbers like `5`. encoding/json rejects
	// non-integer numbers like `5.0` when targeting int, so we
	// don't pretend to accept them; if a config source produces
	// `5.0` we'd rather fail loud than silently truncate.
	var n int
	if err := json.Unmarshal(w.QualityFloor, &n); err == nil {
		s.QualityFloor = n
		return nil
	}
	// Fall back to the legacy stringified form. Empty string is
	// treated as unset (matches the pre-conversion behavior).
	var raw string
	if err := json.Unmarshal(w.QualityFloor, &raw); err != nil {
		return fmt.Errorf("quality_floor: must be an integer or string-of-integer, got %s", w.QualityFloor)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("quality_floor: %q is not a valid integer", raw)
	}
	s.QualityFloor = parsed
	return nil
}

// RequestOptions contains the agent request fields derived from a
// LoopProfile. Callers can merge additional channel- or trigger-specific
// factors on top of these shared routing defaults.
type RequestOptions struct {
	Model            string
	RoutingFactors   map[string]string
	ExcludeTools     []string
	DelegationGating string
}

// RoutingFactors builds the routing-factor map from the profile's
// typed fields. Only non-empty fields are included. ExtraHints
// are merged last and can override typed fields. QualityFloor is
// the only typed numeric field today; it's string-ified at this
// boundary so the wire format (`map[string]string`) the router
// consumes stays uniform.
func (s *LoopProfile) RoutingFactors() map[string]string {
	h := make(map[string]string)

	if s.QualityFloor > 0 {
		h[FactorQualityFloor] = strconv.Itoa(s.QualityFloor)
	}
	if s.Mission != "" {
		h[FactorMission] = s.Mission
	}
	if s.LocalOnly != "" {
		h[FactorLocalOnly] = s.LocalOnly
	}
	if s.PreferSpeed != "" {
		h[FactorPreferSpeed] = s.PreferSpeed
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

// missionPattern matches valid mission identifiers: lowercase ASCII
// letters, digits, and underscores. The router handles unknown missions
// gracefully (no special routing rules fire), so we validate format
// rather than restricting to a closed enum.
var missionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// validDelegationGating is the set of known delegation gating modes.
var validDelegationGating = map[string]bool{
	"":         true,
	"enabled":  true,
	"disabled": true,
}

// Validate checks that the profile's typed fields contain semantically
// valid values. It does not require any field to be set — an empty
// LoopProfile is valid. Returns nil on success.
func (s *LoopProfile) Validate() error {
	if s.QualityFloor != 0 {
		if s.QualityFloor < 1 || s.QualityFloor > 10 {
			return fmt.Errorf("quality_floor must be an integer 1–10, got %d", s.QualityFloor)
		}
	}
	if s.Mission != "" && !missionPattern.MatchString(s.Mission) {
		return fmt.Errorf("mission must be a lowercase identifier (letters, digits, underscores), got %q", s.Mission)
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

// RequestOptions returns the request-ready fields implied by the profile.
// Slices are copied so callers can mutate the result without affecting
// the underlying LoopProfile.
func (s *LoopProfile) RequestOptions() RequestOptions {
	opts := RequestOptions{
		Model:            s.Model,
		RoutingFactors:   s.RoutingFactors(),
		DelegationGating: s.DelegationGating,
	}

	if len(s.ExcludeTools) > 0 {
		opts.ExcludeTools = append([]string(nil), s.ExcludeTools...)
	}

	return opts
}
