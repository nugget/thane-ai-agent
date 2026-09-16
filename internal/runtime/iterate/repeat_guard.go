package iterate

import "github.com/nugget/thane-ai-agent/internal/model/prompts"

// repeatedCallMessage is the tool result that stands in for a call the
// repeat guard refused. [Config.ReplyAwaited] selects the wording: only
// a turn someone is waiting on is told to stop calling tools and answer.
// Every other turn gets the neutral text, which is also the default, so
// a caller that says nothing can never tell a loop to abandon a write.
func (c Config) repeatedCallMessage(toolName string, repeats int) string {
	if c.ReplyAwaited {
		return prompts.RepeatedToolCallInteractive(toolName, repeats)
	}
	return prompts.RepeatedToolCall(toolName, repeats)
}
