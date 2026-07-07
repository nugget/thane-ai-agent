package loop

import (
	"context"
	"encoding"
	"errors"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
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

// SupervisorTrigger names the cause that put an iteration into
// supervisor-turn mode. Reported on [IterationResult],
// [IterationSnapshot], and [Status.LastSupervisorTrigger] so
// retrospection and analytics can distinguish a Bernoulli win
// from a force_supervisor notification. Future routing-strategy
// experiments (alternation, cooldowns, slot-based selectors)
// will widen the value space; consumers should treat unknown
// values as "this iteration was a supervisor turn for some
// reason," not as an error.
type SupervisorTrigger string

const (
	// SupervisorTriggerNone is the zero value, used when the
	// iteration ran as a normal turn. Encodes as the empty
	// string in JSON so it's omitted from the wire format by
	// the standard `omitempty` tag.
	SupervisorTriggerNone SupervisorTrigger = ""
	// SupervisorTriggerRandom indicates the supervisor turn
	// fired because the per-wake Bernoulli trial (driven by
	// [Config.SupervisorProb]) won — the steady-state cause for
	// loops with Supervisor=true.
	SupervisorTriggerRandom SupervisorTrigger = "random"
	// SupervisorTriggerForced indicates the supervisor turn
	// fired because an external signal asked for it via the
	// `force_supervisor` notification field (mqtt, message_tools,
	// feed_wake, etc.). Costlier than a normal wake; reserve for
	// signals that genuinely warrant the extra capacity.
	SupervisorTriggerForced SupervisorTrigger = "forced"
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
	// the loop should observe, check, or do on each wake. Ignored when
	// TaskBuilder or TurnBuilder is set.
	Task string

	// Intent is the resolved purpose string for the loop — [Spec.Intent] if
	// set, otherwise the legacy metadata["intent"] fallback. Resolved once in
	// [Spec.ToConfig] so readers (e.g. LoopView) take a single source.
	Intent string

	// Origin carries the creation provenance copied from [Spec.Origin] so a
	// live loop's canonical view (FromStatus) surfaces the same origin a stored
	// definition does (FromDefinition). Nil for loops with no recorded origin.
	Origin *OriginInfo

	// Operation describes the runtime pattern expected for the loop.
	Operation Operation

	// Completion describes how results should be delivered back to a
	// caller, conversation, or channel.
	Completion Completion

	// Outputs declare durable documents this loop is allowed to
	// maintain through scoped runtime tools.
	Outputs []OutputSpec

	// Subscriptions are entities this loop wants to see in context
	// every iteration. Carried through from [Spec.Subscriptions] at
	// hydration time; see [Registry.AncestorSubscriptions] for the
	// ancestor walk that assembles the effective list.
	Subscriptions []EntitySubscription

	// Tags are capability tags for tool scoping. When non-empty,
	// the loop's tool registry is filtered to tools matching these
	// tags (plus core tags).
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

	// MidTurnInputBudget caps how many times a single turn may pull
	// newly-arrived mailbox input mid-flight (#1221). Zero uses
	// [defaultMidTurnInputBudget]. Only meaningful for loops with a live
	// inbound channel that sets AgentTurn.PullRender.
	MidTurnInputBudget int

	// MaxDuration is the maximum wall-clock time the loop may run.
	// Zero means unlimited.
	MaxDuration time.Duration

	// MaxIter is the maximum number of iteration attempts the loop
	// may make (including failures). Zero means unlimited.
	MaxIter int

	// Supervisor enables periodic supervisor turns: a Bernoulli trial
	// at each wake decides whether the iteration uses the elevated
	// SupervisorProfile overrides. Mirrors [Spec.Supervisor].
	Supervisor bool

	// SupervisorProb is the probability [0.0, 1.0] that a given
	// iteration is a supervisor turn. Only meaningful when
	// Supervisor is true. Zero means never. Mirrors
	// [Spec.SupervisorProb].
	SupervisorProb float64

	// SupervisorProfile carries the per-turn-mode overrides applied
	// during supervisor turns. Overlay on the loop's normal routing
	// shape (built from Profile-derived [requestBase]): any field
	// set here wins, any field left empty falls back to the normal
	// shape.
	//
	// Nil means "no SupervisorProfile overrides declared." Two
	// hardcoded baseline routing flips still apply to every
	// supervisor turn even with a nil SupervisorProfile:
	// `supervisor` is stamped to "true" and `local_only` is forced
	// to "false". A non-nil SupervisorProfile can override either
	// (e.g. set `local_only: "true"` to keep supervisor turns
	// local). See [prepareAgentTurnRequest] for the merge order.
	// Mirrors [Spec.SupervisorProfile].
	SupervisorProfile *router.LoopProfile

	// OnRetrigger determines behavior when the loop's start
	// condition fires again while running. Default: RetriggerSingle.
	OnRetrigger RetriggerMode

	// TaskBuilder is called per-iteration to generate a dynamic prompt.
	// When set, the static Task field is ignored. The returned prompt
	// is wrapped as an [AgentTurn] so task-based loops use the same
	// request preparation and runner path as [TurnBuilder].
	TaskBuilder func(ctx context.Context, isSupervisor bool) (string, error) `json:"-"`

	// TurnBuilder prepares a full agent request for each wake while
	// leaving model execution, telemetry, snapshots, and completion
	// handling in the loop runtime. Return nil, nil when the wake
	// produced no model-facing work. If unset, Task/TaskBuilder are
	// adapted into this shape. Use Handler instead for work that should
	// not run a model turn.
	TurnBuilder TurnBuilder `json:"-"`

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

	// RoutingFactors are merged into the Request's routing factors for
	// each iteration. Config factors override loop-generated defaults
	// (e.g., setting "source" to "metacognitive" instead of "loop").
	RoutingFactors map[string]string

	// DelegationGating sets the typed feature switch on each
	// iteration's Request. "disabled" gives the model direct tool
	// access (no orchestrator-and-delegate gating). Empty means no
	// override — the agent's default gating applies.
	DelegationGating string

	// FallbackContent is static text used when the loop's nested agent run
	// or direct request/reply execution finishes without any user-visible
	// content. Interactive loops can set this to guarantee a reply.
	FallbackContent string

	// Setup is called by [Registry.SpawnLoop] after [New] but before
	// [Loop.Start]. Use it to register tools or perform other setup
	// that requires a *Loop reference before the goroutine launches.
	Setup func(l *Loop) `json:"-"`

	// RuntimeTools are request-scoped tools attached during hydration.
	RuntimeTools []RuntimeTool `json:"-"`

	// OutputContextBuilder renders model-facing context for [Outputs].
	OutputContextBuilder OutputContextBuilder `json:"-"`

	// Metadata holds arbitrary key/value pairs for the loop.
	Metadata map[string]string

	// ParentID is the loop ID of the parent that spawned this loop,
	// if any. Empty for top-level loops.
	ParentID string

	// ParentName is the durable name of the parent loop. Carries
	// across the spec→config boundary so hydration paths that haven't
	// resolved a live ParentID yet still know which parent the loop
	// belongs under. The runtime resolves [ParentName] to [ParentID]
	// at launch time when a live parent exists.
	ParentName string
}

// Default configuration values. Exported so callers can reference them
// when building Config values without memorizing magic numbers.
const (
	DefaultSleepMin       = 30 * time.Second
	DefaultSleepMax       = 5 * time.Minute
	DefaultSleepDefault   = 1 * time.Minute
	DefaultJitter         = 0.2
	DefaultSupervisorProb = 0.1

	// eventDrivenErrorBackoff is the floor applied to wait-error
	// backoffs on event-driven loops. Event-driven specs intentionally
	// carry no sleep envelope, so [Loop.computeSleep] returns zero and
	// a chronically failing WaitFunc would otherwise tight-loop. Keep
	// this short enough that healthy upstream recovery is observed
	// quickly and long enough that the broken case doesn't burn CPU.
	eventDrivenErrorBackoff = 5 * time.Second
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
	if c.Operation == OperationContainer {
		// Containers never wake; don't synthesize sleep defaults
		// that would otherwise be inert-but-confusing on the Config.
		// The well-known core container (see [CoreLoopName]) shares
		// this contract by being a container.
		return
	}
	if c.Operation == OperationEventDriven {
		// Event-driven loops wake only on notifications or
		// [Config.WaitFunc] channel reads — they have no periodic
		// timer. Synthesizing sleep defaults would just write
		// inert-but-misleading values on the Config.
		return
	}
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
	if c.Operation == OperationContainer {
		// Containers are inert nodes — no execution hook, no wake
		// timer. Reject execution-shaped fields the same way
		// [Spec.Validate] does so callers that build a Config directly
		// (tests, internal adapters) get the same category-error
		// contract instead of having fields silently ignored at start.
		if err := containerShape(
			c.Name, c.Task,
			c.TaskBuilder != nil, c.TurnBuilder != nil, c.Handler != nil, c.WaitFunc != nil, c.PostIterate != nil,
			c.SleepMin, c.SleepMax, c.SleepDefault, c.MaxDuration,
			c.Jitter, c.MaxIter,
			c.Supervisor, c.SupervisorProb,
			len(c.Outputs), c.Completion,
		); err != nil {
			return err
		}
		return nil
	}
	if c.Handler == nil && c.Task == "" && c.TaskBuilder == nil && c.TurnBuilder == nil {
		return fmt.Errorf("loop: Task, TaskBuilder, TurnBuilder, or Handler is required")
	}
	if c.Operation != OperationEventDriven {
		// Timer-driven loops require a positive sleep envelope.
		// Event-driven loops are deliberately timer-less (sleep is
		// zero) — they wake only on notifications or WaitFunc, so
		// these checks would be incorrect for them.
		if c.SleepMin <= 0 {
			return fmt.Errorf("loop: SleepMin must be positive, got %v", c.SleepMin)
		}
		if c.SleepMax < c.SleepMin {
			return fmt.Errorf("loop: SleepMax (%v) must be >= SleepMin (%v)", c.SleepMax, c.SleepMin)
		}
	}
	if c.Jitter != nil && (*c.Jitter < 0 || *c.Jitter > 1) {
		return fmt.Errorf("loop: Jitter must be in [0, 1], got %v", *c.Jitter)
	}
	if c.SupervisorProb < 0 || c.SupervisorProb > 1 {
		return fmt.Errorf("loop: SupervisorProb must be in [0, 1], got %v", c.SupervisorProb)
	}
	if err := validateOutputs(c.Outputs); err != nil {
		return fmt.Errorf("loop: %w", err)
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
	// Supervisor reports whether this iteration ran a supervisor turn.
	// It is a convenience projection of SupervisorTrigger != "" for event
	// payloads and consumers that only need the boolean and don't match on
	// the trigger enum.
	Supervisor bool
	// SupervisorTrigger names why a supervisor turn fired (or
	// reports the empty string when this iteration ran as a normal
	// turn). Lets retrospection and routing-strategy experiments
	// distinguish "the dice came up" from "an external signal asked
	// for it" without re-deriving the cause from notification
	// history.
	SupervisorTrigger SupervisorTrigger
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
	// Supervisor reports whether this iteration ran a supervisor turn.
	// It is a convenience projection of SupervisorTrigger != "" kept on
	// the JSON wire so dashboard clients can read the boolean directly
	// without matching on supervisor_trigger.
	Supervisor bool `json:"supervisor,omitempty"`
	// SupervisorTrigger names why a supervisor turn fired. Empty
	// string for normal turns. Emitted alongside the bool so
	// retrospection and analytics tooling can distinguish
	// "random" (Bernoulli win) from "forced" (notification-driven)
	// without re-deriving the cause from event logs.
	SupervisorTrigger SupervisorTrigger `json:"supervisor_trigger,omitempty"`
	// MidTurnMerged is the number of mailbox messages folded into this turn
	// mid-flight (#1230); zero for an ordinary turn. Carried on the snapshot
	// (not just the live event) so the history/reload path renders the
	// "folded N messages" badge identically to a live-watching client.
	MidTurnMerged int `json:"midturn_merged,omitempty"`
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
	// SleepUntil is the scheduled wake instant while the loop is in a
	// timer-based sleep; zero when processing or event-driven (no timer to
	// wake on). A notification can still cut the sleep short. Projection-only
	// input for [LoopView] — `json:"-"` so it does not change the directly
	// serialized HTTP /v1/loops contract (where a model-facing delta string
	// belongs on the view, not a raw timestamp on Status).
	SleepUntil time.Time `json:"-"`
	// CurrentSleep is the post-clamp/post-jitter sleep duration being honored
	// this cycle; zero when not sleeping. Projection-only — `json:"-"` so a
	// raw time.Duration (nanoseconds) never leaks into the HTTP /v1/loops
	// JSON; [LoopView] emits the seconds form.
	CurrentSleep time.Duration `json:"-"`
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
	// EventDriven is true when the loop's run shape is event-driven
	// rather than timer-based — either it has a [Config.WaitFunc]
	// channel reader, or its operation kind is [OperationEventDriven]
	// (the persistable form that blocks on notification arrivals
	// instead of a periodic sleep).
	EventDriven bool `json:"event_driven,omitempty"`
	// PendingRetune is true while a queued retune ([Loop.QueueRetune])
	// has not yet been promoted into the live config — in practice only
	// while an in-flight turn is finishing under its previous config.
	// Sleeping and waiting loops promote (near-)immediately.
	PendingRetune bool `json:"pending_retune,omitempty"`
	// RecentIterations holds up to 10 completed iteration snapshots
	// (newest first), used by the dashboard timeline.
	RecentIterations []IterationSnapshot `json:"recent_iterations,omitempty"`
	// LastSupervisorIter is the iteration number of the most recent
	// iteration that ran a successful supervisor turn. Zero means
	// no supervisor turn has completed yet.
	LastSupervisorIter int `json:"last_supervisor_iter,omitempty"`
	// LastSupervisorTrigger is the cause of the most recent
	// supervisor turn (empty when LastSupervisorIter == 0). Lets
	// the running loop see "my last review was a random pass 14
	// turns ago" vs "my last review was forced by mqtt 3 turns
	// ago" without scanning event logs. Drives self-pacing
	// decisions.
	LastSupervisorTrigger SupervisorTrigger `json:"last_supervisor_trigger,omitempty"`
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
	// EffectiveTags is the post-ancestor-merge view of this loop's
	// capability tags, with provenance on each entry. Computed via
	// [Registry.EffectiveTags] when the loop is registered. Nil
	// when the loop is queried in isolation (tests that build a
	// Status manually) AND when the loop is registered but has no
	// effective tags to report — readers can't distinguish the two
	// from this field alone. This nil-conflation is ambiguous by
	// design and is an accepted v0.10.0 trade-off; the same applies
	// to the sibling Effective* fields below.
	EffectiveTags []EffectiveTag `json:"effective_tags,omitempty"`
	// EffectiveSubscriptions is the post-ancestor-merge view of this
	// loop's entity subscriptions, with provenance on each entry.
	// Companion to EffectiveTags with the same nil-conflation: nil
	// covers both "no registry hook installed" and "registry hook
	// installed but nothing to report."
	EffectiveSubscriptions []EffectiveSubscription `json:"effective_subscriptions,omitempty"`
	// EffectiveExcludeTools is the post-ancestor-merge view of this
	// loop's tool exclusions, with provenance on each entry. Same
	// nil-conflation as EffectiveTags.
	EffectiveExcludeTools []EffectiveExcludeTool `json:"effective_exclude_tools,omitempty"`
	// EffectiveRoutingFactors is the post-ancestor-merge view of this
	// loop's routing factors, child-wins on key collision. Same
	// nil-conflation as EffectiveTags.
	EffectiveRoutingFactors []EffectiveRoutingFactor `json:"effective_routing_factors,omitempty"`
	// EffectiveDelegationGating is the resolved delegation-gating
	// value plus its origin. Nil when no loop in the chain declared
	// a non-empty value (the agent default applies).
	EffectiveDelegationGating *EffectiveDelegationGating `json:"effective_delegation_gating,omitempty"`
	// Config is a copy of the loop's configuration.
	Config Config `json:"config"`
}
