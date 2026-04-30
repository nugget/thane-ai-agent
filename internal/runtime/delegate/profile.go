// Package delegate implements the thane_now and thane_assign delegation
// tools for split-model execution. A calling model delegates subtasks to
// cheaper/local models that run with minimal context and a filtered tool
// set. thane_now is the synchronous front door (the caller blocks for the
// result); thane_assign is the async one-shot front door (the result is
// delivered back through the conversation/channel when the delegate
// completes).
package delegate

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
)

// Profile defines the configuration for a delegation context.
type Profile struct {
	// Name is the profile identifier (e.g., "general", "ha").
	Name string

	// Description is a human-readable summary for logging.
	Description string

	// DefaultTags are compatibility capability tags applied when the
	// caller does not request an explicit tag scope.
	DefaultTags []string

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

// builtinProfiles returns budget and routing defaults for delegate runs.
func builtinProfiles() map[string]*Profile {
	return map[string]*Profile{
		"general": {
			Name:        "general",
			Description: "General-purpose delegation defaults",
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
			Description: "Home Assistant budget and routing defaults",
			DefaultTags: []string{"ha"},
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
