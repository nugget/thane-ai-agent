package main

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/events"
	"github.com/nugget/thane-ai-agent/internal/loop"
)

// loopAdapter bridges [loop.Runner] to [*agent.Loop], converting between
// the loop package's request/response types and the agent package's
// types. It lives in cmd/thane to avoid a circular import between the
// loop and agent packages.
type loopAdapter struct {
	agentLoop *agent.Loop
}

// Run converts a [loop.RunRequest] to [agent.Request], calls the agent
// loop, and converts the result back to [loop.RunResponse]. When
// [loop.RunRequest.OnProgress] is set, streaming events from the agent
// (tool calls, LLM responses) are forwarded through it so the loop
// infrastructure can publish them on the event bus.
func (a *loopAdapter) Run(ctx context.Context, req loop.RunRequest, _ loop.StreamCallback) (*loop.RunResponse, error) {
	// Convert messages.
	msgs := make([]agent.Message, len(req.Messages))
	for i, m := range req.Messages {
		msgs[i] = agent.Message{Role: m.Role, Content: m.Content}
	}

	agentReq := &agent.Request{
		ConversationID: req.ConversationID,
		Messages:       msgs,
		ExcludeTools:   req.ExcludeTools,
		SkipTagFilter:  req.SkipTagFilter,
		Hints:          req.Hints,
	}

	// Build an agent streaming callback that relays tool and LLM
	// events through the loop's OnProgress callback.
	var agentStream agent.StreamCallback
	if req.OnProgress != nil {
		agentStream = func(e agent.StreamEvent) {
			switch e.Kind {
			case agent.KindLLMStart:
				if e.Response != nil {
					data := map[string]any{
						"model": e.Response.Model,
					}
					// Forward enrichment data from agent (tokens, tools, router).
					for k, v := range e.Data {
						data[k] = v
					}
					req.OnProgress(events.KindLoopLLMStart, data)
				}
			case agent.KindToolCallStart:
				if e.ToolCall != nil {
					req.OnProgress(events.KindLoopToolStart, map[string]any{
						"tool": e.ToolCall.Function.Name,
					})
				}
			case agent.KindToolCallDone:
				data := map[string]any{"tool": e.ToolName}
				if e.ToolError != "" {
					data["error"] = e.ToolError
				}
				req.OnProgress(events.KindLoopToolDone, data)
			case agent.KindLLMResponse:
				if e.Response != nil {
					req.OnProgress(events.KindLoopLLMResponse, map[string]any{
						"model":         e.Response.Model,
						"input_tokens":  e.Response.InputTokens,
						"output_tokens": e.Response.OutputTokens,
					})
				}
			}
		}
	}

	resp, err := a.agentLoop.Run(ctx, agentReq, agentStream)
	if err != nil {
		return nil, fmt.Errorf("agent loop: %w", err)
	}

	return &loop.RunResponse{
		Content:       resp.Content,
		Model:         resp.Model,
		InputTokens:   resp.InputTokens,
		OutputTokens:  resp.OutputTokens,
		ContextWindow: a.agentLoop.GetContextWindow(),
		ToolsUsed:     resp.ToolsUsed,
	}, nil
}
