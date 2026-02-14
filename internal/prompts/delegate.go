package prompts

// DelegateToolDescription is the LLM-facing description for the thane_delegate tool.
const DelegateToolDescription = `Delegate a task to a smaller, cheaper model for execution. Use when the task is clear enough to describe in plain English and doesn't require your full context or personality.

Good candidates for delegation:
- Checking device states ("What lights are on in the office?")
- Bulk operations ("Turn off all lights except the bedroom")
- Data gathering ("Get temperature from every room sensor")
- Simple lookups and searches

Keep using your own tools when:
- The task needs conversation history or personality
- You need to reason about the results before acting
- The task is a single quick tool call (delegation overhead not worth it)`
