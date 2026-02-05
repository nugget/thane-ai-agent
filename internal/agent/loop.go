// Package agent implements the core agent loop.
package agent

import (
	"context"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/llm"
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
	llm    *llm.OllamaClient
	model  string
}

// NewLoop creates a new agent loop.
func NewLoop(logger *slog.Logger, mem *memory.Store, ollamaURL, defaultModel string) *Loop {
	return &Loop{
		logger: logger,
		memory: mem,
		llm:    llm.NewOllamaClient(ollamaURL),
		model:  defaultModel,
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
	
	// Phase 4: Response Generation via LLM
	// Build messages for LLM (history + current)
	var llmMessages []llm.Message
	
	// Add system prompt
	llmMessages = append(llmMessages, llm.Message{
		Role:    "system",
		Content: "You are Thane, an autonomous AI agent for Home Assistant. You help users manage their smart home. Be concise and helpful.",
	})
	
	// Add conversation history
	for _, m := range history {
		llmMessages = append(llmMessages, llm.Message{
			Role:    m.Role,
			Content: m.Content,
		})
	}
	
	// Add current request messages
	for _, m := range req.Messages {
		llmMessages = append(llmMessages, llm.Message{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	// Select model
	model := req.Model
	if model == "" || model == "thane" {
		model = l.model
	}

	// Call LLM
	l.logger.Info("calling LLM", "model", model, "messages", len(llmMessages))
	llmResp, err := l.llm.Chat(ctx, model, llmMessages)
	if err != nil {
		l.logger.Error("LLM call failed", "error", err)
		return nil, err
	}

	resp := &Response{
		Content:      llmResp.Message.Content,
		Model:        model,
		FinishReason: "stop",
	}

	// Phase 5: Store response in memory
	l.memory.AddMessage(convID, "assistant", resp.Content)

	l.logger.Info("agent loop completed",
		"conversation", convID,
		"history", len(history)+len(req.Messages)+1,
	)

	return resp, nil
}

// MemoryStats returns current memory statistics.
func (l *Loop) MemoryStats() map[string]any {
	return l.memory.Stats()
}
