package iterate

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/llm"
)

// Default limits applied when Config fields are zero-valued.
const (
	DefaultMaxIterations     = 50
	DefaultMaxIllegalStrikes = 2
	DefaultMaxToolRepeat     = 3
)

// Config controls an [Engine.Run] execution. Callbacks are optional;
// nil callbacks are silently skipped.
type Config struct {
	// --- Limits ---

	// MaxIterations is the maximum number of LLM call iterations.
	// After this many iterations without a text-only response, the
	// engine forces a final text call. Zero uses [DefaultMaxIterations].
	MaxIterations int

	// MaxIllegalStrikes is the number of consecutive iterations
	// containing illegal tool calls before the engine forces text.
	// The first strike allows one recovery iteration. Zero uses
	// [DefaultMaxIllegalStrikes].
	MaxIllegalStrikes int

	// MaxToolRepeat is the maximum number of times the same tool
	// may be called with the same arguments before the engine
	// injects a loop-break error. Zero uses [DefaultMaxToolRepeat].
	MaxToolRepeat int

	// --- LLM ---

	// Model is the model name passed to [llm.Client.ChatStream].
	Model string

	// LLM is the client used for chat completions.
	LLM llm.Client

	// Stream receives streaming events. Nil disables streaming.
	Stream llm.StreamCallback

	// --- Tools ---

	// ToolDefs returns the tool definitions for a given iteration.
	// It is called at the top of each iteration, allowing per-iteration
	// tool filtering (e.g., capability tags). Returning nil means no
	// tools are available for that iteration.
	ToolDefs func(iteration int) []map[string]any

	// Executor runs individual tool calls. If nil, the engine panics.
	Executor ToolExecutor

	// --- Callbacks ---

	// OnIterationStart fires at the top of each iteration before the
	// LLM call. Receives the iteration index, the current message history
	// (including assistant and tool messages appended by prior iterations),
	// and the tool definitions for this iteration.
	OnIterationStart func(ctx context.Context, iteration int, msgs []llm.Message, toolDefs []map[string]any)

	// OnLLMResponse fires after each successful LLM call, before tool
	// execution. Use it for logging and emitting stream events.
	OnLLMResponse func(ctx context.Context, resp *llm.ChatResponse, iteration int)

	// OnLLMError is called when an LLM ChatStream call fails. It may
	// implement retry, failover, or recovery logic. Returning a non-nil
	// ChatResponse and nil error means recovery succeeded; the returned
	// model name replaces the current model for subsequent iterations.
	// Returning a nil response and non-nil error propagates the failure.
	// If OnLLMError is nil, errors are returned immediately.
	OnLLMError func(ctx context.Context, err error, model string,
		msgs []llm.Message, toolDefs []map[string]any,
		stream llm.StreamCallback) (resp *llm.ChatResponse, newModel string, retErr error)

	// OnBeforeToolExec fires before each tool execution. The returned
	// context is passed to the tool executor. Use it to inject
	// conversation IDs, session IDs, tool call IDs, etc.
	OnBeforeToolExec func(ctx context.Context, iteration int, tc llm.ToolCall) context.Context

	// OnToolCallStart fires when a tool call begins execution. Use it
	// for stream events and tool call recording.
	OnToolCallStart func(ctx context.Context, tc llm.ToolCall)

	// OnToolCallDone fires after a tool call completes. The errMsg is
	// empty on success.
	OnToolCallDone func(ctx context.Context, name, result, errMsg string)

	// OnTextResponse fires when the model produces a text-only response
	// (no tool calls). Use it for memory storage, fact extraction, etc.
	OnTextResponse func(ctx context.Context, content string, msgs []llm.Message)

	// CheckBudget is called after each LLM response with the cumulative
	// output token count. Return true if the budget is exhausted and the
	// engine should force a text response. Nil means no budget.
	CheckBudget func(totalOutput int) bool

	// CheckToolAvail reports whether a tool is available in the current
	// iteration. Return false if the tool should be treated as illegal.
	// Nil means all tools are available.
	CheckToolAvail func(name string) bool

	// --- Text handling ---

	// DeferMixedText controls whether text content from mixed
	// (text + tool_call) responses is stripped from the message context
	// and deferred for later use. This prevents the model from restating
	// already-streamed text after tool execution (issue #347).
	// Agent sets true; delegate sets false.
	DeferMixedText bool

	// NudgeOnEmpty enables empty-response nudging: when the model
	// returns no content after tool iterations, inject NudgePrompt to
	// give it one more chance to produce text. Agent sets true.
	NudgeOnEmpty bool

	// NudgePrompt is the user-role message injected on empty responses.
	NudgePrompt string

	// FallbackContent is the static text returned when the model fails
	// to produce content even after nudging.
	FallbackContent string
}

// applyDefaults fills zero-valued fields with their defaults.
func (c *Config) applyDefaults() {
	if c.MaxIterations <= 0 {
		c.MaxIterations = DefaultMaxIterations
	}
	if c.MaxIllegalStrikes <= 0 {
		c.MaxIllegalStrikes = DefaultMaxIllegalStrikes
	}
	if c.MaxToolRepeat <= 0 {
		c.MaxToolRepeat = DefaultMaxToolRepeat
	}
}
