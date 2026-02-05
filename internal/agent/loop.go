// Package agent implements the core agent loop.
package agent

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
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

// MemoryStore is the interface for memory storage.
type MemoryStore interface {
	GetMessages(conversationID string) []memory.Message
	AddMessage(conversationID, role, content string) error
	GetTokenCount(conversationID string) int
	Stats() map[string]any
}

// Compactor handles conversation compaction.
type Compactor interface {
	NeedsCompaction(conversationID string) bool
	Compact(ctx context.Context, conversationID string) error
}

// Loop is the core agent execution loop.
type Loop struct {
	logger    *slog.Logger
	memory    MemoryStore
	compactor Compactor
	llm       *llm.OllamaClient
	tools     *tools.Registry
	model     string
}

// NewLoop creates a new agent loop.
func NewLoop(logger *slog.Logger, mem MemoryStore, compactor Compactor, ha *homeassistant.Client, ollamaURL, defaultModel string) *Loop {
	return &Loop{
		logger:    logger,
		memory:    mem,
		compactor: compactor,
		llm:       llm.NewOllamaClient(ollamaURL),
		tools:     tools.NewRegistry(ha),
		model:     defaultModel,
	}
}

const systemPrompt = `You are Thane, an autonomous AI agent for Home Assistant. You help users manage their smart home.

You have access to tools to query and control Home Assistant:
- get_state: Check the state of any entity (lights, sensors, doors, etc.)
- list_entities: Discover entities by domain
- call_service: Control devices (turn on/off lights, set temperatures, etc.)

Be helpful and concise. When asked about the home, use tools to get real data. When asked to control something, use call_service.`

// Run executes one iteration of the agent loop.
func (l *Loop) Run(ctx context.Context, req *Request) (*Response, error) {
	convID := req.ConversationID
	if convID == "" {
		convID = "default"
	}

	l.logger.Info("agent loop started",
		"conversation", convID,
		"messages", len(req.Messages),
	)

	// Load conversation history
	history := l.memory.GetMessages(convID)

	// Add incoming messages to memory
	for _, m := range req.Messages {
		if err := l.memory.AddMessage(convID, m.Role, m.Content); err != nil {
			l.logger.Warn("failed to store message", "error", err)
		}
	}

	// Build messages for LLM
	var llmMessages []llm.Message
	llmMessages = append(llmMessages, llm.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	// Add history
	for _, m := range history {
		llmMessages = append(llmMessages, llm.Message{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	// Add current messages
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

	// Get available tools
	toolDefs := l.tools.List()

	// Agent loop - may iterate if tool calls are needed
	maxIterations := 5
	for i := 0; i < maxIterations; i++ {
		l.logger.Info("calling LLM",
			"model", model,
			"messages", len(llmMessages),
			"tools", len(toolDefs),
			"iteration", i+1,
		)

		llmResp, err := l.llm.Chat(ctx, model, llmMessages, toolDefs)
		if err != nil {
			l.logger.Error("LLM call failed", "error", err)
			return nil, err
		}

		// Check for tool calls
		if len(llmResp.Message.ToolCalls) > 0 {
			l.logger.Info("processing tool calls", "count", len(llmResp.Message.ToolCalls))

			// Add assistant message with tool calls
			llmMessages = append(llmMessages, llmResp.Message)

			// Execute each tool call
			for _, tc := range llmResp.Message.ToolCalls {
				toolName := tc.Function.Name
				
				// Convert arguments map to JSON string for Execute
				argsJSON := ""
				if tc.Function.Arguments != nil {
					argsBytes, _ := json.Marshal(tc.Function.Arguments)
					argsJSON = string(argsBytes)
				}

				l.logger.Info("executing tool",
					"tool", toolName,
					"args", argsJSON,
				)

				result, err := l.tools.Execute(ctx, toolName, argsJSON)
				if err != nil {
					result = "Error: " + err.Error()
					l.logger.Error("tool execution failed", "tool", toolName, "error", err)
				} else {
					l.logger.Info("tool executed", "tool", toolName, "result_len", len(result))
				}

				// Add tool result message
				llmMessages = append(llmMessages, llm.Message{
					Role:    "tool",
					Content: result,
				})
			}

			// Continue loop to get final response
			continue
		}

		// No tool calls - we have the final response
		resp := &Response{
			Content:      llmResp.Message.Content,
			Model:        model,
			FinishReason: "stop",
		}

		// Store response in memory
		if err := l.memory.AddMessage(convID, "assistant", resp.Content); err != nil {
			l.logger.Warn("failed to store response", "error", err)
		}

		// Check if compaction needed (async-safe: doesn't block response)
		if l.compactor != nil && l.compactor.NeedsCompaction(convID) {
			l.logger.Info("triggering compaction", "conversation", convID)
			go func() {
				if err := l.compactor.Compact(context.Background(), convID); err != nil {
					l.logger.Error("compaction failed", "error", err)
				} else {
					l.logger.Info("compaction completed", "conversation", convID)
				}
			}()
		}

		l.logger.Info("agent loop completed",
			"conversation", convID,
			"iterations", i+1,
			"tokens", l.memory.GetTokenCount(convID),
		)

		return resp, nil
	}

	// Shouldn't reach here, but safety net
	return &Response{
		Content:      "I apologize, but I wasn't able to complete that request. Please try again.",
		Model:        model,
		FinishReason: "max_iterations",
	}, nil
}

// MemoryStats returns current memory statistics.
func (l *Loop) MemoryStats() map[string]any {
	return l.memory.Stats()
}

// ToolsJSON returns the tools definition as JSON (for debugging).
func (l *Loop) ToolsJSON() string {
	data, _ := json.MarshalIndent(l.tools.List(), "", "  ")
	return string(data)
}

// Process is a convenience wrapper for single-shot requests.
func (l *Loop) Process(ctx context.Context, conversationID, message string) (string, error) {
	req := &Request{
		ConversationID: conversationID,
		Messages: []Message{{
			Role:    "user",
			Content: message,
		}},
	}
	
	resp, err := l.Run(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
