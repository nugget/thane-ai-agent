package prompts

// DelegateToolDescription is the LLM-facing description for the thane_delegate tool.
const DelegateToolDescription = `Delegate a concrete task to a smaller, cheaper model for execution. The delegate has access to tools and can iterate, but operates without your conversation history or personality.

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

Delegates inherit the caller's elective capability tags by default. Set inherit_caller_tags=false only when you need a strict, fresh tool scope.

Use mode="async" when you want the delegate to keep running in the background and report the result back into the current conversation later instead of blocking for a direct reply.`
