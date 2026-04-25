package tools

import (
	"context"
	"fmt"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

const (
	defaultLoopDefinitionListLimit = 25
	maxLoopDefinitionListLimit     = 200
)

// LoopDefinitionToolDeps wires the live loop-definition registry into the
// tool registry so the model can inspect and mutate the persistent loops-ng
// definition overlay.
type LoopDefinitionToolDeps struct {
	Registry         *looppkg.DefinitionRegistry
	View             func() *looppkg.DefinitionRegistryView
	PersistSpec      func(looppkg.Spec, time.Time) error
	DeleteSpec       func(string) error
	PersistPolicy    func(string, looppkg.DefinitionPolicy) error
	DeletePolicy     func(string) error
	Reconcile        func(context.Context, string) error
	LaunchDefinition func(context.Context, string, looppkg.Launch) (looppkg.LaunchResult, error)
}

// ConfigureLoopDefinitionTools stores the runtime dependencies needed by
// the loop-definition tool family and registers the tools.
func (r *Registry) ConfigureLoopDefinitionTools(deps LoopDefinitionToolDeps) {
	r.loopDefinitionRegistry = deps.Registry
	r.loopDefinitionView = deps.View
	r.persistLoopDefinition = deps.PersistSpec
	r.deletePersistedLoopDefinition = deps.DeleteSpec
	r.persistLoopDefinitionPolicy = deps.PersistPolicy
	r.deletePersistedLoopDefinitionPolicy = deps.DeletePolicy
	r.reconcileLoopDefinition = deps.Reconcile
	r.launchLoopDefinition = deps.LaunchDefinition
	r.registerLoopDefinitionTools()
}

func (r *Registry) registerLoopDefinitionTools() {
	if r.loopDefinitionRegistry == nil {
		return
	}

	r.Register(&Tool{
		Name:        "loop_definition_summary",
		Description: "Return a compact structured summary of the persistent loops-ng definition registry: generation, counts by source, operation, policy state, live runtime state, warning totals, and the known loop definition names.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: r.handleLoopDefinitionSummary,
	})

	r.Register(&Tool{
		Name:        "loop_definition_list",
		Description: "List persistent loop definitions with compact structured fields, authoring warnings, and optional filters. Use this to discover available service, background_task, and request_reply definitions, along with their effective policy and current live runtime state, before modifying them.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Optional case-insensitive substring match against loop name, task text, mission, and metadata values.",
				},
				"source": map[string]any{
					"type":        "string",
					"enum":        []string{"config", "overlay"},
					"description": "Optional exact source filter.",
				},
				"operation": map[string]any{
					"type":        "string",
					"enum":        []string{"request_reply", "background_task", "service"},
					"description": "Optional operation filter.",
				},
				"completion": map[string]any{
					"type":        "string",
					"enum":        []string{"return", "conversation", "channel", "none"},
					"description": "Optional completion filter.",
				},
				"policy_state": map[string]any{
					"type":        "string",
					"enum":        []string{"active", "paused", "inactive"},
					"description": "Optional exact effective policy-state filter.",
				},
				"runtime_state": map[string]any{
					"type":        "string",
					"description": "Optional runtime state filter such as not_running, pending, sleeping, waiting, processing, error, or stopped.",
				},
				"eligible": map[string]any{
					"type":        "string",
					"enum":        []string{"true", "false"},
					"description": "Optional effective eligibility filter based on the definition's current schedule/conditions.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum results to return (default %d, max %d).", defaultLoopDefinitionListLimit, maxLoopDefinitionListLimit),
				},
			},
		},
		Handler: r.handleLoopDefinitionList,
	})

	r.Register(&Tool{
		Name:        "loop_definition_get",
		Description: "Get one deep loop definition object from the persistent loops-ng definition registry by name, including authoring warnings and its current live runtime state when available.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Loop definition name.",
				},
			},
			"required": []string{"name"},
		},
		Handler: r.handleLoopDefinitionGet,
	})

	r.Register(&Tool{
		Name:        "loop_definition_lint",
		Description: "Lint one candidate persistent loop definition without saving it. Returns whether the spec is persistable, the effective runtime defaults that would apply, and non-fatal warnings for common service-loop authoring mistakes such as omitted cadence fields or delegation-first gating.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"spec": map[string]any{
					"type":        "object",
					"description": "Candidate loop definition spec to validate and inspect before saving.",
				},
			},
			"required": []string{"spec"},
		},
		Handler: r.handleLoopDefinitionLint,
	})

	r.Register(&Tool{
		Name:        "loop_definition_set",
		Description: "Create or replace one dynamic loop definition in the persistent loops-ng overlay. This cannot modify config-owned definitions. The saved definition view includes warnings for common service-loop authoring mistakes. The spec uses human-facing strings for durations and retrigger mode.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"spec": map[string]any{
					"type":        "object",
					"description": "Loop definition spec to persist into the overlay.",
				},
			},
			"required": []string{"spec"},
		},
		Handler: r.handleLoopDefinitionSet,
	})

	r.Register(&Tool{
		Name:        "loop_definition_delete",
		Description: "Delete one dynamic loop definition from the persistent loops-ng overlay. Config-owned definitions are immutable and cannot be deleted.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Loop definition name to delete from the overlay.",
				},
			},
			"required": []string{"name"},
		},
		Handler: r.handleLoopDefinitionDelete,
	})

	r.Register(&Tool{
		Name:        "loop_definition_set_policy",
		Description: "Set or clear runtime policy for one stored loop definition. Use this to activate, pause, or deactivate a definition without editing the definition itself. Changes persist across restart.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Loop definition name.",
				},
				"state": map[string]any{
					"type":        "string",
					"enum":        []string{"active", "paused", "inactive"},
					"description": "Effective policy state to apply.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Optional short operator reason.",
				},
				"clear_override": map[string]any{
					"type":        "boolean",
					"description": "When true, remove any explicit overlay for this definition and return it to its default state from the stored spec.",
				},
			},
			"required": []string{"name"},
		},
		Handler: r.handleLoopDefinitionSetPolicy,
	})

	r.Register(&Tool{
		Name:        "loop_definition_launch",
		Description: "Launch one stored loop definition by name using its persisted spec plus optional per-launch overrides. Use this instead of resending the full definition for request_reply, background_task, or on-demand service launches. Model routing, tool filtering, and iteration caps go in the top-level launch fields (model, allowed_tools, max_iterations, etc.) — NOT inside launch.metadata, which is opaque tagging only. When a launch uses conversation or channel completion and no explicit target is provided, the tool defaults to the current conversation or interactive channel context.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Loop definition name to launch.",
				},
				"launch": map[string]any{
					"type":        "object",
					"description": "Optional per-launch overrides applied on top of the stored definition spec. The stored spec is used automatically; these fields override selected aspects for this one launch.",
					"properties":  loopLaunchOverrideProperties(),
				},
			},
			"required": []string{"name"},
		},
		Handler: r.handleLoopDefinitionLaunch,
	})
}
