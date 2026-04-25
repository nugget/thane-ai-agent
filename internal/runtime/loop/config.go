package loop

import (
	"context"
	"encoding"
	"errors"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
)

// ErrNoOp is returned by a [Config.Handler] to signal that the
// iteration ran but produced no meaningful work (e.g., all received
// events were filtered out). When returned, the loop never transitions
// to [StateProcessing], never publishes [events.KindLoopIterationStart],
// and skips all post-iteration accounting (snapshot, attempt count,
// recentConvIDs) — so the dashboard activity indicator stays quiet.
// ErrNoOp is not counted as an error and does not increment
// [Status.ConsecutiveErrors].
var ErrNoOp = errors.New("loop: no-op iteration")

// State represents the lifecycle state of a running loop.
type State string

// Loop states.
const (
	// StatePending means the loop is registered but not yet started
	// (e.g., waiting for a StartWhen condition).
	StatePending State = "pending"
	// StateSleeping means the loop is between iterations, waiting for
	// the next wake.
	StateSleeping State = "sleeping"
	// StateWaiting means the loop is blocked on a WaitFunc, waiting
	// for an external event to trigger the next iteration.
	StateWaiting State = "waiting"
	// StateProcessing means the loop is actively running an iteration
	// (LLM call or Handler execution).
	StateProcessing State = "processing"
	// StateError means the loop's last iteration failed. It will
	// retry on the next sleep cycle.
	StateError State = "error"
	// StateStopped means the loop has been cancelled and is no
	// longer running.
	StateStopped State = "stopped"
)

// RetriggerMode determines what happens when a loop's start condition
// fires again while the loop is already running.
type RetriggerMode int

var _ encoding.TextMarshaler = RetriggerMode(0)
var _ encoding.TextUnmarshaler = (*RetriggerMode)(nil)

const (
	// RetriggerSingle ignores re-triggers while the loop is running.
	RetriggerSingle RetriggerMode = iota
	// RetriggerRestart cancels the current loop and starts fresh.
	RetriggerRestart
	// RetriggerQueue queues the trigger and runs after current completes.
	RetriggerQueue
	// RetriggerSpawn spawns another instance of the loop.
	RetriggerSpawn
)

// String returns the stable textual form of the retrigger mode.
func (m RetriggerMode) String() string {
	switch m {
	case RetriggerSingle:
		return "single"
	case RetriggerRestart:
		return "restart"
	case RetriggerQueue:
		return "queue"
	case RetriggerSpawn:
		return "spawn"
	default:
		return fmt.Sprintf("unknown(%d)", int(m))
	}
}

// MarshalText implements [encoding.TextMarshaler].
func (m RetriggerMode) MarshalText() ([]byte, error) {
	switch m {
	case RetriggerSingle, RetriggerRestart, RetriggerQueue, RetriggerSpawn:
		return []byte(m.String()), nil
	default:
		return nil, fmt.Errorf("loop: unsupported retrigger mode %d", int(m))
	}
}

// UnmarshalText implements [encoding.TextUnmarshaler].
func (m *RetriggerMode) UnmarshalText(text []byte) error {
	if m == nil {
		return fmt.Errorf("loop: nil retrigger mode")
	}
	parsed, err := ParseRetriggerMode(string(text))
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

// ParseRetriggerMode parses the stable textual form of a retrigger mode.
func ParseRetriggerMode(raw string) (RetriggerMode, error) {
	switch raw {
	case "", "single":
		return RetriggerSingle, nil
	case "restart":
		return RetriggerRestart, nil
	case "queue":
		return RetriggerQueue, nil
	case "spawn":
		return RetriggerSpawn, nil
	default:
		return RetriggerSingle, fmt.Errorf("loop: unsupported retrigger mode %q", raw)
	}
}

// Config holds the configuration for a loop. All fields with zero values
// use sensible defaults.
type Config struct {
	// Name is the unique identifier for this loop. Required.
	Name string

	// Task is the LLM prompt for each iteration. It describes what
	// the loop should observe, check, or do on each wake.
	Task string

	// Operation describes the runtime pattern expected for the loop.
	Operation Operation

	// Completion describes how results should be delivered back to a
	// caller, conversation, or channel.
	Completion Completion

	// Tags are capability tags for tool scoping. When non-empty,
	// the loop's tool registry is filtered to tools matching these
	// tags (plus always-active tags).
	Tags []string

	// ExcludeTools lists tool names to exclude from the loop's
	// available tools.
	ExcludeTools []string

	// SleepMin is the minimum sleep duration between iterations.
	// Default: 30s.
	SleepMin time.Duration

	// SleepMax is the maximum sleep duration between iterations.
	// Default: 5m.
	SleepMax time.Duration

	// SleepDefault is the initial sleep duration before the loop
	// self-adjusts. Default: 1m.
	SleepDefault time.Duration

	// Jitter is the randomization factor applied to sleep durations
	// to break periodicity. Range [0.0, 1.0]. Nil is defaulted to
	// DefaultJitter (0.2) by applyDefaults. Use Float64Ptr(0) to
	// explicitly disable jitter.
	Jitter *float64

	// MaxDuration is the maximum wall-clock time the loop may run.
	// Zero means unlimited.
	MaxDuration time.Duration

	// MaxIter is the maximum number of iteration attempts the loop
	// may make (including failures). Zero means unlimited.
	MaxIter int

	// Supervisor enables frontier model dice rolls. When true, a
	// fraction of iterations (controlled by SupervisorProb) use a
	// more capable model for oversight.
	Supervisor bool

	// SupervisorProb is the probability [0.0, 1.0] that a given
	// iteration uses the supervisor model. Only meaningful when
	// Supervisor is true. Zero means never (use DefaultSupervisorProb
	// for the recommended default).
	SupervisorProb float64

	// QualityFloor is the minimum model quality rating for normal
	// iterations. Zero uses the router default.
	QualityFloor int

	// SupervisorContext is an optional prompt prepended to the Task
	// during supervisor iterations. Use it to give the frontier model
	// review instructions, recent iteration summaries, or oversight
	// criteria. Empty means supervisor runs the same Task as normal.
	SupervisorContext string

	// SupervisorQualityFloor is the minimum model quality rating
	// for supervisor iterations. Zero uses the router default.
	SupervisorQualityFloor int

	// OnRetrigger determines behavior when the loop's start
	// condition fires again while running. Default: RetriggerSingle.
	OnRetrigger RetriggerMode

	// TaskBuilder is called per-iteration to generate a dynamic prompt.
	// When set, the static Task field is ignored. The isSupervisor
	// argument indicates whether this is a supervisor iteration.
	TaskBuilder func(ctx context.Context, isSupervisor bool) (string, error) `json:"-"`

	// PostIterate is called after each successful iteration. Use it
	// for side effects like appending iteration logs. Errors are
	// logged but do not count as iteration failures.
	PostIterate func(ctx context.Context, result IterationResult) error `json:"-"`

	// WaitFunc blocks until an external event arrives. When set, the
	// loop enters [StateWaiting] and calls WaitFunc instead of
	// sleeping between iterations. The returned value is passed to
	// Handler (if set) or discarded for LLM-based loops. If WaitFunc
	// returns a non-context error, the loop treats it as an iteration
	// error (backoff + retry).
	//
	// A (nil, nil) return is treated as a no-op wake: the loop skips
	// the processing phase entirely and re-enters the wait state
	// without counting an iteration. This means nil is a reserved
	// sentinel payload. Implementations that need to deliver a
	// meaningful event with no associated data should return a non-nil
	// sentinel (e.g. a zero-value struct) instead of nil.
	WaitFunc func(ctx context.Context) (any, error) `json:"-"`

	// Handler processes each iteration directly without an LLM call.
	// When set, [Deps].Runner is not required. Receives the event
	// from WaitFunc (nil for timer-triggered loops). Handler-only
	// loops still track iterations, errors, and health.
	//
	// Return [ErrNoOp] to signal that the iteration produced no
	// meaningful work (e.g., all events were filtered). The loop
	// skips iteration accounting and continues to the next cycle.
	Handler func(ctx context.Context, event any) error `json:"-"`

	// Hints are merged into Request hints for each iteration.
	// Config hints override loop-generated defaults (e.g., setting
	// "source" to "metacognitive" instead of "loop").
	Hints map[string]string

	// FallbackContent is static text used when the loop's nested agent run
	// or direct request/reply execution finishes without any user-visible
	// content. Interactive loops can set this to guarantee a reply.
	FallbackContent string

	// Setup is called by [Registry.SpawnLoop] after [New] but before
	// [Loop.Start]. Use it to register tools or perform other setup
	// that requires a *Loop reference before the goroutine launches.
	Setup func(l *Loop) `json:"-"`

	// Metadata holds arbitrary key/value pairs for the loop.
	Metadata map[string]string

	// ParentID is the loop ID of the parent that spawned this loop,
	// if any. Empty for top-level loops.
	ParentID string
}

// Default configuration values. Exported so callers can reference them
// when building Config values without memorizing magic numbers.
const (
	DefaultSleepMin       = 30 * time.Second
	DefaultSleepMax       = 5 * time.Minute
	DefaultSleepDefault   = 1 * time.Minute
	DefaultJitter         = 0.2
	DefaultSupervisorProb = 0.1
)

// Float64Ptr returns a pointer to v. Use it to set optional *float64
// config fields like [Config.Jitter]:
//
//	Config{Jitter: loop.Float64Ptr(0.3)}   // custom jitter
//	Config{Jitter: loop.Float64Ptr(0)}     // explicitly no jitter
//	Config{}                                // nil → DefaultJitter
func Float64Ptr(v float64) *float64 { return &v }

// applyDefaults fills in zero-valued fields with sensible defaults.
// A nil Jitter is defaulted to DefaultJitter so that jitter is on by
// default; use Float64Ptr(0) to explicitly disable it.
// SupervisorProb is intentionally left as-is so that zero means
// "disabled" — callers opt in explicitly.
func (c *Config) applyDefaults() {
	if c.SleepMin == 0 {
		c.SleepMin = DefaultSleepMin
	}
	if c.SleepMax == 0 {
		c.SleepMax = DefaultSleepMax
	}
	if c.SleepDefault == 0 {
		c.SleepDefault = DefaultSleepDefault
	}
	if c.Jitter == nil {
		c.Jitter = Float64Ptr(DefaultJitter)
	}
}

// validate checks that post-default Config values are internally
// consistent. Called by [New] after [applyDefaults].
func (c *Config) validate() error {
	if c.Handler == nil && c.Task == "" && c.TaskBuilder == nil {
		return fmt.Errorf("loop: Task, TaskBuilder, or Handler is required")
	}
	if c.SleepMin <= 0 {
		return fmt.Errorf("loop: SleepMin must be positive, got %v", c.SleepMin)
	}
	if c.SleepMax < c.SleepMin {
		return fmt.Errorf("loop: SleepMax (%v) must be >= SleepMin (%v)", c.SleepMax, c.SleepMin)
	}
	if c.Jitter != nil && (*c.Jitter < 0 || *c.Jitter > 1) {
		return fmt.Errorf("loop: Jitter must be in [0, 1], got %v", *c.Jitter)
	}
	if c.SupervisorProb < 0 || c.SupervisorProb > 1 {
		return fmt.Errorf("loop: SupervisorProb must be in [0, 1], got %v", c.SupervisorProb)
	}
	return nil
}

// IterationResult holds data from a completed loop iteration, passed
// to [Config.PostIterate] callbacks.
type IterationResult struct {
	// ConvID is the conversation ID for this iteration.
	ConvID string
	// RequestID is the agent-generated request ID for this iteration.
	RequestID string
	// Model is the LLM model used for this iteration.
	Model string
	// InputTokens is the number of input tokens consumed.
	InputTokens int
	// OutputTokens is the number of output tokens produced.
	OutputTokens int
	// ContextWindow is the maximum context size (in tokens) of the model used.
	ContextWindow int
	// ToolsUsed maps tool names to invocation counts.
	ToolsUsed map[string]int
	// EffectiveTools lists the tools that were visible to the model for
	// this iteration after allowlists, excludes, capability tags, and
	// delegation gating were applied.
	EffectiveTools []string
	// ActiveTags holds the capability tags active at the end of this
	// iteration.
	ActiveTags []string
	// LoadedCapabilities captures the structured capability entries
	// corresponding to the tags loaded for this iteration.
	LoadedCapabilities []toolcatalog.LoadedCapabilityEntry
	// Elapsed is the wall-clock duration of the iteration.
	Elapsed time.Duration
	// Supervisor indicates whether this was a supervisor iteration.
	Supervisor bool
	// Sleep is the computed sleep duration before the next iteration.
	Sleep time.Duration
}

// IterationSnapshot is a serializable summary of a completed loop
// iteration, retained in a ring buffer for the dashboard timeline.
type IterationSnapshot struct {
	// Number is the 1-based iteration number (matches the loop's
	// cumulative iteration counter at the time of completion).
	Number int `json:"number"`
	// ConvID is the conversation ID used for this iteration.
	ConvID string `json:"conv_id,omitempty"`
	// RequestID is the agent-generated request ID, linking to
	// log_request_content for prompt/response inspection.
	RequestID string `json:"request_id,omitempty"`
	// Model is the LLM model used.
	Model string `json:"model,omitempty"`
	// InputTokens consumed by this iteration.
	InputTokens int `json:"input_tokens,omitempty"`
	// OutputTokens produced by this iteration.
	OutputTokens int `json:"output_tokens,omitempty"`
	// ContextWindow is the model's maximum context size in tokens.
	ContextWindow int `json:"context_window,omitempty"`
	// ToolsUsed maps tool names to invocation counts.
	ToolsUsed map[string]int `json:"tools_used,omitempty"`
	// EffectiveTools lists the tools visible to the model for this turn.
	EffectiveTools []string `json:"effective_tools,omitempty"`
	// ActiveTags holds the capability tags active for this turn.
	ActiveTags []string `json:"active_tags,omitempty"`
	// Tooling captures the authoritative tool/capability view for this turn.
	Tooling ToolingState `json:"tooling,omitempty"`
	// ElapsedMs is the wall-clock duration of the iteration in
	// milliseconds. Stored as int64 (not time.Duration) so the JSON
	// value is directly usable by the client without nanosecond
	// conversion.
	ElapsedMs int64 `json:"elapsed_ms"`
	// Supervisor indicates whether this was a supervisor iteration.
	Supervisor bool `json:"supervisor,omitempty"`
	// Error holds the error message if the iteration failed.
	Error string `json:"error,omitempty"`
	// StartedAt is when the iteration began.
	StartedAt time.Time `json:"started_at"`
	// CompletedAt is when the iteration finished.
	CompletedAt time.Time `json:"completed_at"`
	// SleepAfterMs is the computed sleep duration (in milliseconds)
	// following this iteration. Zero for WaitFunc-based loops.
	SleepAfterMs int64 `json:"sleep_after_ms,omitempty"`
	// WaitAfter is true when the loop entered WaitFunc after this
	// iteration instead of sleeping.
	WaitAfter bool `json:"wait_after,omitempty"`
	// Summary holds handler-reported metrics for this iteration,
	// written by handlers via [IterationSummary] during execution.
	// Values should be small scalars (int, string, bool).
	Summary map[string]any `json:"summary,omitempty"`
}

// Status is a snapshot of a loop's current state and metrics,
// suitable for external inspection via the registry.
type Status struct {
	// ID is the unique loop identifier.
	ID string `json:"id"`
	// Name is the human-readable loop name.
	Name string `json:"name"`
	// State is the current lifecycle state.
	State State `json:"state"`
	// ParentID is the ID of the parent loop, if any.
	ParentID string `json:"parent_id,omitempty"`
	// StartedAt is when the loop was started.
	StartedAt time.Time `json:"started_at"`
	// LastWakeAt is when the loop last began an iteration.
	LastWakeAt time.Time `json:"last_wake_at,omitempty"`
	// Iterations is the total number of completed (successful) iterations.
	Iterations int `json:"iterations"`
	// Attempts is the total number of iteration attempts (including failures).
	Attempts int `json:"attempts"`
	// TotalInputTokens is the cumulative input tokens across all iterations.
	TotalInputTokens int `json:"total_input_tokens"`
	// TotalOutputTokens is the cumulative output tokens across all iterations.
	TotalOutputTokens int `json:"total_output_tokens"`
	// LastInputTokens is the input token count from the most recent iteration.
	LastInputTokens int `json:"last_input_tokens,omitempty"`
	// LastOutputTokens is the output token count from the most recent iteration.
	LastOutputTokens int `json:"last_output_tokens,omitempty"`
	// ContextWindow is the maximum context size (in tokens) of the model used.
	ContextWindow int `json:"context_window,omitempty"`
	// LastError is the error message from the most recent failed iteration.
	LastError string `json:"last_error,omitempty"`
	// ConsecutiveErrors is the number of consecutive failed iterations.
	ConsecutiveErrors int `json:"consecutive_errors"`
	// RecentConvIDs holds conversation IDs from the most recent iterations
	// (up to 10), newest first. Used by the visualizer to query log entries
	// scoped to this loop.
	RecentConvIDs []string `json:"recent_conv_ids,omitempty"`
	// HandlerOnly is true when the loop uses a Handler instead of LLM
	// iterations. Handler-only loops have no token metrics.
	HandlerOnly bool `json:"handler_only,omitempty"`
	// EventDriven is true when the loop uses a WaitFunc instead of
	// timer-based sleeping.
	EventDriven bool `json:"event_driven,omitempty"`
	// RecentIterations holds up to 10 completed iteration snapshots
	// (newest first), used by the dashboard timeline.
	RecentIterations []IterationSnapshot `json:"recent_iterations,omitempty"`
	// LastSupervisorIter is the iteration number of the most recent
	// successful supervisor iteration. Zero means no supervisor
	// iteration has completed yet.
	LastSupervisorIter int `json:"last_supervisor_iter,omitempty"`
	// LLMContext holds enrichment data from the most recent
	// loop_llm_start event (model, est_tokens, messages, tools,
	// complexity, intent, reasoning). Only populated while the loop
	// is in processing state, so late-connecting dashboard clients
	// can display it immediately.
	LLMContext map[string]any `json:"llm_context,omitempty"`
	// ActiveTags holds the currently active capability tags at the time
	// of the snapshot. Nil when capability tagging is not configured.
	ActiveTags []string `json:"active_tags,omitempty"`
	// Tooling captures the resolved loop-level tool/capability state.
	Tooling ToolingState `json:"tooling,omitempty"`
	// Config is a copy of the loop's configuration.
	Config Config `json:"config"`
}
