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

Use the guidance field to steer execution: provide entity names, file paths, specific focus areas, or output format preferences. More specific guidance means fewer wasted iterations.`
