package agent

import "github.com/nugget/thane-ai-agent/internal/platform/events"

// maxProgressToolResultLen is the maximum tool result length forwarded
// via progress events. Longer results are truncated with an ellipsis.
const maxProgressToolResultLen = 2000

// BuildProgressStream converts a loop progress func into a
// [StreamCallback] that forwards in-flight LLM and tool events to the
// event bus for dashboard visibility. Returns nil if progressFn is nil.
//
// This is the canonical bridge between the agent's streaming events and
// the loop infrastructure's progress reporting. Handlers that call
// [Loop.Run] inside a handler-only loop should use this to wire the
// loop's [loop.ProgressFunc] into the agent stream:
//
//	stream := agent.BuildProgressStream(loop.ProgressFunc(hCtx))
//	resp, err := runner.Run(hCtx, req, stream)
func BuildProgressStream(progressFn func(string, map[string]any)) StreamCallback {
	if progressFn == nil {
		return nil
	}
	return func(e StreamEvent) {
		switch e.Kind {
		case KindLLMStart:
			if e.Response != nil {
				data := map[string]any{
					"model": e.Response.Model,
				}
				for k, v := range e.Data {
					data[k] = v
				}
				progressFn(events.KindLoopLLMStart, data)
			}
		case KindToolCallStart:
			if e.ToolCall != nil {
				data := map[string]any{
					"tool": e.ToolCall.Function.Name,
				}
				if len(e.ToolCall.Function.Arguments) > 0 {
					data["args"] = e.ToolCall.Function.Arguments
				}
				progressFn(events.KindLoopToolStart, data)
			}
		case KindToolCallDone:
			data := map[string]any{"tool": e.ToolName}
			if e.ToolError != "" {
				data["error"] = e.ToolError
			}
			if e.ToolResult != "" {
				r := e.ToolResult
				if len(r) > maxProgressToolResultLen {
					r = r[:maxProgressToolResultLen] + "…"
				}
				data["result"] = r
			}
			progressFn(events.KindLoopToolDone, data)
		case KindLLMResponse:
			if e.Response != nil {
				progressFn(events.KindLoopLLMResponse, map[string]any{
					"model":         e.Response.Model,
					"input_tokens":  e.Response.InputTokens,
					"output_tokens": e.Response.OutputTokens,
				})
			}
		}
	}
}
