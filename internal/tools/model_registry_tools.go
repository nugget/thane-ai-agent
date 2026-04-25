package tools

import (
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/model/models"
	routepkg "github.com/nugget/thane-ai-agent/internal/model/router"
)

const (
	defaultModelRegistryListLimit = 25
	maxModelRegistryListLimit     = 200
)

// ModelRegistryToolDeps wires the live model-registry and router state
// into the tool registry so model-facing operator tools can act on the
// same runtime machinery as the API surface.
type ModelRegistryToolDeps struct {
	Registry                *models.Registry
	Router                  *routepkg.Router
	SyncRouter              func()
	PersistDeploymentPolicy func(string, models.DeploymentPolicy) error
	DeleteDeploymentPolicy  func(string) error
	PersistResourcePolicy   func(string, models.ResourcePolicy) error
	DeleteResourcePolicy    func(string) error
}

type modelRegistryResourceView struct {
	models.RegistryResourceSnapshot
	DeploymentCount int                      `json:"deployment_count,omitempty"`
	Health          *routepkg.ResourceHealth `json:"health,omitempty"`
}

type modelRegistryDeploymentView struct {
	models.RegistryDeploymentSnapshot
	Stats *routepkg.DeploymentStats `json:"stats,omitempty"`
}

// ConfigureModelRegistryTools stores the runtime dependencies needed by
// the model-registry tool family and registers the tools.
func (r *Registry) ConfigureModelRegistryTools(deps ModelRegistryToolDeps) {
	r.modelRegistry = deps.Registry
	r.modelRouter = deps.Router
	r.modelRegistrySyncRouter = deps.SyncRouter
	r.persistModelRegistryPolicy = deps.PersistDeploymentPolicy
	r.deletePersistedModelRegistryPolicy = deps.DeleteDeploymentPolicy
	r.persistModelRegistryResourcePolicy = deps.PersistResourcePolicy
	r.deletePersistedModelRegistryResourcePolicy = deps.DeleteResourcePolicy
	r.registerModelRegistryTools()
}

func (r *Registry) registerModelRegistryTools() {
	if r.modelRegistry == nil {
		return
	}

	r.Register(&Tool{
		Name:        "model_registry_summary",
		Description: "Return a compact structured summary of the live Thane model registry: generation, counts, defaults, degraded resources, cooldowns, policy state totals, and promoted discovered deployments. Use this first to understand the current runtime model landscape cheaply.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: r.handleModelRegistrySummary,
	})

	r.Register(&Tool{
		Name:        "model_registry_list",
		Description: "List live model-registry resources or deployments with compact structured fields and optional filters. Use this to find image-capable, tool-capable, routable, promoted, or policy-constrained models before making changes.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{"deployments", "resources"},
					"description": "What to list. Default is deployments.",
				},
				"query": map[string]any{
					"type":        "string",
					"description": "Optional case-insensitive substring match against IDs, model names, provider/resource names, and URLs.",
				},
				"provider": map[string]any{
					"type":        "string",
					"description": "Optional provider filter such as ollama, lmstudio, anthropic, or openai.",
				},
				"resource": map[string]any{
					"type":        "string",
					"description": "Optional resource filter such as spark, mirror, deepslate, centro, or pocket.",
				},
				"supports_tools": map[string]any{
					"type":        "boolean",
					"description": "Optional exact filter on tool support.",
				},
				"supports_images": map[string]any{
					"type":        "boolean",
					"description": "Optional exact filter on image/multimodal support.",
				},
				"routable": map[string]any{
					"type":        "boolean",
					"description": "Optional exact filter on automatic routing eligibility (deployments only).",
				},
				"policy_state": map[string]any{
					"type":        "string",
					"enum":        []string{"active", "inactive", "flagged"},
					"description": "Optional exact policy-state filter.",
				},
				"source": map[string]any{
					"type":        "string",
					"enum":        []string{"config", "discovered"},
					"description": "Optional deployment source filter.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum results to return (default %d, max %d).", defaultModelRegistryListLimit, maxModelRegistryListLimit),
				},
			},
		},
		Handler: r.handleModelRegistryList,
	})

	r.Register(&Tool{
		Name:        "model_registry_get",
		Description: "Get one deep live registry object for a specific resource or deployment, including policy state, related health or telemetry, and associated deployments or resource metadata.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"resource": map[string]any{
					"type":        "string",
					"description": "Resource ID such as spark, mirror, deepslate, centro, or pocket.",
				},
				"deployment": map[string]any{
					"type":        "string",
					"description": "Deployment ID such as spark/gpt-oss:20b or deepslate/google/gemma-3-4b.",
				},
			},
		},
		Handler: r.handleModelRegistryGet,
	})

	r.Register(&Tool{
		Name:        "model_resource_set_policy",
		Description: "Set or clear runtime operator policy for one model resource. Use this to enable, disable, or flag a whole resource such as deepslate, centro, pocket, or spark. Changes persist across restart.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"resource": map[string]any{
					"type":        "string",
					"description": "Resource ID to change.",
				},
				"state": map[string]any{
					"type":        "string",
					"enum":        []string{"active", "inactive", "flagged"},
					"description": "Policy state to apply. Use inactive to disable the resource from automatic routing and explicit model use.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Optional short operator reason such as 'office hours' or 'Aimee is using this machine'.",
				},
				"clear_override": map[string]any{
					"type":        "boolean",
					"description": "When true, remove any explicit overlay for this resource and return it to default policy behavior.",
				},
			},
			"required": []string{"resource"},
		},
		Handler: r.handleModelResourceSetPolicy,
	})

	r.Register(&Tool{
		Name:        "model_deployment_set_policy",
		Description: "Set or clear runtime operator policy for one model deployment. Use this to flag a deployment, make it inactive, or promote/demote a discovered deployment via routable=true or false. Changes persist across restart.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"deployment": map[string]any{
					"type":        "string",
					"description": "Deployment ID such as spark/gpt-oss:20b or deepslate/google/gemma-3-4b.",
				},
				"state": map[string]any{
					"type":        "string",
					"enum":        []string{"active", "inactive", "flagged"},
					"description": "Optional policy state to apply.",
				},
				"routable": map[string]any{
					"type":        "boolean",
					"description": "Optional explicit routing override. Set true to promote a discovered deployment into automatic routing or false to demote it back to explicit-only.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Optional short operator reason such as 'promote vision model' or 'manual quarantine'.",
				},
				"clear_override": map[string]any{
					"type":        "boolean",
					"description": "When true, remove any explicit overlay for this deployment and return it to default behavior.",
				},
			},
			"required": []string{"deployment"},
		},
		Handler: r.handleModelDeploymentSetPolicy,
	})

	r.Register(&Tool{
		Name:        "model_route_explain",
		Description: "Explain how Thane would route a hypothetical request right now using the live registry, live learned experience, and live transient cooldown state. Returns the chosen deployment plus rejected candidates and reasons without mutating router stats or audit history.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Representative prompt or task description. Used for complexity and intent analysis.",
				},
				"context_size": map[string]any{
					"type":        "integer",
					"description": "Estimated context size in tokens.",
				},
				"needs_tools": map[string]any{
					"type":        "boolean",
					"description": "Whether tool calling is required.",
				},
				"needs_streaming": map[string]any{
					"type":        "boolean",
					"description": "Whether streaming support is required.",
				},
				"needs_images": map[string]any{
					"type":        "boolean",
					"description": "Whether image/multimodal input is required.",
				},
				"tool_count": map[string]any{
					"type":        "integer",
					"description": "Optional explicit tool count. Defaults to the current tool-registry size.",
				},
				"priority": map[string]any{
					"type":        "string",
					"enum":        []string{"interactive", "background"},
					"description": "Latency profile. Default interactive.",
				},
				"mission": map[string]any{
					"type":        "string",
					"description": "Optional routing mission hint such as conversation, automation, background, metacognitive, or device_control.",
				},
				"channel": map[string]any{
					"type":        "string",
					"description": "Optional channel hint such as api, ollama, homeassistant, or voice.",
				},
				"quality_floor": map[string]any{
					"type":        "integer",
					"description": "Optional minimum quality rating hint.",
				},
				"local_only": map[string]any{
					"type":        "boolean",
					"description": "Optional local-only routing hint.",
				},
				"prefer_speed": map[string]any{
					"type":        "boolean",
					"description": "Optional prefer-speed routing hint.",
				},
				"model_preference": map[string]any{
					"type":        "string",
					"description": "Optional soft preferred deployment or model reference hint.",
				},
				"hints": map[string]any{
					"type":                 "object",
					"additionalProperties": true,
					"description":          "Optional raw routing hints to merge with the explicit hint fields above.",
				},
			},
		},
		Handler: r.handleModelRouteExplain,
	})
}
