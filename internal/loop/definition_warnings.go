package loop

import (
	"fmt"
	"strings"
)

// DefinitionWarning is a non-fatal authoring concern discovered while
// inspecting a persistable loops-ng definition. Warnings are surfaced
// through definition views and lint tooling so the model can correct
// likely mistakes before they become noisy runtime behavior.
type DefinitionWarning struct {
	Code    string `yaml:"code" json:"code"`
	Message string `yaml:"message" json:"message"`
}

// BuildDefinitionWarnings returns authoring warnings for one loop spec.
// These warnings are advisory: the spec may still validate and persist.
func BuildDefinitionWarnings(spec Spec) []DefinitionWarning {
	if effectiveOperation(spec.Operation) != OperationService {
		return nil
	}

	var warnings []DefinitionWarning
	missingSleep := missingServiceSleepFields(spec)
	switch len(missingSleep) {
	case 4:
		warnings = append(warnings, DefinitionWarning{
			Code: "service_default_cadence",
			Message: fmt.Sprintf(
				"Service loop omits sleep_min, sleep_max, sleep_default, and jitter. It will use engine defaults of sleep_min=%s, sleep_max=%s, sleep_default=%s, jitter=%.1f. Natural-language timing in task text does not schedule the loop.",
				DefaultSleepMin,
				DefaultSleepMax,
				DefaultSleepDefault,
				DefaultJitter,
			),
		})
	case 1, 2, 3:
		warnings = append(warnings, DefinitionWarning{
			Code: "service_partial_cadence_defaults",
			Message: fmt.Sprintf(
				"Service loop leaves %s implicit. Omitted timing fields fall back to sleep_min=%s, sleep_max=%s, sleep_default=%s, jitter=%.1f.",
				quotedFieldList(missingSleep),
				DefaultSleepMin,
				DefaultSleepMax,
				DefaultSleepDefault,
				DefaultJitter,
			),
		})
	}

	if taskSuggestsCadence(spec.Task) && len(missingSleep) > 0 {
		warnings = append(warnings, DefinitionWarning{
			Code:    "task_mentions_cadence_without_explicit_sleep",
			Message: "Task text suggests a cadence such as hourly or daily, but service-loop timing comes only from sleep_min, sleep_max, sleep_default, and jitter.",
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
			Code:    "fixed_cadence_with_jitter",
			Message: "Fixed cadence is implied because sleep_min == sleep_max == sleep_default, but jitter is still non-zero. Set jitter to 0 if the loop should wake on a stable interval.",
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

func taskSuggestsCadence(task string) bool {
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
