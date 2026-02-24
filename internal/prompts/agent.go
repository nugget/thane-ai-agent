package prompts

// EmptyResponseNudge is the prompt injected when the model returns no
// content after executing tool calls. It gives the model one more
// chance to produce a user-visible response.
const EmptyResponseNudge = "You executed tool calls but did not provide a response to the user. Please respond now."

// EmptyResponseFallback is the user-facing message returned when the
// model fails to produce content even after being nudged (or during
// max-iterations recovery).
const EmptyResponseFallback = "I processed your request but wasn't able to compose a response. Please try again."

// IllegalToolMessage is the tool result content injected when the model
// calls a tool that is not available in the current context. The message
// directs the model to delegate or inform the user rather than retrying.
// It is a format string accepting the tool name as its single argument.
const IllegalToolMessage = "Error: tool %q is not available to you. You do not have access to this tool. Delegate the task or inform the user."
