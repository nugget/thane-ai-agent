package loop

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
)

type specJSON struct {
	Name           string               `json:"name,omitempty"`
	Enabled        bool                 `json:"enabled"`
	Task           string               `json:"task,omitempty"`
	Profile        any                  `json:"profile,omitempty"`
	Operation      Operation            `json:"operation,omitempty"`
	Completion     Completion           `json:"completion,omitempty"`
	Outputs        []OutputSpec         `json:"outputs,omitempty"`
	Subscriptions  []EntitySubscription `json:"subscriptions,omitempty"`
	Conditions     Conditions           `json:"conditions,omitempty"`
	Tags           []string             `json:"tags,omitempty"`
	ExcludeTools   []string             `json:"exclude_tools,omitempty"`
	SleepMin       string               `json:"sleep_min,omitempty"`
	SleepMax       string               `json:"sleep_max,omitempty"`
	SleepDefault   string               `json:"sleep_default,omitempty"`
	Jitter         *float64             `json:"jitter,omitempty"`
	MaxDuration    string               `json:"max_duration,omitempty"`
	MaxIter        int                  `json:"max_iter,omitempty"`
	Supervisor     bool                 `json:"supervisor,omitempty"`
	SupervisorProb float64              `json:"supervisor_prob,omitempty"`
	// SupervisorProfile is typed as a pointer (not [any]) so a nil
	// [Spec.SupervisorProfile] is correctly omitted from the wire by
	// `omitempty`. A typed-nil pointer in an `any` interface field
	// would be non-nil to JSON's omitempty check and would emit
	// `"supervisor_profile": null` instead of being omitted, which
	// would contradict the doc on [Spec.SupervisorProfile] and
	// surface a null where consumers expect an omitted field.
	SupervisorProfile *router.LoopProfile `json:"supervisor_profile,omitempty"`
	OnRetrigger       string              `json:"on_retrigger,omitempty"`
	RoutingFactors    map[string]string   `json:"routing_factors,omitempty"`
	DelegationGating  string              `json:"delegation_gating,omitempty"`
	FallbackContent   string              `json:"fallback_content,omitempty"`
	Metadata          map[string]string   `json:"metadata,omitempty"`
	ParentID          string              `json:"parent_id,omitempty"`
	ParentName        string              `json:"parent_name,omitempty"`

	// Legacy top-level fields, accepted on UnmarshalJSON for backwards
	// compatibility with persisted overlay specs written before the
	// PR-R1 routing-surface unification. Translated into Profile /
	// SupervisorProfile on load with a one-shot WARN log. Never
	// emitted by MarshalJSON — new persists use the canonical shape.
	LegacyQualityFloor           int    `json:"quality_floor,omitempty"`
	LegacySupervisorContext      string `json:"supervisor_context,omitempty"`
	LegacySupervisorQualityFloor int    `json:"supervisor_quality_floor,omitempty"`
}

// MarshalJSON renders a loop spec in a human-facing contract shape
// suitable for APIs and tools: durations are strings and retrigger
// mode is named instead of using the engine's integer form.
// Supervisor routing overrides emit as `supervisor_profile` (nil
// omits the field); the legacy top-level `quality_floor`,
// `supervisor_quality_floor`, and `supervisor_context` fields are
// no longer emitted — they only round-trip through UnmarshalJSON
// for backwards compatibility with pre-PR-R1 persisted specs.
func (s Spec) MarshalJSON() ([]byte, error) {
	wire := specJSON{
		Name:              s.Name,
		Enabled:           s.Enabled,
		Task:              s.Task,
		Profile:           s.Profile,
		Operation:         s.Operation,
		Completion:        s.Completion,
		Outputs:           cloneOutputs(s.Outputs),
		Subscriptions:     cloneEntitySubscriptions(s.Subscriptions),
		Conditions:        s.Conditions,
		Tags:              s.Tags,
		ExcludeTools:      s.ExcludeTools,
		SleepMin:          durationString(s.SleepMin),
		SleepMax:          durationString(s.SleepMax),
		SleepDefault:      durationString(s.SleepDefault),
		Jitter:            s.Jitter,
		MaxDuration:       durationString(s.MaxDuration),
		MaxIter:           s.MaxIter,
		Supervisor:        s.Supervisor,
		SupervisorProb:    s.SupervisorProb,
		SupervisorProfile: s.SupervisorProfile,
		RoutingFactors:    s.RoutingFactors,
		DelegationGating:  s.DelegationGating,
		FallbackContent:   s.FallbackContent,
		Metadata:          s.Metadata,
		ParentID:          s.ParentID,
		ParentName:        s.ParentName,
	}
	onRetrigger, err := s.OnRetrigger.MarshalText()
	if err != nil {
		return nil, err
	}
	wire.OnRetrigger = string(onRetrigger)
	return json.Marshal(wire)
}

// UnmarshalJSON accepts the same human-facing contract shape emitted by
// [Spec.MarshalJSON].
func (s *Spec) UnmarshalJSON(data []byte) error {
	if s == nil {
		return fmt.Errorf("loop: nil spec")
	}
	var wire specJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	sleepMin, err := parseOptionalDuration(wire.SleepMin)
	if err != nil {
		return fmt.Errorf("loop: sleep_min: %w", err)
	}
	sleepMax, err := parseOptionalDuration(wire.SleepMax)
	if err != nil {
		return fmt.Errorf("loop: sleep_max: %w", err)
	}
	sleepDefault, err := parseOptionalDuration(wire.SleepDefault)
	if err != nil {
		return fmt.Errorf("loop: sleep_default: %w", err)
	}
	maxDuration, err := parseOptionalDuration(wire.MaxDuration)
	if err != nil {
		return fmt.Errorf("loop: max_duration: %w", err)
	}
	onRetrigger, err := ParseRetriggerMode(wire.OnRetrigger)
	if err != nil {
		return fmt.Errorf("loop: on_retrigger: %w", err)
	}
	// Apply the same forecast normalization and AddedAt stamping
	// the tool-boundary write paths use. Without this, a
	// hand-edited or externally-pushed Spec with `ttl_seconds > 0`
	// and missing `added_at` would unmarshal into a permanent
	// watcher (IsExpired returns false forever, the documented
	// footgun on EntitySubscription.AddedAt). Stamping at load
	// time turns "this subscription has never had its TTL clock
	// started" into "TTL counts from when the spec was loaded" —
	// which matches operator intent better than "ignored
	// silently."
	normalizedSubs, err := normalizeSubscriptionsOnLoad(cloneEntitySubscriptions(wire.Subscriptions), time.Now())
	if err != nil {
		return fmt.Errorf("loop: %w", err)
	}
	*s = Spec{
		Name:             wire.Name,
		Enabled:          wire.Enabled,
		Task:             wire.Task,
		Operation:        wire.Operation,
		Completion:       wire.Completion,
		Outputs:          cloneOutputs(wire.Outputs),
		Subscriptions:    normalizedSubs,
		Conditions:       cloneConditions(wire.Conditions),
		Tags:             append([]string(nil), wire.Tags...),
		ExcludeTools:     append([]string(nil), wire.ExcludeTools...),
		SleepMin:         sleepMin,
		SleepMax:         sleepMax,
		SleepDefault:     sleepDefault,
		Jitter:           cloneFloat64Ptr(wire.Jitter),
		MaxDuration:      maxDuration,
		MaxIter:          wire.MaxIter,
		Supervisor:       wire.Supervisor,
		SupervisorProb:   wire.SupervisorProb,
		OnRetrigger:      onRetrigger,
		RoutingFactors:   cloneStringMap(wire.RoutingFactors),
		DelegationGating: wire.DelegationGating,
		FallbackContent:  wire.FallbackContent,
		Metadata:         cloneStringMap(wire.Metadata),
		ParentID:         wire.ParentID,
		ParentName:       wire.ParentName,
	}
	profileData, err := json.Marshal(wire.Profile)
	if err != nil {
		return err
	}
	if len(profileData) != 0 && string(profileData) != "null" {
		if err := json.Unmarshal(profileData, &s.Profile); err != nil {
			return fmt.Errorf("loop: profile: %w", err)
		}
	}
	// wire.SupervisorProfile is already a typed *router.LoopProfile
	// — JSON decoded it directly (including running
	// LoopProfile.UnmarshalJSON for backwards-compat int/string
	// quality_floor handling) before we got here.
	s.SupervisorProfile = wire.SupervisorProfile
	// Migration shim for pre-PR-R1 persisted specs: translate the
	// legacy top-level quality_floor / supervisor_quality_floor /
	// supervisor_context fields into the canonical Profile /
	// SupervisorProfile shape. New fields win on collision —
	// callers writing the new shape don't get clobbered by
	// stragglers left in the JSON.
	//
	// Logs at WARN every time a legacy-shaped spec is unmarshaled,
	// not just once per upgrade — the function has no
	// process-level memory of which specs have been migrated
	// before. In practice the noise self-limits: once the
	// definition registry persists the spec back to disk in the
	// new shape (which happens on the next mutation or
	// reconcile), subsequent loads find no legacy fields and the
	// warn stops firing. Operators upgrading a deployment with
	// many legacy overlay specs will see one burst of warns at
	// startup and then quiet.
	migrateLegacyRoutingFields(s, wire, slog.Default())
	return nil
}

// migrateLegacyRoutingFields rewrites legacy top-level routing
// fields onto the spec's [Profile] / [SupervisorProfile] surfaces.
// Idempotent: a fully-migrated spec carries empty legacy fields
// and the function is a no-op. Exposed at package scope so the
// regression tests can drive it directly without round-tripping
// JSON.
func migrateLegacyRoutingFields(s *Spec, wire specJSON, logger *slog.Logger) {
	if s == nil {
		return
	}
	migrated := false
	if wire.LegacyQualityFloor > 0 && s.Profile.QualityFloor == "" {
		s.Profile.QualityFloor = fmt.Sprintf("%d", wire.LegacyQualityFloor)
		migrated = true
	}
	if wire.LegacySupervisorQualityFloor > 0 {
		if s.SupervisorProfile == nil {
			s.SupervisorProfile = &router.LoopProfile{}
		}
		if s.SupervisorProfile.QualityFloor == "" {
			s.SupervisorProfile.QualityFloor = fmt.Sprintf("%d", wire.LegacySupervisorQualityFloor)
			migrated = true
		}
	}
	if wire.LegacySupervisorContext != "" {
		if s.SupervisorProfile == nil {
			s.SupervisorProfile = &router.LoopProfile{}
		}
		if s.SupervisorProfile.Instructions == "" {
			s.SupervisorProfile.Instructions = wire.LegacySupervisorContext
			migrated = true
		}
	}
	if migrated && logger != nil {
		logger.Warn("migrated pre-PR-R1 routing fields onto Profile/SupervisorProfile during spec hydration",
			"loop_name", s.Name,
			"legacy_quality_floor", wire.LegacyQualityFloor,
			"legacy_supervisor_quality_floor", wire.LegacySupervisorQualityFloor,
			"legacy_supervisor_context_set", wire.LegacySupervisorContext != "",
		)
	}
}

func durationString(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}

func parseOptionalDuration(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	return time.ParseDuration(raw)
}
