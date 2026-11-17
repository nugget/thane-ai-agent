package llm

import "strings"

// recoverTextCallsBesideNative reads calls a model wrote as JSON in the
// content of a response that also made structured calls: Qwen writes a
// loop's publish call as text and makes its sleep call natively, and
// the publish never ran. Only bare JSON is read, in the forms
// [TextToolActionRecoveredBesideNative] names: a fence, a tag or prose
// beside a structured call may be the model showing or describing a
// call rather than making one. Unoffered names are not taken up for
// repair here.
func recoverTextCallsBesideNative(resp *ChatResponse, validToolNames []string, profile ToolCallTextProfile) TextToolAction {
	content := strings.TrimSpace(resp.Message.Content)
	if content == "" || (content[0] != '{' && content[0] != '[') {
		return TextToolActionNone
	}
	native := make(map[string]bool, len(resp.Message.ToolCalls))
	for _, call := range resp.Message.ToolCalls {
		native[call.Function.Name] = true
	}
	var recovered []ToolCall
	for _, call := range parseNormalizedTextToolCalls(content, validToolNames, profile, false) {
		if !native[call.Function.Name] {
			recovered = append(recovered, call)
		}
	}
	if len(recovered) == 0 {
		return TextToolActionNone
	}
	resp.Message.ToolCalls = append(recovered, resp.Message.ToolCalls...)
	resp.Message.Content = ""
	return TextToolActionRecoveredBesideNative
}
