// Package delegate implements the thane_delegate meta-tool for split-model
// execution. A calling model delegates subtasks to cheaper/local models that
// run with minimal context and a filtered tool set.
package delegate

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/router"
)

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

	// MaxDuration is the maximum wall clock time for the delegation loop.
	MaxDuration time.Duration

	// ToolTimeout is the maximum time a single tool call may run before
	// being cancelled. Zero means defaultToolTimeout.
	ToolTimeout time.Duration
}

const (
	defaultMaxIter     = 15
	defaultMaxTokens   = 25000
	defaultMaxDuration = 90 * time.Second
	defaultToolTimeout = 30 * time.Second
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
				router.HintLocalOnly:    "true",
				router.HintQualityFloor: "5",
				router.HintPreferSpeed:  "true",
			},
			MaxIter:     defaultMaxIter,
			MaxTokens:   defaultMaxTokens,
			MaxDuration: defaultMaxDuration,
			ToolTimeout: defaultToolTimeout,
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
				router.HintLocalOnly:    "true",
				router.HintMission:      "device_control",
				router.HintQualityFloor: "4",
				router.HintPreferSpeed:  "true",
			},
			MaxIter:     defaultMaxIter,
			MaxTokens:   defaultMaxTokens,
			MaxDuration: defaultMaxDuration,
			ToolTimeout: defaultToolTimeout,
		},
	}
}

const generalSystemPrompt = `You are a task executor. Complete the assigned task using the available tools.
Report your findings clearly and concisely. Do not engage in conversation.
Focus on completing the task and returning results.

## Tool usage notes

- File tool paths must be literal filesystem paths. The only expansion supported is a leading ~/ to your home directory; other shell expansions ($HOME, $(whoami), ~user) do NOT work in file tools. Use absolute paths like /path/to/project or ~/Documents.
- For shell operations that need pipes, globs, or environment variable expansion, use the exec tool. The exec tool runs commands through a real shell where expansion works normally.
- On macOS (Darwin), common CLI tools differ from GNU/Linux. In particular: sed requires sed -i '' (empty string argument), date flags differ from GNU coreutils, and stat output format is different. Check the platform in Current Conditions before writing platform-specific commands.
- When a task involves finding files, prefer using the exec tool with find or ls rather than guessing paths with file tools.`

const haSystemPrompt = `You are a Home Assistant task executor. Use the available tools to query or control Home Assistant devices.

Entity IDs follow the pattern: domain.name (e.g., light.living_room, sensor.outdoor_temperature).
Use find_entity when the user describes a device by name rather than entity_id.

Report your findings clearly. Include entity states, values, and any relevant attributes.

## Tool usage notes

- Always use find_entity to look up the correct entity ID before calling get_state or control_device. Do not guess entity IDs — naming conventions vary across installations.
- Entity state values are strings. Compare with string values like "on", "off", "unavailable" — not booleans or numbers.
- When controlling devices, verify the entity exists and check its current state before sending commands. This avoids errors from typos or unavailable devices.
- Some entities expose useful data in their attributes (e.g., brightness, temperature, battery level). Include relevant attributes in your response when they add context.`
