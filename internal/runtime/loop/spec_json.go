package loop

import (
	"encoding/json"
	"fmt"
	"time"
)

type specJSON struct {
	Name                   string               `json:"name,omitempty"`
	Enabled                bool                 `json:"enabled"`
	Task                   string               `json:"task,omitempty"`
	Profile                any                  `json:"profile,omitempty"`
	Operation              Operation            `json:"operation,omitempty"`
	Completion             Completion           `json:"completion,omitempty"`
	Outputs                []OutputSpec         `json:"outputs,omitempty"`
	Subscriptions          []EntitySubscription `json:"subscriptions,omitempty"`
	Conditions             Conditions           `json:"conditions,omitempty"`
	Tags                   []string             `json:"tags,omitempty"`
	ExcludeTools           []string             `json:"exclude_tools,omitempty"`
	SleepMin               string               `json:"sleep_min,omitempty"`
	SleepMax               string               `json:"sleep_max,omitempty"`
	SleepDefault           string               `json:"sleep_default,omitempty"`
	Jitter                 *float64             `json:"jitter,omitempty"`
	MaxDuration            string               `json:"max_duration,omitempty"`
	MaxIter                int                  `json:"max_iter,omitempty"`
	Supervisor             bool                 `json:"supervisor,omitempty"`
	SupervisorProb         float64              `json:"supervisor_prob,omitempty"`
	QualityFloor           int                  `json:"quality_floor,omitempty"`
	SupervisorContext      string               `json:"supervisor_context,omitempty"`
	SupervisorQualityFloor int                  `json:"supervisor_quality_floor,omitempty"`
	OnRetrigger            string               `json:"on_retrigger,omitempty"`
	RoutingFactors         map[string]string    `json:"routing_factors,omitempty"`
	DelegationGating       string               `json:"delegation_gating,omitempty"`
	FallbackContent        string               `json:"fallback_content,omitempty"`
	Metadata               map[string]string    `json:"metadata,omitempty"`
	ParentID               string               `json:"parent_id,omitempty"`
	ParentName             string               `json:"parent_name,omitempty"`
}

// MarshalJSON renders a loop spec in a human-facing contract shape
// suitable for APIs and tools: durations are strings and retrigger mode is
// named instead of using the engine's integer form.
func (s Spec) MarshalJSON() ([]byte, error) {
	wire := specJSON{
		Name:                   s.Name,
		Enabled:                s.Enabled,
		Task:                   s.Task,
		Profile:                s.Profile,
		Operation:              s.Operation,
		Completion:             s.Completion,
		Outputs:                cloneOutputs(s.Outputs),
		Subscriptions:          cloneEntitySubscriptions(s.Subscriptions),
		Conditions:             s.Conditions,
		Tags:                   s.Tags,
		ExcludeTools:           s.ExcludeTools,
		SleepMin:               durationString(s.SleepMin),
		SleepMax:               durationString(s.SleepMax),
		SleepDefault:           durationString(s.SleepDefault),
		Jitter:                 s.Jitter,
		MaxDuration:            durationString(s.MaxDuration),
		MaxIter:                s.MaxIter,
		Supervisor:             s.Supervisor,
		SupervisorProb:         s.SupervisorProb,
		QualityFloor:           s.QualityFloor,
		SupervisorContext:      s.SupervisorContext,
		SupervisorQualityFloor: s.SupervisorQualityFloor,
		RoutingFactors:         s.RoutingFactors,
		DelegationGating:       s.DelegationGating,
		FallbackContent:        s.FallbackContent,
		Metadata:               s.Metadata,
		ParentID:               s.ParentID,
		ParentName:             s.ParentName,
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
		Name:                   wire.Name,
		Enabled:                wire.Enabled,
		Task:                   wire.Task,
		Operation:              wire.Operation,
		Completion:             wire.Completion,
		Outputs:                cloneOutputs(wire.Outputs),
		Subscriptions:          normalizedSubs,
		Conditions:             cloneConditions(wire.Conditions),
		Tags:                   append([]string(nil), wire.Tags...),
		ExcludeTools:           append([]string(nil), wire.ExcludeTools...),
		SleepMin:               sleepMin,
		SleepMax:               sleepMax,
		SleepDefault:           sleepDefault,
		Jitter:                 cloneFloat64Ptr(wire.Jitter),
		MaxDuration:            maxDuration,
		MaxIter:                wire.MaxIter,
		Supervisor:             wire.Supervisor,
		SupervisorProb:         wire.SupervisorProb,
		QualityFloor:           wire.QualityFloor,
		SupervisorContext:      wire.SupervisorContext,
		SupervisorQualityFloor: wire.SupervisorQualityFloor,
		OnRetrigger:            onRetrigger,
		RoutingFactors:         cloneStringMap(wire.RoutingFactors),
		DelegationGating:       wire.DelegationGating,
		FallbackContent:        wire.FallbackContent,
		Metadata:               cloneStringMap(wire.Metadata),
		ParentID:               wire.ParentID,
		ParentName:             wire.ParentName,
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
	return nil
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
