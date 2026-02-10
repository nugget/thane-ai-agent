// Package agent implements the core agent loop.
package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/scheduler"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// Message represents a chat message.
type Message struct {
	Role    string `json:"role"` // system, user, assistant
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

// ToolCallRecorder optionally records tool call execution.
// Implemented by stores that support tool call tracking.
type ToolCallRecorder interface {
	RecordToolCall(conversationID, messageID, toolCallID, toolName, arguments string) error
	CompleteToolCall(toolCallID, result, errMsg string) error
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

// ContextProvider supplies dynamic context for the system prompt.
type ContextProvider interface {
	// GetContext returns context to inject into the system prompt.
	// The userMessage is provided to enable semantic search for relevant facts.
	GetContext(ctx context.Context, userMessage string) (string, error)
}

// Loop is the core agent execution loop.
type Loop struct {
	logger          *slog.Logger
	memory          MemoryStore
	compactor       Compactor
	router          *router.Router
	llm             llm.Client
	tools           *tools.Registry
	model           string
	talents         string // Combined talent content for system prompt
	failoverHandler FailoverHandler
	contextProvider ContextProvider
}

// NewLoop creates a new agent loop.
func NewLoop(logger *slog.Logger, mem MemoryStore, compactor Compactor, rtr *router.Router, ha *homeassistant.Client, sched *scheduler.Scheduler, llmClient llm.Client, defaultModel, talents string) *Loop {
	return &Loop{
		logger:    logger,
		memory:    mem,
		compactor: compactor,
		router:    rtr,
		llm:       llmClient,
		tools:     tools.NewRegistry(ha, sched),
		model:     defaultModel,
		talents:   talents,
	}
}

// SetFailoverHandler configures a handler to be called before model failover.
func (l *Loop) SetFailoverHandler(handler FailoverHandler) {
	l.failoverHandler = handler
}

// SetContextProvider configures a provider for dynamic system prompt context.
func (l *Loop) SetContextProvider(provider ContextProvider) {
	l.contextProvider = provider
}

// Tools returns the tool registry for adding additional tools.
func (l *Loop) Tools() *tools.Registry {
	return l.tools
}

// Simple greeting patterns that don't need tool calls
var greetingPatterns = []string{
	"hi", "hello", "hey", "howdy", "hiya", "yo",
	"good morning", "good afternoon", "good evening",
	"what's up", "whats up", "sup",
}

// isSimpleGreeting checks if the message is a simple greeting
func isSimpleGreeting(msg string) bool {
	lower := strings.ToLower(strings.TrimSpace(msg))
	// Remove punctuation
	lower = strings.TrimRight(lower, "!?.,")
	for _, pattern := range greetingPatterns {
		if lower == pattern {
			return true
		}
	}
	return false
}

// Greeting responses to cycle through
var greetingResponses = []string{
	"Hey! What can I help you with?",
	"Hi there! How can I help?",
	"Hello! What would you like me to do?",
	"Hey! Ready to help.",
}

var greetingIndex int

// getGreetingResponse returns a friendly greeting response
func getGreetingResponse() string {
	resp := greetingResponses[greetingIndex%len(greetingResponses)]
	greetingIndex++
	return resp
}

const baseSystemPrompt = `You are Thane, a friendly Home Assistant voice controller.

## When to Use Tools
Only use tools when the user asks you to DO something or CHECK something specific:
- "Turn on the light" → use control_device
- "Is the door locked?" → use get_state
- "What's the temperature?" → use get_state

Do NOT use tools for:
- Greetings ("hi", "hello", "hey") — just say hi back!
- Conversation ("how are you?", "thanks") — respond directly
- Questions about yourself ("who are you?") — answer from your knowledge

IMPORTANT: For simple greetings, respond IMMEDIATELY with a friendly greeting. No need to recall facts or check anything first.

## Primary Tool
- control_device: USE THIS for all "turn on/off" commands. It finds AND controls the device in one step.

## Examples
User: "Hi"
→ "Hey! What can I help you with?"

User: "Turn on the Hue Go lamp in my office and make it purple"
→ control_device(description="Hue Go lamp", area="office", action="turn_on", color="purple")
→ "Done. Turned on Office Hue Go."

User: "Turn off the kitchen light"
→ control_device(description="kitchen light", action="turn_off")
→ "Done. Turned off Kitchen Light."

## Rules
- Use control_device for device commands. Do not guess entity_ids.
- Keep responses short for actions: "Done" or the result.
- Be conversational for chat — you don't need tools for every message.`

func (l *Loop) buildSystemPrompt(ctx context.Context, userMessage string) string {
	var sb strings.Builder
	sb.WriteString(baseSystemPrompt)

	// Add current time
	sb.WriteString("\n\n## Current Time\n")
	sb.WriteString(time.Now().Format("Monday, January 2, 2006 at 15:04 MST"))

	// Add talents
	if l.talents != "" {
		sb.WriteString("\n\n## Behavioral Guidance\n\n")
		sb.WriteString(l.talents)
	}

	// Add dynamic context (semantic facts, etc.)
	if l.contextProvider != nil {
		dynCtx, err := l.contextProvider.GetContext(ctx, userMessage)
		if err != nil {
			l.logger.Warn("failed to get dynamic context", "error", err)
		} else if dynCtx != "" {
			sb.WriteString("\n\n## Relevant Context\n\n")
			sb.WriteString(dynCtx)
		}
	}

	return sb.String()
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

	// Extract user message for context search
	var userMessage string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			userMessage = req.Messages[i].Content
			break
		}
	}

	// Fast-path: handle simple greetings without tool calls
	if isSimpleGreeting(userMessage) {
		l.logger.Info("simple greeting detected, responding directly")
		response := getGreetingResponse()
		if err := l.memory.AddMessage(convID, "assistant", response); err != nil {
			l.logger.Warn("failed to store greeting response", "error", err)
		}
		return &Response{
			Content:      response,
			Model:        "greeting-handler",
			FinishReason: "stop",
		}, nil
	}

	// Build messages for LLM
	var llmMessages []llm.Message
	llmMessages = append(llmMessages, llm.Message{
		Role:    "system",
		Content: l.buildSystemPrompt(ctx, userMessage),
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
	maxIterations := 5 // Allow enough iterations for context gathering + response
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
			// Check if memory store supports tool call recording
			recorder, hasRecorder := l.memory.(ToolCallRecorder)
			convID := req.ConversationID
			if convID == "" {
				convID = "default"
			}

			for _, tc := range llmResp.Message.ToolCalls {
				toolName := tc.Function.Name
				toolCallID, _ := uuid.NewV7()
				toolCallIDStr := toolCallID.String()

				// Convert arguments map to JSON string for Execute
				argsJSON := ""
				if tc.Function.Arguments != nil {
					argsBytes, _ := json.Marshal(tc.Function.Arguments)
					argsJSON = string(argsBytes)
				}

				l.logger.Info("executing tool",
					"tool", toolName,
					"call_id", toolCallIDStr,
					"args", argsJSON,
				)

				// Record tool call start (if supported)
				if hasRecorder {
					if err := recorder.RecordToolCall(convID, "", toolCallIDStr, toolName, argsJSON); err != nil {
						l.logger.Warn("failed to record tool call", "error", err)
					}
				}

				result, err := l.tools.Execute(ctx, toolName, argsJSON)
				errMsg := ""
				if err != nil {
					errMsg = err.Error()
					result = "Error: " + errMsg
					l.logger.Error("tool execution failed", "tool", toolName, "error", err)
				} else {
					l.logger.Info("tool executed", "tool", toolName, "result_len", len(result))
				}

				// Record tool call completion (if supported)
				if hasRecorder {
					if err := recorder.CompleteToolCall(toolCallIDStr, result, errMsg); err != nil {
						l.logger.Warn("failed to complete tool call record", "error", err)
					}
				}

				// Add tool result message
				llmMessages = append(llmMessages, llm.Message{
					Role:       "tool",
					Content:    result,
					ToolCallID: tc.ID, // Required by Anthropic for tool_result correlation
				})
			}

			// Continue loop to get final response
			continue
		}

		// No tool calls - we have the final response
		// Log when we expected tool calls but got text (first iteration = no tool use)
		if i == 0 && len(llmResp.Message.Content) > 0 {
			preview := llmResp.Message.Content
			if len(preview) > 300 {
				preview = preview[:300] + "..."
			}
			l.logger.Info("model responded with text (no tool call)",
				"content_preview", preview,
			)
		}

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
