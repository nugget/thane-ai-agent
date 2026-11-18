package iterate

import "github.com/nugget/thane-ai-agent/internal/model/promptfmt"

// modelFacingToolError is the error text a model reads in a failed
// tool call's result. A tool that wraps its error in a
// [promptfmt.ModelFacingError] has written that text for the model, and
// it is shown in place of the error's own text, which can carry file
// paths and raw upstream responses. Any other error reads as its full
// text, exactly as before. Logs and tool-call records always keep the
// error's own text.
func modelFacingToolError(err error) string {
	if text, ok := promptfmt.ModelFacingText(err); ok {
		return text
	}
	return err.Error()
}
