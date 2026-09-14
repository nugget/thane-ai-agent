package prompts

import "fmt"

// RepeatedToolCallInteractive is the tool result the iteration engine
// returns in place of a call that repeats an earlier call in the same
// turn with identical arguments, on a turn whose final text is a reply
// someone is waiting for: a channel message or an API request. Telling
// the model to stop and answer is right there, because the person gets
// a reply either way and another identical call cannot change what it
// returns.
func RepeatedToolCallInteractive(toolName string, repeats int) string {
	return fmt.Sprintf("Error: tool '%s' has been called %d times with the same arguments. Stop calling tools and provide your response to the user.", toolName, repeats)
}

// RepeatedToolCall is the same refusal for every other turn: loop
// wakes, delegates, and anything else nobody is waiting to read. There
// is no user to answer there, and "stop calling tools" read by a loop
// in the middle of a durable write means "abandon the write" — which is
// how a production loop ended a wake reporting success with its
// document unpublished. So this text says only what the runtime did and
// will do: the call was not run, identical calls stay refused for the
// rest of the turn, and the way forward is the earlier result or changed
// arguments. It makes no claim that a repeat would return the same
// result, which is false for any read of changing state.
func RepeatedToolCall(toolName string, repeats int) string {
	return fmt.Sprintf("Error: %s was not run: it repeats an earlier %s call in this turn with identical arguments (%d identical calls), and identical calls are refused for the rest of this turn. If the earlier call succeeded, work from the result it returned; if it failed, change the arguments its error names before calling %s again.", toolName, toolName, repeats, toolName)
}

// UnchangedArgumentsNote is appended to a failed tool result when the
// call resent values the previous failed call to the same tool in this
// turn also carried, and both errors name those arguments. keys is the
// rendered list of those top-level argument names. It fires from the
// second rejection, well before the whole-argument repeat guard, because
// a model that resends a rejected projection unchanged has usually not
// noticed which of its arguments the error was about.
func UnchangedArgumentsNote(toolName, keys string) string {
	return fmt.Sprintf("Unchanged since the previous failed %s call in this turn, and named by both its error and this one: %s. When an error is about a value, resending that value unchanged gets the same error, so change these before calling %s again.", toolName, keys, toolName)
}
