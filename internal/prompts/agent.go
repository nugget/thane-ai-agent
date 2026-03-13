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

// TimeoutRecoverySystem is the system prompt for the recovery model
// when the primary model times out after completing tool calls.
const TimeoutRecoverySystem = "You are summarizing work completed by a previous assistant that timed out before it could respond. Provide a brief, helpful summary to the user."

// TimeoutRecoveryFallback is the user-facing message returned when
// the primary model times out but the recovery model is unavailable
// or also fails. It is a format string accepting the total tool call
// count and a comma-separated tool list as arguments.
const TimeoutRecoveryFallback = "I completed %d tool call(s) (%s) but the request timed out before I could compose a response. Please check the results or try again."

// TimeoutRecoveryEmpty is the user-facing message returned when the
// recovery model produces an empty response.
const TimeoutRecoveryEmpty = "The request timed out after completing tool calls. Please check the results."
