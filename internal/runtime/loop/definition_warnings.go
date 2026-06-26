package loop

import (
	"fmt"
	"sort"
	"strings"
)

// DefinitionWarning is a non-fatal authoring concern discovered while
// inspecting a persistable loop definition. Warnings are surfaced
// through definition views and lint tooling so the model can correct
// likely mistakes before they become noisy runtime behavior.
type DefinitionWarning struct {
	Code    string `yaml:"code" json:"code"`
	Message string `yaml:"message" json:"message"`
}

// BuildDefinitionWarnings returns authoring warnings for one loop spec.
// These warnings are advisory: the spec may still validate and persist.
func BuildDefinitionWarnings(spec Spec) []DefinitionWarning {
	// Shadow checks run for every operation: a metadata key that duplicates a
	// real top-level spec field is inert for any loop kind, and parent_name in
	// metadata in particular silently fails to nest the loop — the runtime
	// parents off the structural field, never metadata. Collect these before
	// the service-only gate so containers and event-driven loops are covered.
	warnings := metadataShadowWarnings(spec)

	if effectiveOperation(spec.Operation) != OperationService {
		return warnings
	}

	missingSleep := missingServiceSleepFields(spec)
	switch len(missingSleep) {
	case 4:
		warnings = append(warnings, DefinitionWarning{
			Code: "service_default_sleep_envelope",
			Message: fmt.Sprintf(
				"Service loop omits sleep_min, sleep_max, sleep_default, and jitter. It will use engine defaults of sleep_min=%s, sleep_max=%s, sleep_default=%s, jitter=%.1f. Natural-language timing in task text does not schedule the loop.",
				DefaultSleepMin.String(),
				DefaultSleepMax.String(),
				DefaultSleepDefault.String(),
				DefaultJitter,
			),
		})
	case 1, 2, 3:
		warnings = append(warnings, DefinitionWarning{
			Code: "service_partial_sleep_defaults",
			Message: fmt.Sprintf(
				"Service loop leaves %s implicit. Omitted timing fields fall back to sleep_min=%s, sleep_max=%s, sleep_default=%s, jitter=%.1f.",
				quotedFieldList(missingSleep),
				DefaultSleepMin.String(),
				DefaultSleepMax.String(),
				DefaultSleepDefault.String(),
				DefaultJitter,
			),
		})
	}

	if taskSuggestsTiming(spec.Task) && len(missingSleep) > 0 {
		warnings = append(warnings, DefinitionWarning{
			Code:    "task_mentions_timing_without_explicit_sleep",
			Message: "Task text suggests a recurring schedule such as hourly or daily, but service-loop timing comes only from sleep_min, sleep_max, sleep_default, and jitter.",
		})
	}

	if len(spec.Tags) > 0 && strings.TrimSpace(spec.Profile.DelegationGating) != "disabled" {
		warnings = append(warnings, DefinitionWarning{
			Code:    "service_delegation_gating_enabled",
			Message: "Tagged service loop keeps profile.delegation_gating enabled. That often leaves the loop with orchestration tools instead of direct tagged tools. Set profile.delegation_gating to \"disabled\" when the loop should use its own tools directly.",
		})
	}

	if spec.Completion != "" && spec.Completion != CompletionNone {
		warnings = append(warnings, DefinitionWarning{
			Code:    "service_completion_not_periodic",
			Message: "Service-loop completion is not a periodic callback. Completion delivery only matters when the loop stops or exits. Use prompt-level decision boundaries inside the loop or use background_task for a single eventual reply.",
		})
	}

	cfg := spec.ToConfig()
	cfg.applyDefaults()
	jitter := DefaultJitter
	if cfg.Jitter != nil {
		jitter = *cfg.Jitter
	}
	if cfg.SleepMin == cfg.SleepMax && cfg.SleepDefault == cfg.SleepMin && jitter > 0 {
		warnings = append(warnings, DefinitionWarning{
			Code:    "fixed_interval_with_jitter",
			Message: "Fixed interval is implied because sleep_min == sleep_max == sleep_default, but jitter is still non-zero. Set jitter to 0 if the loop should wake on a stable interval.",
		})
	}

	return warnings
}

// shadowableSpecFields is the set of canonical top-level spec wire names (the
// json tags on specJSON in spec_json.go) that a metadata key can collide
// with. A metadata entry matching one of these is almost certainly a
// misplaced structural field: Metadata is stored verbatim and never
// interpreted, so the real field stays unset. parent_name is the motivating
// case — a loop authored with parent_name in metadata silently lands at the
// graph root instead of under its container. The set excludes "metadata"
// itself and the legacy compat keys (quality_floor, supervisor_context,
// supervisor_quality_floor), which are deliberately not part of the canonical
// authoring surface.
var shadowableSpecFields = map[string]struct{}{
	"name": {}, "enabled": {}, "task": {}, "profile": {}, "operation": {},
	"completion": {}, "outputs": {}, "subscriptions": {}, "conditions": {},
	"tags": {}, "exclude_tools": {}, "sleep_min": {}, "sleep_max": {},
	"sleep_default": {}, "jitter": {}, "max_duration": {}, "max_iter": {},
	"supervisor": {}, "supervisor_prob": {}, "supervisor_profile": {},
	"on_retrigger": {}, "routing_factors": {}, "delegation_gating": {},
	"fallback_content": {}, "parent_name": {}, "parent_id": {},
}

// metadataShadowWarnings flags metadata keys that collide with a real
// top-level spec field. Such keys are inert: the runtime reads the structural
// field, not metadata, so the value never takes effect. parent_name/parent_id
// get a placement-specific message because the failure is silent
// mis-parenting rather than a generic dropped value.
func metadataShadowWarnings(spec Spec) []DefinitionWarning {
	if len(spec.Metadata) == 0 {
		return nil
	}
	shadowed := make([]string, 0, len(spec.Metadata))
	for key := range spec.Metadata {
		if _, ok := shadowableSpecFields[key]; ok {
			shadowed = append(shadowed, key)
		}
	}
	if len(shadowed) == 0 {
		return nil
	}
	sort.Strings(shadowed) // deterministic order across repeated lint/set calls

	warnings := make([]DefinitionWarning, 0, len(shadowed))
	for _, key := range shadowed {
		var msg string
		switch key {
		case "parent_name", "parent_id":
			msg = "metadata." + key + " is inert and does not nest this loop under a parent: metadata is stored verbatim and never interpreted. Set the top-level parent_name field instead."
		default:
			msg = fmt.Sprintf("metadata.%s shadows the top-level %s field, which the runtime reads instead of metadata. The metadata copy is stored verbatim and never interpreted, so it is inert; set the top-level %s field.", key, key, key)
		}
		warnings = append(warnings, DefinitionWarning{
			Code:    "metadata_shadows_spec_field",
			Message: msg,
		})
	}
	return warnings
}

func missingServiceSleepFields(spec Spec) []string {
	missing := make([]string, 0, 4)
	if spec.SleepMin == 0 {
		missing = append(missing, "sleep_min")
	}
	if spec.SleepMax == 0 {
		missing = append(missing, "sleep_max")
	}
	if spec.SleepDefault == 0 {
		missing = append(missing, "sleep_default")
	}
	if spec.Jitter == nil {
		missing = append(missing, "jitter")
	}
	return missing
}

func quotedFieldList(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(fields))
	for _, field := range fields {
		quoted = append(quoted, fmt.Sprintf("%q", field))
	}
	if len(quoted) == 1 {
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + ", and " + quoted[len(quoted)-1]
}

func taskSuggestsTiming(task string) bool {
	task = strings.ToLower(strings.TrimSpace(task))
	if task == "" {
		return false
	}
	for _, needle := range []string{
		" hourly",
		"daily",
		"weekly",
		"every hour",
		"every day",
		"every night",
		"each hour",
		"once a day",
		"per hour",
	} {
		if strings.Contains(task, needle) {
			return true
		}
	}
	return strings.HasPrefix(task, "hourly") || strings.HasPrefix(task, "daily") || strings.HasPrefix(task, "weekly")
}
