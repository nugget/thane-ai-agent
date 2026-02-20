// Package agent implements the core agent loop.
package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/conditions"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/prompts"
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
	Messages       []Message         `json:"messages"`
	Model          string            `json:"model,omitempty"`
	ConversationID string            `json:"conversation_id,omitempty"`
	Hints          map[string]string `json:"hints,omitempty"` // Routing hints (channel, mission, etc.)
	SkipContext    bool              `json:"-"`               // Skip memory, tools, and context injection (for lightweight completions)
	ExcludeTools   []string          `json:"-"`               // Tool names to exclude from this run (e.g., lifecycle tools for recurring wakes)
}

// StreamEvent is a single event in a streaming response.
// Alias to llm.StreamEvent for use by consumers.
type StreamEvent = llm.StreamEvent

// StreamCallback receives streaming events.
// Alias to llm.StreamCallback for compatibility.
type StreamCallback = llm.StreamCallback

// Stream event kinds re-exported for consumers.
const (
	KindToken         = llm.KindToken
	KindToolCallStart = llm.KindToolCallStart
	KindToolCallDone  = llm.KindToolCallDone
	KindDone          = llm.KindDone
)

// maxEgoBytes is the maximum size of ego.md content injected into the
// system prompt. Content beyond this limit is truncated with a marker.
const maxEgoBytes = 16 * 1024

// Response represents the agent's response.
type Response struct {
	Content      string         `json:"content"`
	Model        string         `json:"model"`
	FinishReason string         `json:"finish_reason"`
	InputTokens  int            `json:"input_tokens,omitempty"`
	OutputTokens int            `json:"output_tokens,omitempty"`
	ToolsUsed    map[string]int `json:"tools_used,omitempty"` // tool name → call count
}

// MemoryStore is the interface for memory storage.
type MemoryStore interface {
	GetMessages(conversationID string) []memory.Message
	AddMessage(conversationID, role, content string) error
	GetTokenCount(conversationID string) int
	Clear(conversationID string) error
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

// SessionArchiver handles session lifecycle and message archiving.
type SessionArchiver interface {
	// ArchiveConversation archives all messages from a conversation before clearing.
	ArchiveConversation(conversationID string, messages []memory.Message, reason string) error
	// StartSession begins a new session for a conversation.
	StartSession(conversationID string) (sessionID string, err error)
	// EndSession ends the current session.
	EndSession(sessionID string, reason string) error
	// ActiveSessionID returns the current session ID, or empty if none.
	ActiveSessionID(conversationID string) string
	// EnsureSession starts a session if none is active, returns the session ID.
	EnsureSession(conversationID string) string
	// OnMessage is called after each message to track session stats.
	OnMessage(conversationID string)
	// ActiveSessionStartedAt returns when the active session began,
	// or the zero time if there is no active session.
	ActiveSessionStartedAt(conversationID string) time.Time
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
	persona         string // Persona content (replaces base system prompt if set)
	egoFile         string // Path to ego.md — read fresh each turn for system prompt
	injectedContext string // Static context from inject_files, loaded at startup
	timezone        string // IANA timezone for Current Conditions (e.g., "America/Chicago")
	contextWindow   int    // Context window size of default model
	failoverHandler FailoverHandler
	contextProvider ContextProvider
	archiver        SessionArchiver
	extractor       *memory.Extractor
	iter0Tools      []string           // Restricted tool set for orchestrator mode (nil = all tools)
	debugCfg        config.DebugConfig // Debug options (system prompt dump, etc.)
}

// NewLoop creates a new agent loop.
func NewLoop(logger *slog.Logger, mem MemoryStore, compactor Compactor, rtr *router.Router, ha *homeassistant.Client, sched *scheduler.Scheduler, llmClient llm.Client, defaultModel, talents, persona string, contextWindow int) *Loop {
	return &Loop{
		logger:        logger,
		memory:        mem,
		compactor:     compactor,
		router:        rtr,
		llm:           llmClient,
		tools:         tools.NewRegistry(ha, sched),
		model:         defaultModel,
		talents:       talents,
		persona:       persona,
		contextWindow: contextWindow,
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

// SetArchiver configures the session archiver for preserving conversations.
func (l *Loop) SetArchiver(archiver SessionArchiver) {
	l.archiver = archiver
}

// SetExtractor configures the automatic fact extractor.
func (l *Loop) SetExtractor(e *memory.Extractor) {
	l.extractor = e
}

// SetEgoFile sets the path to ego.md. When set, the file is read fresh
// on each turn and its content is injected into the system prompt.
func (l *Loop) SetEgoFile(path string) {
	l.egoFile = path
}

// SetInjectedContext sets static context to include in every system prompt.
func (l *Loop) SetInjectedContext(ctx string) {
	l.injectedContext = ctx
}

// SetTimezone configures the IANA timezone for the Current Conditions
// section of the system prompt (e.g., "America/Chicago").
func (l *Loop) SetTimezone(tz string) {
	l.timezone = tz
}

// SetIter0Tools configures the restricted tool set for all iterations
// of the agent loop. When set, only the named tools are advertised on
// every LLM call, keeping the primary model in orchestrator mode and
// steering it toward delegation. If thane_delegate is not registered
// in the tool registry, gating is silently disabled to avoid leaving
// the agent without actionable tools.
func (l *Loop) SetIter0Tools(names []string) {
	l.iter0Tools = names
}

// SetDebugConfig configures debug options for the agent loop.
func (l *Loop) SetDebugConfig(cfg config.DebugConfig) {
	l.debugCfg = cfg
}

// Tools returns the tool registry for adding additional tools.
func (l *Loop) Tools() *tools.Registry {
	return l.tools
}

// Router returns the model router, or nil if no router is configured.
func (l *Loop) Router() *router.Router {
	return l.router
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

// promptSection records the name and byte boundaries of one section in
// the assembled system prompt. Used by the debug dump to annotate
// section sizes.
type promptSection struct {
	name  string
	start int
	end   int
}

func (l *Loop) buildSystemPrompt(ctx context.Context, userMessage string, history []memory.Message) string {
	var sb strings.Builder

	// Track section boundaries for debug dump.
	var sections []promptSection
	mark := func(name string) { sections = append(sections, promptSection{name: name, start: sb.Len()}) }
	seal := func() { sections[len(sections)-1].end = sb.Len() }

	// 1. Persona (identity — who am I)
	mark("PERSONA")
	if l.persona != "" {
		sb.WriteString(l.persona)
	} else {
		sb.WriteString(prompts.BaseSystemPrompt())
	}
	seal()

	// 2. Ego (self-reflection — what have I been noticing/thinking)
	if l.egoFile != "" {
		if data, err := os.ReadFile(l.egoFile); err == nil && len(data) > 0 {
			mark("EGO")
			sb.WriteString("\n\n## Self-Reflection (ego.md)\n\n")
			if len(data) > maxEgoBytes {
				sb.WriteString(string(data[:maxEgoBytes]))
				sb.WriteString("\n\n[ego.md truncated — exceeded 16 KB limit]")
			} else {
				sb.WriteString(string(data))
			}
			seal()
		}
	}

	// 3. Injected context (knowledge — what do I know)
	if l.injectedContext != "" {
		mark("INJECTED CONTEXT")
		sb.WriteString("\n\n## Injected Context\n\n")
		sb.WriteString(l.injectedContext)
		seal()
	}

	// 4. Current Conditions (environment — where/when am I)
	// Placed early because models attend more strongly to content near
	// the beginning. Uses H1 heading to signal operational importance.
	mark("CURRENT CONDITIONS")
	sb.WriteString("\n\n")
	sb.WriteString(conditions.CurrentConditions(l.timezone))

	seal()

	// 5. Talents (behavior — how should I act)
	if l.talents != "" {
		mark("TALENTS")
		sb.WriteString("\n\n## Behavioral Guidance\n\n")
		sb.WriteString(l.talents)
		seal()
	}

	// 6. Dynamic context (facts, anticipations — what's relevant right now)
	if l.contextProvider != nil {
		dynCtx, err := l.contextProvider.GetContext(ctx, userMessage)
		if err != nil {
			l.logger.Warn("failed to get dynamic context", "error", err)
		} else if dynCtx != "" {
			mark("DYNAMIC CONTEXT")
			sb.WriteString("\n\n## Relevant Context\n\n")
			sb.WriteString(dynCtx)
			seal()
		}
	}

	result := sb.String()

	if l.debugCfg.DumpSystemPrompt {
		l.dumpSystemPrompt(result, sections)
	}

	return result
}

// dumpSystemPrompt writes the assembled system prompt to disk with
// section markers and size annotations. Errors are logged as warnings
// and never fail the request.
func (l *Loop) dumpSystemPrompt(prompt string, sections []promptSection) {
	dir := l.debugCfg.DumpDir
	if dir == "" {
		dir = "./debug"
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		l.logger.Warn("debug: failed to create dump dir", "dir", dir, "error", err)
		return
	}

	var dump strings.Builder
	attrs := make([]any, 0, len(sections)*2+2)
	for _, s := range sections {
		size := s.end - s.start
		attrs = append(attrs, strings.ToLower(strings.ReplaceAll(s.name, " ", "_")), size)
		fmt.Fprintf(&dump, "=== %s (%s chars) ===\n", s.name, formatNumber(size))
		dump.WriteString(prompt[s.start:s.end])
		if !strings.HasSuffix(prompt[s.start:s.end], "\n") {
			dump.WriteString("\n")
		}
		dump.WriteString("\n")
	}
	fmt.Fprintf(&dump, "=== TOTAL: %s chars ===\n", formatNumber(len(prompt)))
	attrs = append(attrs, "total", len(prompt))

	l.logger.Info("debug: system prompt dump", attrs...)

	path := filepath.Join(dir, "system-prompt-latest.txt")
	if err := os.WriteFile(path, []byte(dump.String()), 0o640); err != nil {
		l.logger.Warn("debug: failed to write system prompt dump", "path", path, "error", err)
	}
}

// formatNumber formats an integer with comma separators for readability
// in debug output (e.g., 87494 → "87,494").
func formatNumber(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var sb strings.Builder
	remainder := len(s) % 3
	if remainder > 0 {
		sb.WriteString(s[:remainder])
	}
	for i := remainder; i < len(s); i += 3 {
		if sb.Len() > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(s[i : i+3])
	}
	return sb.String()
}

// generateRequestID returns a short, human-scannable identifier for a single
// user-message turn (e.g., "r_7f3ab2c1"). It uses 4 random bytes from a
// UUIDv7 (bytes 8-11), giving 32 bits of entropy — sufficient for
// request-level correlation without realistic collision risk.
func generateRequestID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// Fallback: use current time hex if UUID generation fails.
		return fmt.Sprintf("r_%08x", time.Now().UnixMilli()&0xFFFFFFFF)
	}
	// Bytes 8-11 are from the random section of UUIDv7 (after the
	// variant bits in byte 8, masked by the UUID spec, but still
	// provide ~30 bits of effective randomness).
	return "r_" + hex.EncodeToString(id[8:12])
}

// Run executes one iteration of the agent loop.
// If stream is non-nil, tokens are pushed to it as they arrive.
func (l *Loop) Run(ctx context.Context, req *Request, stream StreamCallback) (resp *Response, err error) {
	convID := req.ConversationID
	if convID == "" {
		convID = "default"
	}

	// Track session activity on successful completion.
	// Skip for lightweight requests (auxiliary) to avoid session noise.
	defer func() {
		if err == nil && l.archiver != nil && !req.SkipContext {
			l.archiver.EnsureSession(convID)
			l.archiver.OnMessage(convID)
		}
	}()

	// Get session ID for log correlation. The session ID (UUIDv7 prefix)
	// is more meaningful than the conversation name (usually "default").
	sessionTag := convID // fallback if no archiver
	if l.archiver != nil {
		if sid := l.archiver.ActiveSessionID(convID); sid != "" {
			sessionTag = sid
		}
	}
	sessionTag = memory.ShortID(sessionTag)

	// Generate a request-scoped ID and logger. Every log line within this
	// turn carries request_id so you can grep for a single user→response cycle.
	requestID := generateRequestID()
	log := l.logger.With("request_id", requestID, "session", sessionTag, "conversation", convID)

	log.Info("agent loop started",
		"messages", len(req.Messages),
	)

	// Always use Thane's memory as the source of truth.
	// For externally-managed conversations (owu-), the client sends full history
	// but Thane's store is the superset (includes tool calls, results, etc.).
	// Only store the NEW message from the client — the last user message.
	//
	// Skip memory entirely for lightweight requests (auxiliary title/tag gen)
	// to avoid polluting conversation history.
	var history []memory.Message
	if !req.SkipContext {
		history = l.memory.GetMessages(convID)

		if strings.HasPrefix(convID, "owu-") {
			// External client (Open WebUI): only add the last user message
			for i := len(req.Messages) - 1; i >= 0; i-- {
				if req.Messages[i].Role == "user" {
					if err := l.memory.AddMessage(convID, "user", req.Messages[i].Content); err != nil {
						log.Warn("failed to store message", "error", err)
					}
					break
				}
			}
		} else {
			// Internal/API clients: store all messages
			for _, m := range req.Messages {
				if err := l.memory.AddMessage(convID, m.Role, m.Content); err != nil {
					log.Warn("failed to store message", "error", err)
				}
			}
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
		log.Debug("simple greeting detected, responding directly")
		response := getGreetingResponse()
		if err := l.memory.AddMessage(convID, "assistant", response); err != nil {
			log.Warn("failed to store greeting response", "error", err)
		}
		return &Response{
			Content:      response,
			Model:        "greeting-handler",
			FinishReason: "stop",
		}, nil
	}

	// Lightweight path: skip memory, tools, and heavy context injection.
	// Used for auxiliary requests (title/tag generation) that don't need the
	// full agent loop. Just send messages to the LLM with no tools.
	if req.SkipContext {
		startTime := time.Now()

		// Resolve model via router (same logic as full path, but inline)
		liteModel := req.Model
		var liteDecision *router.Decision
		if (liteModel == "" || liteModel == "thane") && l.router != nil {
			liteModel, liteDecision = l.router.Route(ctx, router.Request{
				Query:    userMessage,
				Hints:    req.Hints,
				Priority: router.PriorityBackground,
			})
		}
		if liteModel == "" {
			liteModel = l.model
		}

		log.Info("lightweight completion (skip context)",
			"model", liteModel, "messages", len(req.Messages),
		)

		var llmMessages []llm.Message
		for _, m := range req.Messages {
			llmMessages = append(llmMessages, llm.Message{
				Role:    m.Role,
				Content: m.Content,
			})
		}

		llmResp, err := l.llm.ChatStream(ctx, liteModel, llmMessages, nil, stream)
		if err != nil {
			// Record failed outcome
			if l.router != nil && liteDecision != nil {
				l.router.RecordOutcome(liteDecision.RequestID, time.Since(startTime).Milliseconds(), 0, false)
			}
			return nil, fmt.Errorf("lightweight completion: %w", err)
		}

		// Record successful outcome
		if l.router != nil && liteDecision != nil {
			l.router.RecordOutcome(liteDecision.RequestID, time.Since(startTime).Milliseconds(), llmResp.InputTokens+llmResp.OutputTokens, true)
		}

		log.Info("lightweight completion done",
			"model", llmResp.Model,
			"input_tokens", llmResp.InputTokens,
			"output_tokens", llmResp.OutputTokens,
			"elapsed", time.Since(startTime).Round(time.Millisecond),
		)

		return &Response{
			Content:      llmResp.Message.Content,
			Model:        llmResp.Model,
			FinishReason: "stop",
			InputTokens:  llmResp.InputTokens,
			OutputTokens: llmResp.OutputTokens,
		}, nil
	}

	// Build messages for LLM. Enrich ctx with conversation ID so that
	// context providers (e.g. working memory) can scope their output.
	// Propagate request hints so channel-aware providers can adapt.
	promptCtx := tools.WithConversationID(ctx, convID)
	promptCtx = tools.WithHints(promptCtx, req.Hints)

	systemPrompt := l.buildSystemPrompt(promptCtx, userMessage, history)

	// Context usage — appended to the system prompt after all sections
	// are assembled so the token estimate includes the full prompt
	// (persona, talents, dynamic context), not just conversation history.
	//
	// Model/ContextWindow reflect the default model. The "(routed)"
	// suffix signals that the router may select a different model.
	totalChars := len(systemPrompt)
	for _, m := range history {
		totalChars += len(m.Content)
	}
	for _, m := range req.Messages {
		totalChars += len(m.Content)
	}

	usageInfo := conditions.ContextUsageInfo{
		Model:          l.model,
		Routed:         l.router != nil,
		TokenCount:     totalChars / 4, // rough char-to-token estimate
		ContextWindow:  l.contextWindow,
		MessageCount:   len(history),
		ConversationID: convID,
		SessionID:      sessionTag, // already truncated to 8 chars
		RequestID:      requestID,
	}
	for _, m := range history {
		if m.Role == "system" && strings.HasPrefix(m.Content, "[Conversation Summary]") {
			usageInfo.CompactionCount++
		}
	}
	if l.archiver != nil {
		if started := l.archiver.ActiveSessionStartedAt(convID); !started.IsZero() {
			usageInfo.SessionAge = time.Since(started)
		}
	}
	if line := conditions.FormatContextUsage(usageInfo); line != "" {
		systemPrompt += "\n" + line
	}

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

	// Select model via router
	model := req.Model
	var routerDecision *router.Decision

	log.Debug("model selection start", "req_model", req.Model, "default_model", l.model)

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
				Hints:       req.Hints,
			}

			model, routerDecision = l.router.Route(ctx, routerReq)
			log.Debug("model selected by router", "model", model)
		} else {
			model = l.model
			log.Debug("model selected as default (no router)", "model", model)
		}
	} else {
		log.Debug("model specified in request, skipping router", "model", model)
	}

	// Determine whether tool gating is active. Gating is silently disabled
	// when thane_delegate is not registered — without a delegation tool the
	// restricted set would leave the agent unable to act. The thane:ops
	// profile disables gating via the delegation_gating hint to give the
	// model direct access to all tools.
	gatingActive := len(l.iter0Tools) > 0 && l.tools.Get("thane_delegate") != nil
	if req.Hints[router.HintDelegationGating] == "disabled" {
		gatingActive = false
	}
	if gatingActive {
		log.Info("tool gating active", "tools", l.iter0Tools)
	}

	// Build the effective tool registry, excluding any tools the caller
	// requested to hide (e.g., lifecycle tools stripped from recurring wakes).
	effectiveTools := l.tools
	if len(req.ExcludeTools) > 0 {
		effectiveTools = l.tools.FilteredCopyExcluding(req.ExcludeTools)
		log.Info("tools excluded from run", "excluded", req.ExcludeTools)
	}

	startTime := time.Now()

	// Agent loop - may iterate if tool calls are needed
	// Accumulate token usage across iterations
	var totalInputTokens, totalOutputTokens int

	// Estimate system prompt size for cost logging
	systemTokens := len(llmMessages[0].Content) / 4 // rough char-to-token ratio

	// Track tool call repetitions to detect loops
	toolCallCounts := make(map[string]int) // "toolName:argsHash" → count
	toolsUsed := make(map[string]int)      // tool name → total call count (exposed on Response)
	const maxToolRepeat = 3                // Break if same tool+args called this many times

	maxIterations := 50   // Tool call budget; final text response always gets one extra call
	emptyRetried := false // Track whether we've already nudged after an empty response
iterLoop:
	for i := 0; i < maxIterations; i++ {
		// Select tool definitions for this iteration. With gating active,
		// only advertise the restricted set to keep the primary model in
		// orchestrator mode across all iterations.
		var toolDefs []map[string]any
		if gatingActive {
			toolDefs = effectiveTools.FilteredCopy(l.iter0Tools).List()
		} else {
			toolDefs = effectiveTools.List()
		}

		// Estimate total message size for this iteration
		iterMsgTokens := 0
		for _, m := range llmMessages {
			iterMsgTokens += len(m.Content) / 4
		}

		iterStart := time.Now()

		log.Info("llm call",
			"iter", i,
			"model", model,
			"msgs", len(llmMessages),
			"est_tokens", iterMsgTokens,
			"system_tokens", systemTokens,
		)

		// Use streaming to avoid HTTP timeouts on slow models
		llmResp, err := l.llm.ChatStream(ctx, model, llmMessages, toolDefs, stream)
		if err != nil {
			log.Error("LLM call failed", "error", err, "iter", i, "model", model)

			// Try failover to default model if using a routed model
			if model != l.model {
				fallbackModel := l.model
				log.Info("attempting failover", "iter", i, "from", model, "to", fallbackModel)

				// Call failover handler if configured (for checkpointing)
				if l.failoverHandler != nil {
					if ferr := l.failoverHandler.OnFailover(ctx, model, fallbackModel, err.Error()); ferr != nil {
						log.Warn("failover handler failed", "error", ferr)
						// Continue with failover anyway
					}
				}

				// Retry with fallback model
				model = fallbackModel
				llmResp, err = l.llm.ChatStream(ctx, model, llmMessages, toolDefs, stream)
				if err != nil {
					log.Error("failover also failed", "error", err, "model", model)
					return nil, err
				}
				log.Info("failover successful", "model", model)
			} else {
				return nil, err
			}
		}

		// Accumulate token usage
		totalInputTokens += llmResp.InputTokens
		totalOutputTokens += llmResp.OutputTokens

		log.Info("llm response",
			"iter", i,
			"model", model,
			"resp_model", llmResp.Model,
			"input_tokens", llmResp.InputTokens,
			"output_tokens", llmResp.OutputTokens,
			"cumul_in", totalInputTokens,
			"cumul_out", totalOutputTokens,
			"tool_calls", len(llmResp.Message.ToolCalls),
			"elapsed", time.Since(iterStart).Round(time.Millisecond),
			"tok_per_sec", func() float64 {
				elapsed := time.Since(iterStart).Seconds()
				if elapsed > 0 && llmResp.OutputTokens > 0 {
					return math.Round(float64(llmResp.OutputTokens)/elapsed*10) / 10
				}
				return 0
			}(),
		)

		// Check for tool calls
		if len(llmResp.Message.ToolCalls) > 0 {

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

				// Detect tool call loops
				callKey := toolName + ":" + argsJSON
				toolCallCounts[callKey]++
				if toolCallCounts[callKey] > maxToolRepeat {
					log.Warn("tool call loop detected, breaking",
						"iter", i, "tool", toolName, "repeat_count", toolCallCounts[callKey],
					)
					// Inject an error message to help the model recover
					llmMessages = append(llmMessages, llm.Message{
						Role:    "tool",
						Content: fmt.Sprintf("Error: tool '%s' has been called %d times with the same arguments. Stop calling tools and provide your response to the user.", toolName, toolCallCounts[callKey]),
					})
					continue iterLoop
				}

				log.Info("tool exec",
					"iter", i, "tool", toolName,
				)
				// Log arguments at DEBUG to avoid leaking sensitive data
				// (exec commands, file contents, credentials in paths)
				if log.Enabled(ctx, slog.LevelDebug) {
					argPreview := argsJSON
					if len(argPreview) > 200 {
						argPreview = argPreview[:200] + "..."
					}
					log.Debug("tool exec args",
						"iter", i, "tool", toolName,
						"args", argPreview,
					)
				}

				// Record tool call start (if supported)
				if hasRecorder {
					if err := recorder.RecordToolCall(convID, "", toolCallIDStr, toolName, argsJSON); err != nil {
						log.Warn("failed to record tool call", "error", err)
					}
				}

				// Emit tool call start event
				if stream != nil {
					stream(llm.StreamEvent{
						Kind:     llm.KindToolCallStart,
						ToolCall: &tc,
					})
				}

				toolCtx := tools.WithConversationID(ctx, convID)
				result, err := l.tools.Execute(toolCtx, toolName, argsJSON)
				toolsUsed[toolName]++
				errMsg := ""
				if err != nil {
					errMsg = err.Error()
					result = "Error: " + errMsg
					log.Error("tool exec failed", "tool", toolName, "error", err)
				} else {
					log.Debug("tool exec done", "tool", toolName, "result_len", len(result))
				}

				// Emit tool call done event
				if stream != nil {
					stream(llm.StreamEvent{
						Kind:       llm.KindToolCallDone,
						ToolName:   toolName,
						ToolResult: result,
						ToolError:  errMsg,
					})
				}

				// Record tool call completion (if supported)
				if hasRecorder {
					if err := recorder.CompleteToolCall(toolCallIDStr, result, errMsg); err != nil {
						log.Warn("failed to complete tool call record", "error", err)
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
			log.Debug("model responded with text (no tool call)",
				"content_preview", preview,
			)
		}

		// Guard against empty responses after tool calls (#167).
		// The model sometimes returns stop tokens with no content after
		// spending iterations on tool calls. Nudge it once to respond.
		if llmResp.Message.Content == "" && i > 0 {
			if !emptyRetried {
				log.Warn("empty response after tool calls, nudging model",
					"iter", i,
					"input_tokens", totalInputTokens,
					"output_tokens", totalOutputTokens,
				)
				llmMessages = append(llmMessages, llm.Message{
					Role:    "user",
					Content: prompts.EmptyResponseNudge,
				})
				emptyRetried = true
				continue
			}
			log.Error("empty response after nudge, returning fallback",
				"iter", i,
				"input_tokens", totalInputTokens,
				"output_tokens", totalOutputTokens,
			)
			llmResp.Message.Content = prompts.EmptyResponseFallback
		}

		resp := &Response{
			Content:      llmResp.Message.Content,
			Model:        model,
			FinishReason: "stop",
			InputTokens:  totalInputTokens,
			OutputTokens: totalOutputTokens,
			ToolsUsed:    toolsUsed,
		}

		// Store response in memory
		if err := l.memory.AddMessage(convID, "assistant", resp.Content); err != nil {
			log.Warn("failed to store response", "error", err)
		}

		// Async fact extraction — don't block the response path.
		if l.extractor != nil {
			extractMsgs := recentSlice(history, 6)
			go func() {
				if !l.extractor.ShouldExtract(userMessage, resp.Content, len(history)+len(req.Messages)+1, req.SkipContext) {
					return
				}
				extractCtx, cancel := context.WithTimeout(context.Background(), l.extractor.Timeout())
				defer cancel()
				if err := l.extractor.Extract(extractCtx, userMessage, resp.Content, extractMsgs); err != nil {
					log.Warn("fact extraction failed",
						"error", err)
				}
			}()
		}

		// Check if compaction needed (async-safe: doesn't block response)
		if l.compactor != nil && l.compactor.NeedsCompaction(convID) {
			preTokens := l.memory.GetTokenCount(convID)
			preMessages := len(l.memory.GetMessages(convID))
			log.Info("triggering compaction",
				"tokens_before", preTokens,
				"messages_before", preMessages,
			)
			go func() {
				compactStart := time.Now()
				if err := l.compactor.Compact(context.Background(), convID); err != nil {
					log.Error("compaction failed",
						"error", err,
					)
				} else {
					postTokens := l.memory.GetTokenCount(convID)
					postMessages := len(l.memory.GetMessages(convID))
					log.Info("compaction completed",
						"tokens_after", postTokens,
						"messages_after", postMessages,
						"tokens_freed", preTokens-postTokens,
						"messages_compacted", preMessages-postMessages,
						"elapsed", time.Since(compactStart).Round(time.Millisecond),
					)
				}
			}()
		}

		// Record router outcome
		if l.router != nil && routerDecision != nil {
			latency := time.Since(startTime).Milliseconds()
			l.router.RecordOutcome(routerDecision.RequestID, latency, l.memory.GetTokenCount(convID), true)
		}

		elapsed := time.Since(startTime)
		log.Info("agent loop completed",
			"model", model,
			"iter", i,
			"input_tokens", totalInputTokens,
			"output_tokens", totalOutputTokens,
			"elapsed", elapsed.Round(time.Millisecond),
			"context_tokens", l.memory.GetTokenCount(convID),
		)

		return resp, nil
	}

	// Max iterations exhausted. If the last message is a tool result,
	// make one final LLM call with tools disabled to generate a response.
	if len(llmMessages) > 0 && llmMessages[len(llmMessages)-1].Role == "tool" {
		log.Warn("max iterations reached with pending tool results, making final LLM call",
			"max_iter", maxIterations,
		)

		llmResp, err := l.llm.ChatStream(ctx, model, llmMessages, nil, stream) // nil tools = no more tool calls
		if err != nil {
			log.Error("final LLM call failed after max iterations", "error", err, "model", model, "max_iter", maxIterations)
			return &Response{
				Content:      "I found the information but couldn't compose a response. Please try again.",
				Model:        model,
				FinishReason: "max_iterations",
				InputTokens:  totalInputTokens,
				OutputTokens: totalOutputTokens,
				ToolsUsed:    toolsUsed,
			}, nil
		}

		totalInputTokens += llmResp.InputTokens
		totalOutputTokens += llmResp.OutputTokens

		content := llmResp.Message.Content
		if content == "" {
			log.Error("empty response in max-iterations recovery",
				"max_iter", maxIterations,
				"input_tokens", totalInputTokens,
				"output_tokens", totalOutputTokens,
			)
			content = prompts.EmptyResponseFallback
		}

		resp := &Response{
			Content:      content,
			Model:        model,
			FinishReason: "stop",
			InputTokens:  totalInputTokens,
			OutputTokens: totalOutputTokens,
			ToolsUsed:    toolsUsed,
		}

		if err := l.memory.AddMessage(convID, "assistant", resp.Content); err != nil {
			log.Warn("failed to store response", "error", err)
		}

		elapsed := time.Since(startTime)
		log.Info("agent loop completed (max iterations recovery)",
			"model", model,
			"max_iter", maxIterations,
			"input_tokens", totalInputTokens,
			"output_tokens", totalOutputTokens,
			"elapsed", elapsed.Round(time.Millisecond),
		)

		return resp, nil
	}

	log.Error("max iterations reached without tool results or response",
		"max_iter", maxIterations,
		"model", model,
	)
	return &Response{
		Content:      "I wasn't able to complete that request. Please try again.",
		Model:        model,
		FinishReason: "max_iterations",
		InputTokens:  totalInputTokens,
		OutputTokens: totalOutputTokens,
		ToolsUsed:    toolsUsed,
	}, nil
}

// MemoryStats returns current memory statistics.
func (l *Loop) MemoryStats() map[string]any {
	return l.memory.Stats()
}

// GetTokenCount returns the estimated token count for a conversation.
// GetHistory returns the conversation messages for a given conversation.
func (l *Loop) GetHistory(conversationID string) []memory.Message {
	return l.memory.GetMessages(conversationID)
}

func (l *Loop) GetTokenCount(conversationID string) int {
	return l.memory.GetTokenCount(conversationID)
}

// GetContextWindow returns the context window size of the default model.
func (l *Loop) GetContextWindow() int {
	return l.contextWindow
}

// ResetConversation archives and then clears the conversation history.
func (l *Loop) ResetConversation(conversationID string) error {
	l.archiveAndEndSession(conversationID, "reset")

	if err := l.memory.Clear(conversationID); err != nil {
		return err
	}

	// Start a fresh session.
	if l.archiver != nil {
		if _, err := l.archiver.StartSession(conversationID); err != nil {
			l.logger.Error("failed to start new session after reset", "error", err)
		}
	}

	return nil
}

// CloseSession gracefully closes the current session, archives messages,
// injects a carry-forward handoff into the new session, and starts a
// fresh session. Unlike ResetConversation, the carry-forward summary
// provides continuity across the session boundary.
func (l *Loop) CloseSession(conversationID, reason, carryForward string) error {
	if reason == "" {
		reason = "close"
	}

	// Archive and end current session (same pattern as ResetConversation).
	l.archiveAndEndSession(conversationID, reason)

	if err := l.memory.Clear(conversationID); err != nil {
		return fmt.Errorf("clear memory: %w", err)
	}

	// Start a fresh session.
	if l.archiver != nil {
		if _, err := l.archiver.StartSession(conversationID); err != nil {
			l.logger.Error("failed to start new session after close", "error", err)
		}
	}

	// Inject carry-forward into the new session as a system message.
	if carryForward != "" {
		if cs, ok := l.memory.(interface {
			AddCompactionSummary(string, string) error
		}); ok {
			if err := cs.AddCompactionSummary(conversationID, "[Session Handoff]\n"+carryForward); err != nil {
				l.logger.Error("failed to inject carry-forward", "error", err)
			}
		}
	}

	l.logger.Info("session closed",
		"conversation", conversationID,
		"reason", reason,
		"carry_forward_len", len(carryForward),
	)
	return nil
}

// CheckpointSession archives a snapshot of the current conversation state
// without ending the session. The active session continues uninterrupted.
func (l *Loop) CheckpointSession(conversationID, label string) error {
	if l.archiver == nil {
		return fmt.Errorf("no archiver configured")
	}

	messages := l.getAllMessages(conversationID)
	if len(messages) == 0 {
		return fmt.Errorf("no messages to checkpoint")
	}

	reason := "checkpoint"
	if label != "" {
		reason = "checkpoint:" + label
	}

	if err := l.archiver.ArchiveConversation(conversationID, messages, reason); err != nil {
		return fmt.Errorf("archive checkpoint: %w", err)
	}

	l.logger.Info("session checkpoint created",
		"conversation", conversationID,
		"label", label,
		"messages", len(messages),
	)
	return nil
}

// SplitSession retroactively splits the current session at a past message
// boundary. Messages before the split point are archived as a completed
// session; messages at and after the split point are retained as the
// current session. Exactly one of atIndex (negative offset from end) or
// atMessage (substring match) must be non-zero.
func (l *Loop) SplitSession(conversationID string, atIndex int, atMessage string) error {
	if l.archiver == nil {
		return fmt.Errorf("no archiver configured")
	}

	messages := l.getAllMessages(conversationID)
	if len(messages) == 0 {
		return fmt.Errorf("no messages to split")
	}

	splitIdx, err := findSplitPoint(messages, atIndex, atMessage)
	if err != nil {
		return err
	}

	preSplit := messages[:splitIdx]
	postSplit := messages[splitIdx:]

	// Archive pre-split messages.
	if err := l.archiver.ArchiveConversation(conversationID, preSplit, "split"); err != nil {
		return fmt.Errorf("archive pre-split messages: %w", err)
	}

	// End the current session at the split-point timestamp.
	if sid := l.archiver.ActiveSessionID(conversationID); sid != "" {
		if err := l.archiver.EndSession(sid, "split"); err != nil {
			l.logger.Error("failed to end session at split point", "error", err)
		}
	}

	// Start a new session for the post-split messages.
	if _, err := l.archiver.StartSession(conversationID); err != nil {
		l.logger.Error("failed to start new session after split", "error", err)
	}

	// Rebuild working memory with only the post-split messages.
	if err := l.memory.Clear(conversationID); err != nil {
		return fmt.Errorf("clear memory for split: %w", err)
	}
	for _, m := range postSplit {
		if err := l.memory.AddMessage(conversationID, m.Role, m.Content); err != nil {
			l.logger.Error("failed to re-add post-split message", "error", err, "role", m.Role)
		}
	}

	l.logger.Info("session split",
		"conversation", conversationID,
		"pre_split_msgs", len(preSplit),
		"post_split_msgs", len(postSplit),
	)
	return nil
}

// getAllMessages retrieves all messages for a conversation, preferring the
// full-fidelity GetAllMessages when available.
func (l *Loop) getAllMessages(conversationID string) []memory.Message {
	if full, ok := l.memory.(interface {
		GetAllMessages(string) []memory.Message
	}); ok {
		return full.GetAllMessages(conversationID)
	}
	return l.memory.GetMessages(conversationID)
}

// archiveAndEndSession archives all messages and ends the active session.
// Errors are logged but not propagated — callers should not be blocked by
// archive failures.
func (l *Loop) archiveAndEndSession(conversationID, reason string) {
	if l.archiver == nil {
		return
	}

	messages := l.getAllMessages(conversationID)
	if len(messages) > 0 {
		if err := l.archiver.ArchiveConversation(conversationID, messages, reason); err != nil {
			l.logger.Error("failed to archive conversation", "error", err)
		}
	}

	if sid := l.archiver.ActiveSessionID(conversationID); sid != "" {
		if err := l.archiver.EndSession(sid, reason); err != nil {
			l.logger.Error("failed to end session", "error", err)
		}
	}
}

// findSplitPoint determines the message index at which to split. Exactly
// one of atIndex (negative offset from the end) or atMessage (substring
// match) must be provided. Returns the index of the first post-split message.
func findSplitPoint(messages []memory.Message, atIndex int, atMessage string) (int, error) {
	if atIndex != 0 {
		// Negative offset from end: -1 = last message, -5 = five from end.
		idx := len(messages) + atIndex
		if idx <= 0 || idx >= len(messages) {
			return 0, fmt.Errorf("at_index %d out of range for %d messages", atIndex, len(messages))
		}
		return idx, nil
	}

	// Substring match — find the first message containing the string.
	for i, m := range messages {
		if i == 0 {
			continue // Splitting before the first message is a no-op.
		}
		if strings.Contains(m.Content, atMessage) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no message found containing %q", atMessage)
}

// ShutdownArchive archives the current conversation state before shutdown.
func (l *Loop) ShutdownArchive(conversationID string) {
	l.archiveAndEndSession(conversationID, "shutdown")
}

// TriggerCompaction manually triggers conversation compaction.
func (l *Loop) TriggerCompaction(ctx context.Context, conversationID string) error {
	if l.compactor == nil {
		return fmt.Errorf("compaction not configured")
	}
	return l.compactor.Compact(ctx, conversationID)
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

// recentSlice returns the last n messages from the slice, or all of them
// if the slice is shorter than n. Used for extraction context.
func recentSlice(msgs []memory.Message, n int) []memory.Message {
	if len(msgs) <= n {
		return msgs
	}
	return msgs[len(msgs)-n:]
}
