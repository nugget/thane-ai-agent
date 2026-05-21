// Package agentctx defines per-Run context types shared by the agent
// loop and the providers it pulls context from. Kept separate from
// internal/runtime/agent so context-producing packages (state,
// channels, integrations, etc.) can satisfy the provider contract
// without depending on the agent package itself, which would create
// import cycles.
package agentctx

import (
	"context"
	"fmt"
)

// PromptMode names the system-prompt shape used for an agent run.
type PromptMode string

const (
	// PromptModeFull is the default Thane prompt: persona, ego,
	// configured inject files, talents, generated runtime context, and
	// conversation history.
	PromptModeFull PromptMode = "full"

	// PromptModeTask is a compact worker prompt for bounded child
	// tasks. It preserves tool contracts and tag-scoped context while
	// omitting full identity and continuity material.
	PromptModeTask PromptMode = "task"
)

type promptModeKey struct{}

// ContextRequest carries everything a context provider might need
// during system-prompt assembly. Always-on providers ignore
// ActiveTags; tagged providers ignore UserMessage when they don't
// need it; the semantic-search providers (contacts, knowledge
// subjects, archive prewarm) read UserMessage to surface relevant
// content.
//
// IncludeAlways gates the always-on bucket inside the assembler;
// it's set true for main-loop runs (include presence, episodic
// memory, working memory, notification history, etc.) and false for
// delegate runs that should see only tag-scoped context appropriate
// to the bounded child task.
type ContextRequest struct {
	UserMessage   string
	ActiveTags    map[string]bool
	IncludeAlways bool
}

// ContextBucket names the model-facing section class for generated
// runtime context. Buckets let prompt assembly keep durable guidance,
// continuity material, request-related leads, and live state in stable
// positions instead of flattening everything into one generic context
// blob.
type ContextBucket string

const (
	// ContextBucketTaggedGuidance contains tag-scoped doctrine or
	// instructions loaded because a capability tag is active.
	ContextBucketTaggedGuidance ContextBucket = "tagged_guidance"

	// ContextBucketContinuity contains conversation, channel, working
	// memory, and other experiential state that helps the model preserve
	// continuity across turns.
	ContextBucketContinuity ContextBucket = "continuity_context"

	// ContextBucketRelated contains retrieved or inferred pointers that
	// may be relevant to the current request. Results here should stay
	// lightweight and clearly optional.
	ContextBucketRelated ContextBucket = "related_context"

	// ContextBucketLiveState contains current operational or world state,
	// such as entity snapshots, recent state changes, presence, or
	// service health.
	ContextBucketLiveState ContextBucket = "live_state"
)

// Title returns the markdown heading used for b in system prompts.
func (b ContextBucket) Title() string {
	switch b {
	case ContextBucketTaggedGuidance:
		return "Tagged Guidance"
	case ContextBucketContinuity:
		return "Continuity Context"
	case ContextBucketRelated:
		return "Related Context"
	case ContextBucketLiveState:
		return "Live State"
	default:
		return "Context"
	}
}

// Valid reports whether b is one of the known prompt context buckets.
func (b ContextBucket) Valid() bool {
	switch b {
	case ContextBucketTaggedGuidance,
		ContextBucketContinuity,
		ContextBucketRelated,
		ContextBucketLiveState:
		return true
	default:
		return false
	}
}

// OrDefault returns b when it is recognized, otherwise fallback.
func (b ContextBucket) OrDefault(fallback ContextBucket) ContextBucket {
	if b.Valid() {
		return b
	}
	if fallback.Valid() {
		return fallback
	}
	return ContextBucketContinuity
}

// ContextSection is one assembled context bucket ready for prompt
// rendering. Content excludes the heading; prompt assembly owns section
// placement and retained section metadata.
type ContextSection struct {
	Bucket  ContextBucket
	Title   string
	Content string
}

// ParsePromptMode validates a wire value and returns the corresponding
// prompt mode. An empty value resolves to the full default.
func ParsePromptMode(value string) (PromptMode, error) {
	mode := PromptMode(value)
	if mode == "" {
		return PromptModeFull, nil
	}
	if mode.Valid() {
		return mode, nil
	}
	return "", fmt.Errorf("prompt_mode must be one of [full, task], got %q", value)
}

// Valid reports whether m is a supported prompt mode.
func (m PromptMode) Valid() bool {
	switch m {
	case "", PromptModeFull, PromptModeTask:
		return true
	default:
		return false
	}
}

// OrDefault returns m when set, otherwise the full prompt mode.
func (m PromptMode) OrDefault() PromptMode {
	if m == "" {
		return PromptModeFull
	}
	return m
}

// WithPromptMode annotates ctx with the system-prompt shape for the run.
func WithPromptMode(ctx context.Context, mode PromptMode) context.Context {
	return context.WithValue(ctx, promptModeKey{}, mode.OrDefault())
}

// PromptModeFromContext returns the requested prompt mode, defaulting to
// full when no request-scoped mode has been set.
func PromptModeFromContext(ctx context.Context) PromptMode {
	if mode, ok := ctx.Value(promptModeKey{}).(PromptMode); ok && mode.Valid() {
		return mode.OrDefault()
	}
	return PromptModeFull
}
