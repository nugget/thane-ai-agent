// Package delegate implements the thane_delegate meta-tool for split-model
// execution. A calling model delegates subtasks to cheaper/local models that
// run with minimal context and a filtered tool set.
package delegate

import "github.com/nugget/thane-ai-agent/internal/router"

// Profile defines the configuration for a delegation context.
type Profile struct {
	// Name is the profile identifier (e.g., "general", "ha").
	Name string

	// Description is a human-readable summary for logging.
	Description string

	// AllowedTools lists the tool names available to the delegate.
	// An empty list means all tools (minus thane_delegate).
	AllowedTools []string

	// SystemPrompt is the profile-specific system prompt for the delegate.
	SystemPrompt string

	// RouterHints are passed to the router for model selection.
	RouterHints map[string]string

	// MaxIter is the maximum number of tool-calling iterations.
	MaxIter int

	// MaxTokens is the maximum cumulative output tokens before budget exhaustion.
	MaxTokens int
}

const (
	defaultMaxIter   = 8
	defaultMaxTokens = 25000
)

// builtinProfiles returns the MVP delegation profiles.
func builtinProfiles() map[string]*Profile {
	return map[string]*Profile{
		"general": {
			Name:         "general",
			Description:  "General-purpose delegation with all tools",
			AllowedTools: nil, // all tools minus thane_delegate
			SystemPrompt: generalSystemPrompt,
			RouterHints: map[string]string{
				router.HintLocalOnly: "true",
			},
			MaxIter:   defaultMaxIter,
			MaxTokens: defaultMaxTokens,
		},
		"ha": {
			Name:        "ha",
			Description: "Home Assistant device queries and control",
			AllowedTools: []string{
				"get_state",
				"list_entities",
				"control_device",
				"call_service",
				"find_entity",
			},
			SystemPrompt: haSystemPrompt,
			RouterHints: map[string]string{
				router.HintLocalOnly: "true",
				router.HintMission:   "device_control",
			},
			MaxIter:   defaultMaxIter,
			MaxTokens: defaultMaxTokens,
		},
	}
}

const generalSystemPrompt = `You are a task executor. Complete the assigned task using the available tools.
Report your findings clearly and concisely. Do not engage in conversation.
Focus on completing the task and returning results.`

const haSystemPrompt = `You are a Home Assistant task executor. Use the available tools to query or control Home Assistant devices.

Entity IDs follow the pattern: domain.name (e.g., light.living_room, sensor.outdoor_temperature).
Use find_entity when the user describes a device by name rather than entity_id.

Report your findings clearly. Include entity states, values, and any relevant attributes.`
