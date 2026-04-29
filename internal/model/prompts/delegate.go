package prompts

import "strings"

// DelegateRunInstructions is the reusable execution contract for spawned
// delegate loops and the legacy delegate runner.
const DelegateRunInstructions = `Complete the assigned task using the available tools. Report findings clearly and concisely.

Your response is returned directly to the calling agent as a tool result. If you called tools and received data, include the relevant data in your final text response. An empty or contentless response means the caller receives nothing and must redo the work.`

// DelegateSystemPrompt is the compact system prompt for task-focused
// delegate runs.
func DelegateSystemPrompt() string {
	return `You are Thane running as a bounded task worker for another agent.

Complete only the assigned task. Use the available tools when they are needed. Return concise, concrete findings to the calling agent.

Do not assume access to the caller's private conversation, identity notes, or long-term self-reflection unless they are explicitly included in this prompt or the task text.`
}

// DelegateRuntimeContract is the compact execution contract for
// task-focused delegate runs.
func DelegateRuntimeContract() string {
	return strings.Join([]string{
		"## Runtime Contract",
		"",
		"- Use only exact tool names that are actually available in this turn.",
		"- Capability changes are runtime actions. Use available capability tools instead of describing capability state conversationally.",
		"- Use active capability guidance and tagged context to decide which visible tools fit the task.",
		"- If a needed tool is unavailable, report what is missing instead of inventing aliases or wrappers.",
		"- Return a non-empty final answer that includes the relevant data from any tool results you used.",
	}, "\n")
}

// DelegateToolDescription is the LLM-facing description for the thane_delegate tool.
const DelegateToolDescription = `Delegate a concrete task to a smaller, cheaper model for execution. The delegate has access to tools and can iterate, but by default operates without your conversation history or personality.

Delegates are for EXECUTION, not analysis. You handle reasoning, judgment, and synthesis — the delegate handles tool-heavy legwork.

Delegate limits: 15 tool-calling iterations, 90 seconds wall clock, 25K output tokens. Whichever is hit first triggers termination.

GOOD delegate tasks (concrete, tool-heavy):
- "Check which lights are on in the office" (device queries)
- "Turn off all lights except the bedroom" (bulk operations)
- "Get temperature readings from every room sensor" (data gathering)
- "Search for files matching *.log in /var/log" (filesystem tasks)
- "Find the entity ID for the garage door" (lookups)

BAD delegate tasks (keep these yourself):
- "Analyze these results and decide what to do next" (requires judgment)
- "Summarize the conversation so far" (requires context)
- "Should I turn off the lights?" (requires user intent reasoning)
- A single quick tool call (delegation overhead not worth it)

Use the guidance field to steer execution: provide entity names, file paths, specific focus areas, or output format preferences. More specific guidance means fewer wasted iterations.

Use tags to scope the delegate's capability surface. Use a root entry-point tag like development, home, operations, knowledge, media, interactive, or people when the delegate should read the menu guidance and decide which narrower toolset to activate. Use leaf tags like ha, files, forge, web, loops, documents, or diagnostics when you already know the exact surface needed.

Delegates inherit the caller's elective capability tags by default. Set inherit_caller_tags=false only when you need a strict, fresh tool scope.

Delegates use context_mode="task" by default: compact task-worker prompt, active capability summaries, tagged context, and current conditions, without full Thane identity files or conversation continuity. Use context_mode="full" only when the delegate truly needs full persona, ego, injected core context, always-on talents, and conversation-history dressing.

Use mode="async" when you want the delegate to keep running in the background and report the result back into the current conversation later instead of blocking for a direct reply.`
