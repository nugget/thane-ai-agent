package main

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/agent"
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
// loop, and converts the result back to [loop.RunResponse].
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

	resp, err := a.agentLoop.Run(ctx, agentReq, nil)
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
