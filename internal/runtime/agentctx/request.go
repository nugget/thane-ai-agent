// Package agentctx defines per-Run context types shared by the agent
// loop and the providers it pulls context from. Kept separate from
// internal/runtime/agent so context-producing packages (state,
// channels, integrations, etc.) can satisfy the provider contract
// without depending on the agent package itself, which would create
// import cycles.
package agentctx

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
