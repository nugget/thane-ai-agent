// Package agent implements the core agent loop.
package agent

import (
	"context"
	"fmt"
	"log/slog"
)

// Message represents a chat message.
type Message struct {
	Role    string `json:"role"`    // system, user, assistant
	Content string `json:"content"`
}

// Request represents an incoming agent request.
type Request struct {
	Messages      []Message `json:"messages"`
	Model         string    `json:"model,omitempty"`
	ConversationID string   `json:"conversation_id,omitempty"`
}

// Response represents the agent's response.
type Response struct {
	Content      string `json:"content"`
	Model        string `json:"model"`
	FinishReason string `json:"finish_reason"`
}

// Loop is the core agent execution loop.
type Loop struct {
	logger *slog.Logger
}

// NewLoop creates a new agent loop.
func NewLoop(logger *slog.Logger) *Loop {
	return &Loop{logger: logger}
}

// Run executes one iteration of the agent loop.
// This is where the magic happens:
// 1. Assemble context
// 2. Plan actions
// 3. Execute tools
// 4. Generate response
func (l *Loop) Run(ctx context.Context, req *Request) (*Response, error) {
	l.logger.Info("agent loop started",
		"messages", len(req.Messages),
		"model", req.Model,
	)

	// Phase 1: Context Assembly
	// TODO: Load memory, recent events, entity state
	
	// Phase 2: Planning
	// TODO: Determine what information/actions are needed
	
	// Phase 3: Tool Execution
	// TODO: Execute tools (can be parallel)
	
	// Phase 4: Response Generation
	// For now, just echo back a placeholder
	lastMsg := ""
	for _, m := range req.Messages {
		if m.Role == "user" {
			lastMsg = m.Content
		}
	}

	resp := &Response{
		Content:      fmt.Sprintf("[Thane] Received: %q (agent loop placeholder)", lastMsg),
		Model:        req.Model,
		FinishReason: "stop",
	}

	l.logger.Info("agent loop completed",
		"response_len", len(resp.Content),
	)

	return resp, nil
}
