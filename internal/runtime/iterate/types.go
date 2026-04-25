package iterate

import (
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// Exhaustion reason constants describe why the engine stopped iterating.
const (
	ExhaustMaxIterations = "max_iterations"
	ExhaustTokenBudget   = "token_budget"
	ExhaustWallClock     = "wall_clock"
	ExhaustNoOutput      = "no_output"
	ExhaustIllegalTool   = "illegal_tool"
)

// IterationRecord collects per-iteration trace data. This replaces the
// identical iterationRecord structs that were independently defined in
// the agent and delegate packages.
type IterationRecord struct {
	Index                      int
	Model                      string
	InputTokens                int
	OutputTokens               int
	CacheCreationInputTokens   int
	CacheCreation5mInputTokens int
	CacheCreation1hInputTokens int
	CacheReadInputTokens       int
	ToolCallIDs                []string
	ToolsOffered               []string
	StartedAt                  time.Time
	DurationMs                 int64
	HasToolCalls               bool
	BreakReason                string
}

// Result is the outcome of an [Engine.Run] execution.
type Result struct {
	// Content is the final text response from the model.
	Content string

	// Model is the model that produced the final response. It may
	// differ from the initially configured model if error recovery
	// or failover changed it.
	Model string

	// InputTokens is the cumulative input token count across all iterations.
	InputTokens int

	// OutputTokens is the cumulative output token count across all iterations.
	OutputTokens int

	// CacheCreationInputTokens is the cumulative prompt-cache write token
	// count across all iterations when the provider reports it.
	CacheCreationInputTokens int

	// CacheCreation5mInputTokens and CacheCreation1hInputTokens break
	// down cache-write tokens by TTL bucket when the provider exposes
	// the split (Anthropic). The sum is ≤ CacheCreationInputTokens;
	// any shortfall reflects writes the provider didn't attribute.
	CacheCreation5mInputTokens int
	CacheCreation1hInputTokens int

	// CacheReadInputTokens is the cumulative prompt-cache read token
	// count across all iterations when the provider reports it.
	CacheReadInputTokens int

	// ToolsUsed maps tool name → invocation count.
	ToolsUsed map[string]int

	// Exhausted is true when the engine stopped due to a budget
	// (iterations, tokens, wall clock) rather than a text response.
	Exhausted bool

	// ExhaustReason is set when Exhausted is true.
	ExhaustReason string

	// Iterations records per-iteration trace data for archival and
	// dashboard display.
	Iterations []IterationRecord

	// Messages is the final message history including all tool
	// results. Consumers use it for archival and context replay.
	Messages []llm.Message

	// IterationCount is the number of iterations completed.
	IterationCount int
}
