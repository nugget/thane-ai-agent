// Package ego implements the self-reflection loop that maintains
// core/ego.md. It runs as a loops-ng service: each iteration is a fresh
// conversation with bounded voluntary sleep, supervisor randomization
// for periodic frontier review, and a declared maintained-document
// output that pins ego.md to the loop.
//
// The agent's core context provider reads ego.md every turn and injects
// it into the system prompt; this loop is the sole writer.
package ego

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// DefinitionName is the durable loops-ng definition name for the ego
// service.
const DefinitionName = "ego"

// stateFileName is the fixed filename of the ego self-reflection
// document. The runtime places it under workspace/core.
const stateFileName = "ego.md"

// Config holds the parsed ego loop configuration with time.Duration
// fields (as opposed to the YAML string representation in
// [config.EgoConfig]).
type Config struct {
	Enabled                bool
	StateFile              string
	MinSleep               time.Duration
	MaxSleep               time.Duration
	DefaultSleep           time.Duration
	Jitter                 float64
	SupervisorProbability  float64
	QualityFloor           int
	SupervisorQualityFloor int
}

// ParseConfig converts a [config.EgoConfig] (string durations) into a
// [Config] (time.Duration fields). Call after config validation has
// passed.
func ParseConfig(raw config.EgoConfig) (Config, error) {
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
		SupervisorProbability:  derefFloat(raw.SupervisorProbability, 0.2),
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

// DefinitionSpec returns the persistable loops-ng definition for the ego
// service. Runtime hooks are attached later by [HydrateSpec] so the
// definition can live in the durable registry.
func DefinitionSpec(cfg Config) loop.Spec {
	return loop.Spec{
		Name:       DefinitionName,
		Enabled:    cfg.Enabled,
		Task:       "Reflect on recent experience and update self-reflection state when warranted.",
		Operation:  loop.OperationService,
		Completion: loop.CompletionNone,
		Outputs: []loop.OutputSpec{
			{
				Name:    "ego_state",
				Type:    loop.OutputTypeMaintainedDocument,
				Ref:     "core:ego.md",
				Mode:    loop.OutputModeReplace,
				Purpose: "Self-reflection written by the ego loop, for the agent: how the agent's thinking is evolving, behavioral patterns it notices in itself, observations about its relationships, genuine open questions, and honest self-assessment. Read every turn via the agent's core context. NOT a task list, status report, or operational notes.",
			},
		},
		SleepMin:     cfg.MinSleep,
		SleepMax:     cfg.MaxSleep,
		SleepDefault: cfg.DefaultSleep,
		Jitter:       loop.Float64Ptr(cfg.Jitter),
		ExcludeTools: egoExcludeTools,
		Profile: router.LoopProfile{
			Mission:          "ego",
			DelegationGating: "disabled",
			InitialTags:      []string{"ego"},
			ExtraHints:       map[string]string{"source": "ego"},
		},
		Supervisor:             cfg.SupervisorProbability > 0,
		SupervisorProb:         cfg.SupervisorProbability,
		QualityFloor:           cfg.QualityFloor,
		SupervisorQualityFloor: cfg.SupervisorQualityFloor,
		Metadata: map[string]string{
			"subsystem": "ego",
			"category":  "service",
		},
	}
}

// HydrateSpec attaches the runtime-only hooks needed to execute the ego
// service from a durable loops-ng definition.
func HydrateSpec(spec loop.Spec, cfg Config) loop.Spec {
	if strings.TrimSpace(spec.Name) == "" {
		spec.Name = DefinitionName
	}
	spec.TaskBuilder = func(ctx context.Context, isSupervisor bool) (string, error) {
		return prompts.EgoPrompt(isSupervisor), nil
	}
	return spec
}

// BuildSpec returns a [loop.Spec] that implements the ego loop as a
// standard loops-ng service. The returned spec declares the durable
// output document and uses runtime hooks to build prompts.
func BuildSpec(cfg Config) loop.Spec {
	spec := DefinitionSpec(cfg)
	spec.SupervisorContext = ""
	return HydrateSpec(spec, cfg)
}

// BuildLoopConfig returns the engine-facing [loop.Config] view of the
// ego loop. Kept as a compatibility bridge while loops-ng adoption is
// in progress.
func BuildLoopConfig(cfg Config) loop.Config {
	spec := BuildSpec(cfg)
	out := spec.ToConfig()
	profileHints := spec.Profile.Hints()
	if len(profileHints) > 0 {
		if out.Hints == nil {
			out.Hints = make(map[string]string, len(profileHints))
		}
		for k, v := range profileHints {
			out.Hints[k] = v
		}
	}
	return out
}

// egoExcludeTools lists tools that the ego loop should not have access
// to. File tools are replaced by the declared durable output tool, exec
// is unnecessary, session management is for interactive use only.
var egoExcludeTools = []string{
	"file_read", "file_write", "file_edit", "file_list",
	"file_search", "file_grep", "file_stat", "file_tree",
	"exec",
	"conversation_reset", "session_close", "session_split", "session_checkpoint",
	"create_temp_file",
	"activate_capability", "deactivate_capability",
}
