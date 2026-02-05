// Package agent implements the core agent loop.
package agent

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/memory"
)

// Message represents a chat message.
type Message struct {
	Role    string `json:"role"`    // system, user, assistant
	Content string `json:"content"`
}

// Request represents an incoming agent request.
type Request struct {
	Messages       []Message `json:"messages"`
	Model          string    `json:"model,omitempty"`
	ConversationID string    `json:"conversation_id,omitempty"`
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
	memory *memory.Store
}

// NewLoop creates a new agent loop.
func NewLoop(logger *slog.Logger, mem *memory.Store) *Loop {
	return &Loop{
		logger: logger,
		memory: mem,
	}
}

// Run executes one iteration of the agent loop.
// This is where the magic happens:
// 1. Assemble context (load memory)
// 2. Plan actions
// 3. Execute tools
// 4. Generate response
// 5. Store in memory
func (l *Loop) Run(ctx context.Context, req *Request) (*Response, error) {
	convID := req.ConversationID
	if convID == "" {
		convID = "default"
	}

	l.logger.Info("agent loop started",
		"conversation", convID,
		"messages", len(req.Messages),
		"model", req.Model,
	)

	// Phase 1: Context Assembly
	// Load existing conversation history
	history := l.memory.GetMessages(convID)
	l.logger.Debug("loaded history", "count", len(history))

	// Add incoming messages to memory
	for _, m := range req.Messages {
		l.memory.AddMessage(convID, m.Role, m.Content)
	}

	// Phase 2: Planning
	// TODO: Determine what information/actions are needed
	
	// Phase 3: Tool Execution
	// TODO: Execute tools (can be parallel)
	
	// Phase 4: Response Generation
	// For now, echo back with context awareness
	lastMsg := ""
	for _, m := range req.Messages {
		if m.Role == "user" {
			lastMsg = m.Content
		}
	}

	// Show we're tracking history
	historyCount := len(history)
	resp := &Response{
		Content:      fmt.Sprintf("[Thane] Received: %q (history: %d messages)", lastMsg, historyCount),
		Model:        req.Model,
		FinishReason: "stop",
	}

	// Phase 5: Store response in memory
	l.memory.AddMessage(convID, "assistant", resp.Content)

	l.logger.Info("agent loop completed",
		"conversation", convID,
		"history", historyCount+len(req.Messages)+1,
	)

	return resp, nil
}

// MemoryStats returns current memory statistics.
func (l *Loop) MemoryStats() map[string]any {
	return l.memory.Stats()
}
