package iterate

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
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
	// LLM call. Receives the iteration index, the active model name at
	// the start of the iteration (may differ from Config.Model if a
	// prior OnLLMError changed it), the current message history
	// (including assistant and tool messages appended by prior
	// iterations), and the tool definitions for this iteration.
	OnIterationStart func(ctx context.Context, iteration int, currentModel string, msgs []llm.Message, toolDefs []map[string]any)

	// OnLLMResponse fires after each successful LLM call, before tool
	// execution. Use it for logging and emitting stream events. For an
	// iteration's call ctx carries no call site; for the force-text
	// call that closes an exhausted run, which starts no iteration, it
	// carries the [llm.CallKindForceText] call site, so a caller can
	// tell a response that answers no iteration's request.
	OnLLMResponse func(ctx context.Context, resp *llm.ChatResponse, iteration int)

	// OnLLMError is called when an LLM ChatStream call fails. It may
	// implement retry, failover, or recovery logic. Returning a non-nil
	// ChatResponse and nil error means recovery succeeded; the returned
	// model name replaces the current model for subsequent iterations.
	// Returning a nil response and non-nil error propagates the failure.
	// ctx is the failed call's own context: it carries that call's
	// [llm.CallSite] and, when the run has one, its output budget, so a
	// handler that makes a call in the failed call's stead derives that
	// call's context from ctx (for example with [llm.WithCallKind]).
	// [llm.ErrOutputBudgetExhausted] ends the run with [ExhaustTokenBudget]
	// and static fallback content, without invoking this callback or
	// executing assistant output returned alongside the error.
	// If OnLLMError is nil, other errors are returned immediately.
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
	// output token count from successful iterations. Return true to stop
	// tool execution. Callers tracking usage across retries should consult
	// that shared total here and enforce its remaining allowance at the
	// client boundary, including any calls made by OnLLMError.
	// Nil leaves only MaxOutputTokens in effect.
	CheckBudget func(totalOutput int) bool

	// MaxOutputTokens is the output-token budget for the whole run, or 0
	// for unlimited. The engine supplies the remaining allowance to its
	// calls and stops once successful iterations consume it, even without
	// CheckBudget. Clients with internal retries must additionally deduct
	// failed-attempt usage at their call boundary; see CheckBudget.
	// Providers may enforce a lower ceiling or report no usage, so this
	// is not a guarantee of exact upstream token consumption.
	MaxOutputTokens int

	// CheckToolAvail reports whether a tool is available in the current
	// iteration. Return false if the tool should be treated as illegal.
	// Nil means all tools are available.
	CheckToolAvail func(name string) bool

	// PullInput is polled at the top of each iteration — after any prior
	// iteration's tool results are appended, before this iteration's LLM
	// call — so it can never split a tool_use/tool_result pair. A non-empty
	// return is appended verbatim as user-role messages and the iteration
	// proceeds with them in context. This is the seam that merges
	// newly-arrived conversation input into a live turn (#1221): a long
	// tool-heavy turn becomes aware of messages that arrive while it works.
	//
	// It is polled once more when an iteration ends in a text reply,
	// another iteration remains, and the output allowance left under
	// MaxOutputTokens is at least what that reply spent. A non-empty return
	// there holds the turn open: the reply joins history without reaching
	// OnTextResponse, the input follows it, and the next iteration answers
	// both. If a later reply comes back empty with nothing newer to stand
	// in, the run returns the held reply instead of FallbackContent. When
	// either condition fails the poll is skipped, so the reply closes the
	// turn and input that arrived while it was composed stays with the
	// caller.
	//
	// Nil disables mid-turn input. The callback owns delta-gating (return
	// only what is new) and any per-turn drain budget (return empty once
	// spent); the engine appends whatever it returns without inspection.
	PullInput func(ctx context.Context) []llm.Message

	// NormalizeToolCall can rewrite or repair a model-emitted tool call
	// before availability checks and execution. Use it for runtime
	// compatibility shims such as aliasing common hallucinated names to
	// the exact supported tool contract.
	NormalizeToolCall func(ctx context.Context, iteration int, tc llm.ToolCall) llm.ToolCall

	// --- Text handling ---

	// DeferMixedText controls whether text content from mixed
	// (text + tool_call) responses is stripped from the message context
	// and deferred for later use. This prevents the model from restating
	// already-streamed text after tool execution (issue #347).
	// Agent sets true; delegate sets false.
	DeferMixedText bool

	// NudgeOnEmpty enables empty-response nudging: when the model
	// returns no content and left no deferred text, inject NudgePrompt to
	// give it one more chance to produce text. The nudge is tried once per
	// run, and only while another iteration remains to answer it. An
	// empty reply on the last iteration with nothing to stand in and the
	// nudge still untried ends the run as exhausted, like any run that runs
	// out of iterations. Once the nudge was tried, an empty reply with
	// nothing to stand in gets FallbackContent on any iteration, the last
	// included, and the run is not exhausted. Agent sets true.
	NudgeOnEmpty bool

	// NudgePrompt is the user-role message injected on empty responses.
	NudgePrompt string

	// FallbackContent is the static text a run returns when it has no
	// reply to give. An empty response gets it only when NudgeOnEmpty is
	// set, the nudge was already tried, and nothing the model wrote
	// earlier can stand in (neither deferred text nor a reply held at
	// closure; see PullInput). A run the output budget ends without text,
	// or whose force-text recovery fails or comes back empty, gets it too.
	FallbackContent string

	// ReplyAwaited reports that someone is waiting on this turn's final
	// text — a channel message or an API response — rather than the turn
	// being a loop wake, a delegate, or other work nobody reads as a
	// reply. It selects the repeat guard's wording: only a turn with a
	// reply awaited is told to stop calling tools and answer, because on
	// any other turn that instruction reads as "abandon the work".
	ReplyAwaited bool

	// TargetKey names the document a tool call writes, so that each
	// tool's outcome is also tallied per target ([ToolOutcome.Targets]).
	// It is called for every call the model makes, including calls the
	// repeat guard refuses. Nil gives every call the empty target. A call
	// the tool refuses with [tools.ErrTargetRefused] is tallied under the
	// empty target whatever TargetKey named, and so is a guard refusal of
	// a target whose latest executed call was refused that way.
	TargetKey func(tool string, args map[string]any) string
}

func (c Config) outputBudgetExhausted(totalOutput int) bool {
	return (c.MaxOutputTokens > 0 && totalOutput >= c.MaxOutputTokens) ||
		(c.CheckBudget != nil && c.CheckBudget(totalOutput))
}

// closureHoldBlocker names what rules out holding the turn open after
// iteration i ended in a text reply that cost replyTokens to write, with
// totalOutput spent so far: "last_iteration" when no iteration remains to
// answer pulled input, "output_budget" when the allowance left under
// MaxOutputTokens is smaller than that reply. For text standing in for an
// empty response, replyTokens is what the call that wrote it spent. It
// returns "" when a hold may be taken.
func (c Config) closureHoldBlocker(i, totalOutput, replyTokens int) string {
	switch {
	case i+1 >= c.MaxIterations:
		return "last_iteration"
	case c.MaxOutputTokens > 0 && c.MaxOutputTokens-totalOutput < replyTokens:
		return "output_budget"
	default:
		return ""
	}
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
