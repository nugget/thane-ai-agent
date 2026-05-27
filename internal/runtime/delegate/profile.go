// Package delegate implements the thane_now and thane_assign delegation
// tools for split-model execution. A calling model delegates subtasks to
// cheaper/local models that run with minimal context and a filtered tool
// set. thane_now is the synchronous front door (the caller blocks for the
// result); thane_assign is the async one-shot front door (the result is
// delivered back through the conversation/channel when the delegate
// completes).
//
// # Tag scope and inheritance
//
// A delegate's tag scope is composed from three sources, in priority
// order:
//
//  1. Explicit tags passed to thane_now / thane_assign. When the
//     caller names tags, those tags define the scope and the run
//     policy's DefaultTags are NOT applied — the explicit list is
//     authoritative. This is the "I know exactly what tools this
//     subtask needs" path.
//
//  2. Elective tags inherited from the caller's context when the
//     caller did not provide explicit tags. Inheritance is the
//     default — a delegate working on the same conversation thread
//     should see the same tag-loaded knowledge as the orchestrator.
//
//  3. The run policy's DefaultTags, merged in only when neither
//     explicit nor inherited tags are present. These exist to give a
//     bare invocation (no caller context, no explicit tags) a
//     sensible default tool surface — e.g., the `ha` policy adds
//     `[ha]` so a `thane_now` aimed at HA work gets HA tools without
//     the caller having to say so.
//
// Tags that never propagate to delegates: `message_channel` and
// `owner`. These are runtime-asserted trust tags meaningful only in
// the context they were set; they must be re-asserted (or not) by
// the delegate's own runtime context, not inherited.
//
// Core tags (those operator-pinned via config) always load regardless
// of which path above contributed the scope.
//
// # Vocabulary
//
// "Profile" appears in operator-facing surfaces (YAML key
// `delegate.profiles.<name>`, log keys, JSON wire field `profile`,
// model-facing result strings `profile=...`) and refers to the
// operator-named entry. Internally that entry is a [RunPolicy] —
// the rename in #959 disambiguated three concepts that previously
// shared the word "profile": delegate run policies (this type),
// [router.LoopProfile] (loop/wake routing shape), and user-facing
// `thane:*` virtual model names.
package delegate

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
)

// RunPolicy bounds and routes a single delegated run: budget caps,
// default tags, and router hints. Operator-facing config still calls
// these "profiles" (the YAML key is `delegate.profiles`, the wire
// JSON field is `profile`, the log key is `profile`) — the rename is
// internal-only and clarifies that the type expresses how a delegated
// run is bounded and routed, not a free-form configuration profile.
//
// Profiles vs. delegate run policies vs. virtual models — three
// distinct concepts that used to share the word "profile":
//   - virtual model: user-facing `thane:*` model name selected by
//     clients (routes via [router.VirtualModel]).
//   - [router.LoopProfile]: loop/wake routing shape (model, mission,
//     quality floor, etc.).
//   - RunPolicy (this type): internal delegate run policy.
type RunPolicy struct {
	// Name is the policy identifier (e.g., "general", "ha"). Matches
	// the YAML key under `delegate.profiles.<name>` and the value
	// surfaced as `profile=<name>` in model-facing result strings.
	Name string

	// Description is a human-readable summary for logging.
	Description string

	// DefaultTags are the policy's fallback tag scope, merged in only
	// when the caller did not provide explicit tags AND no context
	// tags were inherited. The motivating example is the `ha` policy:
	// a bare `thane_now` aimed at HA work gets `[ha]` so HA tools
	// load even when the caller didn't name them. See the package
	// "Tag scope and inheritance" comment for the full composition
	// rule.
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

// builtinRunPolicies returns budget and routing defaults for delegate runs.
func builtinRunPolicies() map[string]*RunPolicy {
	return map[string]*RunPolicy{
		"general": {
			Name:        "general",
			Description: "General-purpose delegation defaults",
			RouterHints: map[string]string{
				router.FactorLocalOnly:    "true",
				router.FactorQualityFloor: "5",
				router.FactorPreferSpeed:  "true",
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
				router.FactorLocalOnly:    "true",
				router.FactorMission:      "device_control",
				router.FactorQualityFloor: "4",
				router.FactorPreferSpeed:  "true",
			},
			MaxIter:     defaultMaxIter,
			MaxTokens:   defaultMaxTokens,
			MaxDuration: defaultMaxDuration,
			ToolTimeout: defaultToolTimeout,
		},
	}
}
