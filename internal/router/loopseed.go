package router

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
