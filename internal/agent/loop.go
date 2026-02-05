// Package agent implements the core agent loop.
package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/scheduler"
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

// StreamCallback is called for each token during streaming.
// Alias to llm.StreamCallback for compatibility.
type StreamCallback = llm.StreamCallback

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

// FailoverHandler is called before model failover to allow checkpointing.
type FailoverHandler interface {
	// OnFailover is called when switching from one model to another due to failure.
	// Returns an error if failover should be aborted.
	OnFailover(ctx context.Context, fromModel, toModel, reason string) error
}

// Loop is the core agent execution loop.
type Loop struct {
	logger          *slog.Logger
	memory          MemoryStore
	compactor       Compactor
	router          *router.Router
	llm             *llm.OllamaClient
	tools           *tools.Registry
	model           string
	talents         string // Combined talent content for system prompt
	failoverHandler FailoverHandler
}

// NewLoop creates a new agent loop.
func NewLoop(logger *slog.Logger, mem MemoryStore, compactor Compactor, rtr *router.Router, ha *homeassistant.Client, sched *scheduler.Scheduler, ollamaURL, defaultModel, talents string) *Loop {
	return &Loop{
		logger:    logger,
		memory:    mem,
		compactor: compactor,
		router:    rtr,
		llm:       llm.NewOllamaClient(ollamaURL),
		tools:     tools.NewRegistry(ha, sched),
		model:     defaultModel,
		talents:   talents,
	}
}

// SetFailoverHandler configures a handler to be called before model failover.
func (l *Loop) SetFailoverHandler(handler FailoverHandler) {
	l.failoverHandler = handler
}

// Tools returns the tool registry for adding additional tools.
func (l *Loop) Tools() *tools.Registry {
	return l.tools
}

const baseSystemPrompt = `You are Thane, an autonomous AI agent for Home Assistant. You help users manage their smart home.

You have access to tools to query and control Home Assistant:
- get_state: Check the state of any entity (lights, sensors, doors, etc.)
- list_entities: Discover entities by domain
- call_service: Control devices (turn on/off lights, set temperatures, etc.)

When asked about the home, use tools to get real data. When asked to control something, use call_service.`

func (l *Loop) buildSystemPrompt() string {
	if l.talents == "" {
		return baseSystemPrompt
	}
	return baseSystemPrompt + "\n\n## Behavioral Guidance\n\n" + l.talents
}

// Run executes one iteration of the agent loop.
// If stream is non-nil, tokens are pushed to it as they arrive.
func (l *Loop) Run(ctx context.Context, req *Request, stream StreamCallback) (*Response, error) {
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
		Content: l.buildSystemPrompt(),
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

	// Select model via router
	model := req.Model
	var routerDecision *router.Decision
	
	if model == "" || model == "thane" {
		if l.router != nil {
			// Get the user's query from messages
			query := ""
			for i := len(req.Messages) - 1; i >= 0; i-- {
				if req.Messages[i].Role == "user" {
					query = req.Messages[i].Content
					break
				}
			}
			
			// Calculate context size (rough estimate)
			contextSize := len(l.talents) / 4 // talents
			for _, m := range history {
				contextSize += len(m.Content) / 4
			}
			
			routerReq := router.Request{
				Query:       query,
				ContextSize: contextSize,
				NeedsTools:  true, // We always have tools available
				ToolCount:   len(l.tools.List()),
				Priority:    router.PriorityInteractive,
			}
			
			model, routerDecision = l.router.Route(ctx, routerReq)
		} else {
			model = l.model
		}
	}

	// Get available tools
	toolDefs := l.tools.List()
	
	startTime := time.Now()

	// Agent loop - may iterate if tool calls are needed
	maxIterations := 5
	for i := 0; i < maxIterations; i++ {
		l.logger.Info("calling LLM",
			"model", model,
			"messages", len(llmMessages),
			"tools", len(toolDefs),
			"iteration", i+1,
		)

		// Use streaming to avoid HTTP timeouts on slow models
		llmResp, err := l.llm.ChatStream(ctx, model, llmMessages, toolDefs, stream)
		if err != nil {
			l.logger.Error("LLM call failed", "error", err, "model", model)
			
			// Try failover to default model if using a routed model
			if model != l.model {
				fallbackModel := l.model
				l.logger.Info("attempting failover", "from", model, "to", fallbackModel)
				
				// Call failover handler if configured (for checkpointing)
				if l.failoverHandler != nil {
					if ferr := l.failoverHandler.OnFailover(ctx, model, fallbackModel, err.Error()); ferr != nil {
						l.logger.Warn("failover handler failed", "error", ferr)
						// Continue with failover anyway
					}
				}
				
				// Retry with fallback model
				model = fallbackModel
				llmResp, err = l.llm.ChatStream(ctx, model, llmMessages, toolDefs, stream)
				if err != nil {
					l.logger.Error("failover also failed", "error", err, "model", model)
					return nil, err
				}
				l.logger.Info("failover successful", "model", model)
			} else {
				return nil, err
			}
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

		// Record router outcome
		if l.router != nil && routerDecision != nil {
			latency := time.Since(startTime).Milliseconds()
			l.router.RecordOutcome(routerDecision.RequestID, latency, l.memory.GetTokenCount(convID), true)
		}
		
		l.logger.Info("agent loop completed",
			"conversation", convID,
			"iterations", i+1,
			"tokens", l.memory.GetTokenCount(convID),
			"model", model,
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

// Process is a convenience wrapper for single-shot requests (no streaming).
func (l *Loop) Process(ctx context.Context, conversationID, message string) (string, error) {
	req := &Request{
		ConversationID: conversationID,
		Messages: []Message{{
			Role:    "user",
			Content: message,
		}},
	}
	
	resp, err := l.Run(ctx, req, nil)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
