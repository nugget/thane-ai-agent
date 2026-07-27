// Package metacognitive implements a perpetual self-regulating attention
// loop that receives persistent state, reasons via LLM, and adapts its
// own sleep cycle. See issue #319.
//
// Each iteration is a fresh conversation. State persists across iterations
// via a markdown file (metacognitive.md by default). The loop's cost is
// self-limiting: quiet periods produce long sleeps and few iterations.
//
// Supervisor turns randomly select a frontier model to review the
// loop's own behavior, catching blind spots that the cheaper local
// model's consistent reasoning patterns miss. The per-wake
// probability is driven by [Config.SupervisorProbability]; when a
// supervisor turn fires, the loop overlays
// [loop.Spec.SupervisorProfile] on its normal routing.
//
// The loop lifecycle is managed by the [loop] package. This loop is
// declarative and attaches no runtime hooks: the per-iteration prompt is
// the spec Task plus the supervisor-turn [loop.Spec.SupervisorProfile]
// Instructions, and the state document is written only by the model,
// through the declared output — the same shape as the ego loop.
package metacognitive

import (
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// DefinitionName is the durable loop definition name for the
// metacognitive service.
const DefinitionName = "metacognitive"

// stateFileName is the fixed filename of the metacognitive state
// document. The app runtime places it under workspace/core.
const stateFileName = "metacognitive.md"

// Config holds the parsed metacognitive loop configuration with
// time.Duration fields (as opposed to the YAML string representation
// in [config.MetacognitiveConfig]).
type Config struct {
	Enabled                bool
	StateFile              string // fixed filename under workspace/core
	MinSleep               time.Duration
	MaxSleep               time.Duration
	DefaultSleep           time.Duration
	Jitter                 float64 // 0.0–1.0
	SupervisorProbability  float64 // 0.0–1.0
	QualityFloor           int     // normal iterations
	SupervisorQualityFloor int     // supervisor turns
}

// ParseConfig converts a [config.MetacognitiveConfig] (string durations)
// into a [Config] (time.Duration fields). Call after config validation
// has passed.
func ParseConfig(raw config.MetacognitiveConfig) (Config, error) {
	minSleep, err := time.ParseDuration(raw.MinSleep)
	if err != nil {
		return Config{}, fmt.Errorf("min_sleep %q: %w", raw.MinSleep, err)
	}
	maxSleep, err := time.ParseDuration(raw.MaxSleep)
	if err != nil {
		return Config{}, fmt.Errorf("max_sleep %q: %w", raw.MaxSleep, err)
	}
	defaultSleep, err := time.ParseDuration(raw.DefaultSleep)
	if err != nil {
		return Config{}, fmt.Errorf("default_sleep %q: %w", raw.DefaultSleep, err)
	}
	return Config{
		Enabled:                raw.Enabled,
		StateFile:              stateFileName,
		MinSleep:               minSleep,
		MaxSleep:               maxSleep,
		DefaultSleep:           defaultSleep,
		Jitter:                 derefFloat(raw.Jitter, 0.2),
		SupervisorProbability:  derefFloat(raw.SupervisorProbability, 0.1),
		QualityFloor:           raw.Router.QualityFloor,
		SupervisorQualityFloor: raw.SupervisorRouter.QualityFloor,
	}, nil
}

// derefFloat returns *p when non-nil, otherwise def. Used to apply
// the package-level fallback when ParseConfig is called with a raw
// config struct that bypassed [config.Config.applyDefaults] (typically
// in tests).
func derefFloat(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

// DefinitionSpec returns the persistable loop definition for the
// metacognitive service. Runtime hooks are attached later by
// [HydrateSpec] so the definition can live in the durable registry.
func DefinitionSpec(cfg Config) loop.Spec {
	return loop.Spec{
		Name:       DefinitionName,
		Enabled:    cfg.Enabled,
		Task:       prompts.MetacognitiveBaseTemplate,
		Operation:  loop.OperationService,
		Completion: loop.CompletionNone,
		Outputs: []loop.OutputSpec{
			{
				Name:    "metacognitive_state",
				Type:    loop.OutputTypeMaintainedDocument,
				Ref:     "core:metacognitive.md",
				Mode:    loop.OutputModeReplace,
				Purpose: "Current metacognitive state: active concerns, recent observations, actions taken, and sleep reasoning that should persist across fresh loop iterations.",
			},
		},
		SleepMin:     cfg.MinSleep,
		SleepMax:     cfg.MaxSleep,
		SleepDefault: cfg.DefaultSleep,
		Jitter:       loop.Float64Ptr(cfg.Jitter),
		ExcludeTools: metacogExcludeTools,
		Tags:         []string{"metacognitive"},
		Profile: router.LoopProfile{
			Mission:          "metacognitive",
			DelegationGating: "disabled",
			QualityFloor:     cfg.QualityFloor,
			ExtraHints:       map[string]string{"source": "metacognitive"},
		},
		SupervisorProfile: supervisorProfile(cfg.SupervisorQualityFloor),

		Supervisor:     cfg.SupervisorProbability > 0,
		SupervisorProb: cfg.SupervisorProbability,
		Metadata: map[string]string{
			"subsystem": "metacognitive",
			"category":  "service",
		},
	}
}

// supervisorProfile builds the metacognitive service's supervisor-turn
// overlay: the frontier-review prompt prefix (always) plus a higher
// quality floor when one is configured. Unset fields fall back to the
// normal Profile during supervisor turns; the prefix is prepended to the
// Task.
func supervisorProfile(qualityFloor int) *router.LoopProfile {
	p := &router.LoopProfile{Instructions: prompts.MetacognitiveSupervisorInstructions}
	if qualityFloor > 0 {
		p.QualityFloor = qualityFloor
	}
	return p
}

// HydrateSpec fills in what a durable loop definition cannot carry.
// That is now only the name: the prompt is declarative (the spec Task
// and SupervisorProfile.Instructions), and nothing runtime-side writes
// the state document.
func HydrateSpec(spec loop.Spec, _ Config) loop.Spec {
	if strings.TrimSpace(spec.Name) == "" {
		spec.Name = DefinitionName
	}
	return spec
}

// metacogExcludeTools lists tools that the metacognitive loop should not
// have access to. File tools are replaced by the declared durable output
// tool, exec is unnecessary and dangerous, session management is for
// interactive use only.
var metacogExcludeTools = append([]string{
	"file_read", "file_write", "file_edit", "file_list",
	"file_search", "file_grep", "file_stat", "file_tree",
	"exec",
	"conversation_reset", "session_close", "session_split", "session_checkpoint",
	"create_temp_file",
	"tag_activate", "tag_deactivate",
	// A reflective loop must not stand up new durable loops; thane_loop_create is
	// Core (#1106 A), so it has to be excluded by name rather than gated behind
	// the `loops` capability metacognition can't activate.
	"thane_loop_create",
}, tools.DirectHumanEgressToolNames()...)
