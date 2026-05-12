package loop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// Runner executes one prepared model turn for the loop runtime. The
// application adapter satisfies this with *agent.Loop while keeping the
// loop package independent of the agent package. The stream callback is
// the caller-facing delivery path for raw runner events; loop telemetry is
// supplied separately through Request.OnProgress.
type Runner interface {
	Run(ctx context.Context, req Request, stream StreamCallback) (*Response, error)
}

// Request mirrors the loop-facing fields of agent.Request. The loop
// package defines its own type to avoid importing agent.
type Request struct {
	Model          string                 `yaml:"model,omitempty" json:"model,omitempty"`
	ConversationID string                 `yaml:"conversation_id,omitempty" json:"conversation_id,omitempty"`
	ChannelBinding *memory.ChannelBinding `yaml:"channel_binding,omitempty" json:"channel_binding,omitempty"`
	Messages       []Message              `yaml:"messages,omitempty" json:"messages,omitempty"`
	SkipContext    bool                   `yaml:"skip_context,omitempty" json:"skip_context,omitempty"`
	AllowedTools   []string               `yaml:"allowed_tools,omitempty" json:"allowed_tools,omitempty"`
	ExcludeTools   []string               `yaml:"exclude_tools,omitempty" json:"exclude_tools,omitempty"`
	SkipTagFilter  bool                   `yaml:"skip_tag_filter,omitempty" json:"skip_tag_filter,omitempty"`
	Hints          map[string]string      `yaml:"hints,omitempty" json:"hints,omitempty"`
	// InitialTags are capability tags to activate at the start of the Run,
	// in addition to always-active and channel-pinned tags. Used by loops
	// to carry forward tags activated in previous iterations.
	InitialTags []string `yaml:"initial_tags,omitempty" json:"initial_tags,omitempty"`
	// RuntimeTags are trusted runtime-asserted tags pinned for this runner
	// invocation only. Unlike InitialTags, the model cannot deactivate them
	// during the run. They are runtime-only so persisted specs and external
	// launch JSON cannot assert trust.
	RuntimeTags []string `yaml:"-" json:"-"`
	// RuntimeTools are request-scoped tools visible only to this run.
	RuntimeTools []RuntimeTool `yaml:"-" json:"-"`

	// OnProgress is called by the Runner during execution to report
	// in-flight activity (tool calls, LLM responses). The kind
	// parameter maps to an [events.Kind] constant; data holds
	// event-specific fields. The loop automatically injects loop_id
	// and loop_name into data before publishing. Nil means no
	// progress reporting.
	OnProgress func(kind string, data map[string]any) `yaml:"-" json:"-"`

	MaxIterations   int                 `yaml:"max_iterations,omitempty" json:"max_iterations,omitempty"`
	MaxOutputTokens int                 `yaml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`
	ToolTimeout     time.Duration       `yaml:"tool_timeout,omitempty" json:"tool_timeout,omitempty"`
	UsageRole       string              `yaml:"usage_role,omitempty" json:"usage_role,omitempty"`
	UsageTaskName   string              `yaml:"usage_task_name,omitempty" json:"usage_task_name,omitempty"`
	SystemPrompt    string              `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	FallbackContent string              `yaml:"fallback_content,omitempty" json:"fallback_content,omitempty"`
	PromptMode      agentctx.PromptMode `yaml:"prompt_mode,omitempty" json:"prompt_mode,omitempty"`

	// SuppressAlwaysContext drops the always-on bucket from the
	// system-prompt assembler's context output for this run. Default
	// false (main-loop behavior: include presence, episodic memory,
	// working memory, notification history, etc.). Delegates set true
	// so child agents see only tag-scoped context appropriate to the
	// bounded task.
	SuppressAlwaysContext bool `yaml:"suppress_always_context,omitempty" json:"suppress_always_context,omitempty"`
}

// RunRequest is kept as a compatibility alias while loops-ng migrates
// onto Request as the primary loop-facing run descriptor.
type RunRequest = Request

// Message is a chat message for the runner. It intentionally mirrors
// the subset of agent.Message that loop-owned request preparation needs.
type Message struct {
	// Role is the chat role, such as system, user, assistant, or tool.
	Role string `yaml:"role" json:"role"`

	// Content is the text body sent to the model.
	Content string `yaml:"content" json:"content"`

	// Images carries multimodal payloads for this message. It is
	// runtime-only because persisted loop specs should stay textual and
	// portable; HTTP ingress paths such as OWU populate it per request.
	Images []llm.ImageContent `yaml:"-" json:"-"`
}

// RunMessage is kept as a compatibility alias while loops-ng migrates
// onto Message as the primary loop-facing message type.
type RunMessage = Message

// Response mirrors agent.Response fields that loops consume. It holds
// the result of an LLM call executed by a [Runner].
type Response struct {
	Content                  string                              `yaml:"content,omitempty" json:"content,omitempty"`
	Model                    string                              `yaml:"model,omitempty" json:"model,omitempty"`
	FinishReason             string                              `yaml:"finish_reason,omitempty" json:"finish_reason,omitempty"`
	InputTokens              int                                 `yaml:"input_tokens,omitempty" json:"input_tokens,omitempty"`
	OutputTokens             int                                 `yaml:"output_tokens,omitempty" json:"output_tokens,omitempty"`
	CacheCreationInputTokens int                                 `yaml:"cache_creation_input_tokens,omitempty" json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int                                 `yaml:"cache_read_input_tokens,omitempty" json:"cache_read_input_tokens,omitempty"`
	ContextWindow            int                                 `yaml:"context_window,omitempty" json:"context_window,omitempty"`
	ToolsUsed                map[string]int                      `yaml:"tools_used,omitempty" json:"tools_used,omitempty"`
	EffectiveTools           []string                            `yaml:"effective_tools,omitempty" json:"effective_tools,omitempty"`
	LoadedCapabilities       []toolcatalog.LoadedCapabilityEntry `yaml:"loaded_capabilities,omitempty" json:"loaded_capabilities,omitempty"`
	RequestID                string                              `yaml:"request_id,omitempty" json:"request_id,omitempty"`
	Iterations               int                                 `yaml:"iterations,omitempty" json:"iterations,omitempty"`
	Exhausted                bool                                `yaml:"exhausted,omitempty" json:"exhausted,omitempty"`
	// ActiveTags is the set of capability tags that were active at the
	// end of the Run. Loops use this to carry forward activations to
	// subsequent iterations.
	ActiveTags []string `yaml:"active_tags,omitempty" json:"active_tags,omitempty"`
}

// RunResponse is kept as a compatibility alias while loops-ng
// migrates onto Response as the primary loop-facing response type.
type RunResponse = Response

// StreamCallback receives raw streaming events from a [Runner]. The event
// value is intentionally untyped at the loop boundary so the loop package
// does not import channel-specific or agent-specific event structs.
// Adapters that know the concrete event type should validate it before
// forwarding. Nil disables caller-facing streaming for the turn.
type StreamCallback func(event any)

// RandSource abstracts randomness for deterministic testing.
type RandSource interface {
	Float64() float64
}

type defaultRand struct{}

func (defaultRand) Float64() float64 { return rand.Float64() }

// ErrNilRunner is returned by [New] when Deps.Runner is nil.
var ErrNilRunner = errors.New("loop: Runner is required")

// recentConvIDsCap is the maximum number of conversation IDs retained
// in the ring buffer exposed via [Status.RecentConvIDs].
const recentConvIDsCap = 10

// recentIterationsCap is the maximum number of completed iteration
// snapshots retained for the dashboard timeline.
const recentIterationsCap = 10

// Deps holds injected dependencies for a loop. Using a struct avoids a
// growing parameter list as loops evolve.
type Deps struct {
	// Runner executes LLM iterations. Required unless [Config].Handler
	// is set (Handler-only loops do not call the Runner).
	Runner Runner
	// CompletionSink receives detached completion deliveries such as
	// background-task results injected into conversations.
	CompletionSink CompletionSink
	// Logger for loop operations. Defaults to slog.Default().
	Logger *slog.Logger
	// EventBus publishes loop lifecycle events. Nil disables events.
	EventBus *events.Bus
	// Rand provides randomness for sleep jitter and supervisor dice.
	// Nil uses math/rand/v2 default.
	Rand RandSource
}

// Loop is a persistent background goroutine that iterates on a timer
// or in response to external events. Each iteration runs an LLM call
// via the agent runner, prepares an agent turn via [Config.TurnBuilder],
// or calls a direct [Config.Handler] for
// infrastructure loops that don't need an LLM. Create with [New],
// start with [Start], stop with [Stop].
type Loop struct {
	id     string
	config Config
	deps   Deps

	mu        sync.Mutex
	state     State
	started   bool
	stopped   bool // set by Stop to prevent Start after Stop
	cancel    context.CancelFunc
	done      chan struct{}
	startedAt time.Time

	// eventSeq is a monotonic counter for state-change events.
	// Published outside the lock to avoid deadlock if the event bus
	// blocks, so consumers use the sequence number to reorder.
	eventSeq atomic.Int64

	// Metrics updated during execution.
	lastWakeAt        time.Time
	iterations        int // successful iterations
	attempts          int // total attempts (including failures)
	totalInputTokens  int
	totalOutputTokens int
	lastInputTokens   int
	lastOutputTokens  int
	contextWindow     int
	lastError         string

	// currentConvID is the conversation ID of the in-flight iteration.
	// Set at the start of each iteration, cleared after. Tool handlers
	// read it via [Loop.CurrentConvID].
	currentConvID string

	// requestBase carries the per-iteration request shaping derived
	// from a loops-ng [Spec]'s [router.LoopProfile]. It is additive and
	// only populated for loops created via [NewFromSpec].
	requestBase Request

	// requestOverride carries launch-specific per-run overrides
	// applied on top of the spec/profile-derived request shaping.
	requestOverride Request

	// requestInstructions is extra guidance derived from a loops-ng
	// [Spec]'s [router.LoopProfile]. It is prepended to each iteration
	// task when present.
	requestInstructions string

	// lastResponse is the most recent successful runner response for
	// this loop. Joined request/reply launches surface this as their
	// concrete result payload once the loop finishes.
	lastResponse *Response

	// taskOverride replaces the spec/config task for launch-driven,
	// one-shot runs that need per-run input without mutating the
	// underlying [Spec].
	taskOverride string

	// activatedTags tracks capability tags activated during previous
	// iterations. Carried forward via InitialTags on the next Request so
	// activations persist across the loop's lifetime.
	activatedTags []string

	// recentConvIDs is a ring buffer of conversation IDs from the most
	// recent iterations (newest first, up to recentConvIDsCap). Used by
	// the visualizer to query log entries scoped to this loop.
	recentConvIDs []string

	// recentIterations is a ring buffer of completed iteration
	// snapshots (newest first, up to recentIterationsCap). Used by
	// the dashboard timeline to show iteration history.
	recentIterations []IterationSnapshot

	// lastSupervisorIter is the iteration number of the most recent
	// successful supervisor iteration. Zero means none yet.
	lastSupervisorIter int

	// llmContext holds enrichment data from the most recent
	// loop_llm_start progress event (model, est_tokens, messages,
	// tools, complexity, intent, reasoning). Set by makeProgressFunc
	// during processing, cleared when the iteration ends. Included
	// in Status() so late-connecting dashboard clients see it.
	llmContext map[string]any

	// activeTagsFunc is an optional callback that returns the currently
	// active capability tags. When set, Status() includes the result
	// in ActiveTags so the dashboard can display dynamic capabilities.
	activeTagsFunc func() []string

	// nextSleep can be set externally (e.g., by a set_next_sleep
	// tool handler) to override the default sleep for one cycle.
	nextSleep time.Duration

	// consecutiveErrors tracks sequential failures for backoff.
	consecutiveErrors int

	// wakeCh interrupts timer sleep when a signal is queued for the next
	// iteration. Buffered size 1 coalesces multiple wake requests.
	wakeCh chan struct{}

	// pendingNotifies are one-shot envelopes injected into the next
	// iteration prompt or handler context.
	pendingNotifies []pendingNotify
}

// New creates a loop with the given configuration and dependencies.
// Returns an error if required fields are missing or invalid.
// Call [Loop.Start] to launch the background goroutine.
func New(cfg Config, deps Deps) (*Loop, error) {
	if cfg.Handler == nil && deps.Runner == nil {
		return nil, ErrNilRunner
	}
	if cfg.Name == "" {
		return nil, errors.New("loop: Name is required")
	}

	cfg.applyDefaults()

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Rand == nil {
		deps.Rand = defaultRand{}
	}

	id, _ := uuid.NewV7()

	return &Loop{
		id:     id.String(),
		config: cfg,
		deps:   deps,
		state:  StatePending,
		wakeCh: make(chan struct{}, 1),
	}, nil
}

// NewFromSpec creates a loop from a [Spec], validating the loops-ng
// fields before compiling the engine-facing [Config]. This is an
// additive bridge for gradually moving call sites onto Spec.
func NewFromSpec(spec Spec, deps Deps) (*Loop, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	l, err := New(spec.ToConfig(), deps)
	if err != nil {
		return nil, err
	}
	l.requestBase = spec.profileRequest()
	l.requestInstructions = spec.Profile.Instructions
	return l, nil
}

// NewFromLaunch creates a loop from a [Launch], validating the launch,
// compiling the underlying [Spec], and applying per-run request and
// metadata overrides. This is the additive bridge used by
// [Registry.Launch].
func NewFromLaunch(launch Launch, deps Deps) (*Loop, error) {
	if err := launch.Validate(); err != nil {
		return nil, err
	}

	spec := launch.Spec
	if launch.Task != "" && spec.Task == "" && spec.TaskBuilder == nil && spec.TurnBuilder == nil && spec.Handler == nil {
		spec.Task = launch.Task
	}
	cfg := spec.ToConfig()
	if launch.ParentID != "" {
		cfg.ParentID = launch.ParentID
	}
	if len(launch.Metadata) > 0 {
		merged := cloneStringMap(cfg.Metadata)
		if merged == nil {
			merged = make(map[string]string, len(launch.Metadata))
		}
		for k, v := range launch.Metadata {
			merged[k] = v
		}
		cfg.Metadata = merged
	}

	l, err := New(cfg, deps)
	if err != nil {
		return nil, err
	}
	l.requestBase = spec.profileRequest()
	l.requestInstructions = spec.Profile.Instructions
	l.requestOverride = launch.requestOverride()
	l.taskOverride = launch.Task
	return l, nil
}

// ID returns the unique loop identifier.
func (l *Loop) ID() string { return l.id }

// Name returns the loop's configured name.
func (l *Loop) Name() string { return l.config.Name }

// ErrLoopStopped is returned by [Loop.Start] when the loop has already
// been stopped. A stopped loop cannot be restarted.
var ErrLoopStopped = errors.New("loop: cannot start a stopped loop")

// Start launches the background goroutine. Calling Start on an already
// running loop is a no-op (returns nil). Returns [ErrLoopStopped] if
// [Loop.Stop] was called before Start. The goroutine runs until ctx is
// cancelled or [Loop.Stop] is called.
func (l *Loop) Start(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.stopped {
		return ErrLoopStopped
	}
	if l.started {
		return nil
	}
	l.started = true
	l.startedAt = time.Now()

	loopCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	l.done = make(chan struct{})

	go l.run(loopCtx)
	return nil
}

// Stop cancels the loop and waits for the goroutine to exit. Safe to
// call multiple times or before Start. After Stop, [Loop.Start] will
// return [ErrLoopStopped]. Blocks until the goroutine exits or 10
// seconds elapse.
func (l *Loop) Stop() {
	l.mu.Lock()
	l.stopped = true
	done := l.done
	cancel := l.cancel
	l.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			l.deps.Logger.Warn("loop.Stop timed out waiting for goroutine exit",
				"loop_id", l.id,
				"loop_name", l.config.Name,
			)
		}
	}
}

// cancel0 fires the loop's context cancellation without waiting for
// the goroutine to exit. Used by [Registry.ShutdownAll] to cancel all
// loops in parallel before waiting for them to drain.
func (l *Loop) cancel0() {
	l.mu.Lock()
	cancel := l.cancel
	l.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// Done returns a channel that is closed when the loop's goroutine exits.
// Returns nil if the loop has not been started.
func (l *Loop) Done() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.done
}

// Status returns a snapshot of the loop's current state and metrics.
// The returned Config is a deep copy; callers cannot mutate loop state
// via the snapshot.
func (l *Loop) Status() Status {
	l.mu.Lock()

	// Capture the callback reference under the lock; call it after
	// releasing to avoid holding l.mu while the agent's tagMu is acquired.
	atFunc := l.activeTagsFunc

	// Deep copy Config to prevent callers from mutating internal state
	// via shared slices/maps. Function fields are cleared — they can't
	// be serialized and shouldn't leak to callers.
	cfgCopy := l.config
	cfgCopy.TaskBuilder = nil
	cfgCopy.TurnBuilder = nil
	cfgCopy.PostIterate = nil
	cfgCopy.WaitFunc = nil
	cfgCopy.Handler = nil
	cfgCopy.Setup = nil
	cfgCopy.RuntimeTools = nil
	cfgCopy.OutputContextBuilder = nil
	cfgCopy.Outputs = cloneOutputs(l.config.Outputs)
	if l.config.Tags != nil {
		cfgCopy.Tags = make([]string, len(l.config.Tags))
		copy(cfgCopy.Tags, l.config.Tags)
	}
	if l.config.ExcludeTools != nil {
		cfgCopy.ExcludeTools = make([]string, len(l.config.ExcludeTools))
		copy(cfgCopy.ExcludeTools, l.config.ExcludeTools)
	}
	if l.config.Hints != nil {
		cfgCopy.Hints = make(map[string]string, len(l.config.Hints))
		for k, v := range l.config.Hints {
			cfgCopy.Hints[k] = v
		}
	}
	if l.config.Metadata != nil {
		cfgCopy.Metadata = make(map[string]string, len(l.config.Metadata))
		for k, v := range l.config.Metadata {
			cfgCopy.Metadata[k] = v
		}
	}

	// Deep copy recentConvIDs so callers can't mutate internal state.
	var convIDsCopy []string
	if len(l.recentConvIDs) > 0 {
		convIDsCopy = make([]string, len(l.recentConvIDs))
		copy(convIDsCopy, l.recentConvIDs)
	}

	// Deep copy recentIterations (including ToolsUsed maps).
	var iterCopy []IterationSnapshot
	if len(l.recentIterations) > 0 {
		iterCopy = make([]IterationSnapshot, len(l.recentIterations))
		copy(iterCopy, l.recentIterations)
		for i, snap := range iterCopy {
			if len(snap.ToolsUsed) > 0 {
				m := make(map[string]int, len(snap.ToolsUsed))
				for k, v := range snap.ToolsUsed {
					m[k] = v
				}
				iterCopy[i].ToolsUsed = m
			}
			if len(snap.EffectiveTools) > 0 {
				iterCopy[i].EffectiveTools = append([]string(nil), snap.EffectiveTools...)
			}
			if len(snap.ActiveTags) > 0 {
				iterCopy[i].ActiveTags = append([]string(nil), snap.ActiveTags...)
			}
			if len(snap.Summary) > 0 {
				s := make(map[string]any, len(snap.Summary))
				for k, v := range snap.Summary {
					s[k] = v
				}
				iterCopy[i].Summary = s
			}
		}
	}

	// Deep copy llmContext for late-connecting dashboard clients.
	var llmCtxCopy map[string]any
	requestBaseInitialTags := append([]string(nil), l.requestBase.InitialTags...)
	requestOverrideInitialTags := append([]string(nil), l.requestOverride.InitialTags...)
	activatedTagsCopy := append([]string(nil), l.activatedTags...)
	handlerOnly := l.config.Handler != nil
	if len(l.llmContext) > 0 {
		llmCtxCopy = make(map[string]any, len(l.llmContext))
		for k, v := range l.llmContext {
			llmCtxCopy[k] = v
		}
	}

	s := Status{
		ID:                 l.id,
		Name:               l.config.Name,
		State:              l.state,
		ParentID:           l.config.ParentID,
		StartedAt:          l.startedAt,
		LastWakeAt:         l.lastWakeAt,
		Iterations:         l.iterations,
		Attempts:           l.attempts,
		TotalInputTokens:   l.totalInputTokens,
		TotalOutputTokens:  l.totalOutputTokens,
		LastInputTokens:    l.lastInputTokens,
		LastOutputTokens:   l.lastOutputTokens,
		ContextWindow:      l.contextWindow,
		LastError:          l.lastError,
		ConsecutiveErrors:  l.consecutiveErrors,
		RecentConvIDs:      convIDsCopy,
		RecentIterations:   iterCopy,
		LLMContext:         llmCtxCopy,
		LastSupervisorIter: l.lastSupervisorIter,
		HandlerOnly:        l.config.Handler != nil,
		EventDriven:        l.config.WaitFunc != nil,
		Config:             cfgCopy,
	}
	l.mu.Unlock()

	// Call the active tags callback outside the lock to avoid
	// lock-ordering issues with the agent loop's tagMu.
	// Deep-copy the result so callers can't mutate internal state.
	defaultLoadedTags := mergeUniqueStrings(cfgCopy.Tags, requestBaseInitialTags, requestOverrideInitialTags, activatedTagsCopy)
	if atFunc != nil {
		if tags := atFunc(); len(tags) > 0 {
			cp := make([]string, len(tags))
			copy(cp, tags)
			s.ActiveTags = cp
		}
	}
	if len(s.ActiveTags) == 0 && len(defaultLoadedTags) > 0 {
		s.ActiveTags = append([]string(nil), defaultLoadedTags...)
	}
	if len(s.ActiveTags) == 0 && !handlerOnly && len(iterCopy) > 0 && len(iterCopy[0].ActiveTags) > 0 {
		s.ActiveTags = append([]string(nil), iterCopy[0].ActiveTags...)
	}

	effectiveTools := []string(nil)
	if tooling, ok := llmCtxCopy["tooling"].(ToolingState); ok && len(tooling.EffectiveTools) > 0 {
		effectiveTools = append(effectiveTools, tooling.EffectiveTools...)
	} else if got, ok := llmCtxCopy["effective_tools"].([]string); ok {
		effectiveTools = append(effectiveTools, got...)
	} else if !handlerOnly && len(iterCopy) > 0 {
		effectiveTools = append(effectiveTools, iterCopy[0].EffectiveTools...)
	}

	loadedCaps := []toolcatalog.LoadedCapabilityEntry(nil)
	if tooling, ok := llmCtxCopy["tooling"].(ToolingState); ok && len(tooling.LoadedCapabilities) > 0 {
		loadedCaps = append(loadedCaps, tooling.LoadedCapabilities...)
	} else if !handlerOnly && len(iterCopy) > 0 {
		loadedCaps = append(loadedCaps, iterCopy[0].Tooling.LoadedCapabilities...)
	}

	lastToolsUsed := map[string]int(nil)
	if !handlerOnly && len(iterCopy) > 0 && len(iterCopy[0].ToolsUsed) > 0 {
		lastToolsUsed = iterCopy[0].ToolsUsed
	}
	configuredTags := mergeUniqueStrings(cfgCopy.Tags, requestBaseInitialTags, requestOverrideInitialTags)
	s.Tooling = BuildToolingState(configuredTags, s.ActiveTags, effectiveTools, cfgCopy.ExcludeTools, loadedCaps, lastToolsUsed)

	return s
}

// SetNextSleep sets the sleep duration for the next cycle. This is
// intended for tool handlers (e.g., set_next_sleep) to communicate the
// LLM's chosen sleep duration back to the loop.
func (l *Loop) SetNextSleep(d time.Duration) {
	l.mu.Lock()
	l.nextSleep = d
	l.mu.Unlock()
}

// SetActiveTagsFunc configures an optional callback that returns the
// currently active capability tags. When set, [Status] includes the
// result so the dashboard can display dynamically activated capabilities.
func (l *Loop) SetActiveTagsFunc(fn func() []string) {
	l.mu.Lock()
	l.activeTagsFunc = fn
	l.mu.Unlock()
}

// CurrentConvID returns the conversation ID of the in-flight iteration,
// or empty string if no iteration is running. Tool handlers use this to
// tag their outputs with the current conversation.
func (l *Loop) CurrentConvID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.currentConvID
}

// run is the main goroutine body. It iterates until the context is
// cancelled, max duration is reached, or max iterations is exhausted.
func (l *Loop) run(ctx context.Context) {
	defer close(l.done)

	logger := l.deps.Logger.With(
		"subsystem", logging.SubsystemLoop,
		"loop_id", l.id,
		"loop_name", l.config.Name,
	)
	ctx = logging.WithLogger(ctx, logger)
	// Stamp this loop's ID onto the run context so every descendant
	// context — iterCtx, handlerCtx, turnCtx, the agent runner's ctx,
	// tool calls, and any delegates those tools launch — sees this
	// loop as the LoopID origin rather than inheriting whatever ID
	// the spawner's context happened to carry. Without this, a child
	// loop spawned from a parent's handler context (where withLoopID
	// is set to the parent's ID) silently inherits the parent's ID
	// through every downstream context, and any delegate launched
	// from within the child loop's run is mis-parented onto the
	// parent loop in the topology.
	ctx = withLoopID(ctx, l.id)

	// Initial state depends on whether the loop waits for events or
	// sleeps on a timer. WaitFunc loops enter StateWaiting immediately
	// (they block at the top of the loop); timer loops enter
	// StateSleeping (they sleep on startup, then process, then sleep).
	if l.config.WaitFunc != nil {
		l.setState(StateWaiting)
	} else {
		l.setState(StateSleeping)
	}
	l.publishEvent(events.Event{
		Timestamp: time.Now(),
		Source:    events.SourceLoop,
		Kind:      events.KindLoopStarted,
		Data: map[string]any{
			"loop_id":   l.id,
			"loop_name": l.config.Name,
			"parent_id": l.config.ParentID,
		},
	})

	logger.Info("loop started",
		"sleep_min", l.config.SleepMin,
		"sleep_max", l.config.SleepMax,
		"max_duration", l.config.MaxDuration,
		"max_iter", l.config.MaxIter,
		"supervisor", l.config.Supervisor,
		"handler_only", l.config.Handler != nil,
		"event_driven", l.config.WaitFunc != nil,
	)

	// Apply max duration as a context deadline if configured.
	if l.config.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, l.config.MaxDuration)
		defer cancel()
	}

	// --- INITIAL SLEEP (timer-driven loops only) ---
	// On fresh startup, timer-driven loops sleep before their first
	// iteration instead of firing immediately. The delay is jittered
	// from SleepDefault to stagger loop starts and avoid a thundering
	// herd of simultaneous iterations after a restart.
	if l.config.WaitFunc == nil {
		initialSleep := l.applyJitter(l.config.SleepDefault)
		initialSleep = l.clamp(initialSleep)

		l.publishEvent(events.Event{
			Timestamp: time.Now(),
			Source:    events.SourceLoop,
			Kind:      events.KindLoopSleepStart,
			Data: map[string]any{
				"loop_id":        l.id,
				"loop_name":      l.config.Name,
				"sleep_duration": initialSleep.String(),
				"initial":        true,
			},
		})

		logger.Debug("loop initial sleep before first iteration",
			"duration", initialSleep.Round(time.Second),
		)

		if !l.sleep(ctx, initialSleep) {
			logger.Debug("loop stopped during initial sleep",
				"phase", "initial_sleep",
			)
			l.emitStopped()
			return
		}
	}

	for {
		var event any // payload from WaitFunc; nil for timer-driven loops.

		// Check attempt limit (counts both successes and failures).
		l.mu.Lock()
		attemptCount := l.attempts
		l.mu.Unlock()
		if l.config.MaxIter > 0 && attemptCount >= l.config.MaxIter {
			logger.Info("loop max iterations reached",
				"max_iter", l.config.MaxIter,
				"attempts", attemptCount,
			)
			break
		}

		// --- WAIT PHASE (top of loop, WaitFunc loops only) ---
		// Event-driven loops block here until an external event
		// arrives. This runs before the first iteration — no event
		// has occurred yet, so there is nothing to process.
		if l.config.WaitFunc != nil {
			l.setState(StateWaiting)
			l.publishEvent(events.Event{
				Timestamp: time.Now(),
				Source:    events.SourceLoop,
				Kind:      events.KindLoopWaitStart,
				Data: map[string]any{
					"loop_id":   l.id,
					"loop_name": l.config.Name,
				},
			})
			logger.Debug("loop waiting for event")

			var waitErr error
			event, waitErr = l.waitForEvent(ctx)
			if waitErr != nil {
				if ctx.Err() != nil || errors.Is(waitErr, context.Canceled) || errors.Is(waitErr, context.DeadlineExceeded) {
					logger.Debug("loop stopped while waiting for event",
						"phase", "wait",
						"error", waitErr,
					)
					break
				}
				// WaitFunc error (not cancellation) — apply backoff
				// before retrying. This prevents tight-looping when
				// the upstream source is temporarily broken.
				l.mu.Lock()
				l.lastError = waitErr.Error()
				l.consecutiveErrors++
				consecutiveErrors := l.consecutiveErrors
				l.mu.Unlock()
				l.setState(StateError)
				l.publishEvent(events.Event{
					Timestamp: time.Now(),
					Source:    events.SourceLoop,
					Kind:      events.KindLoopError,
					Data: map[string]any{
						"loop_id":   l.id,
						"loop_name": l.config.Name,
						"error":     waitErr.Error(),
						"phase":     "wait",
					},
				})
				backoff := l.computeSleep()
				logger.Warn("loop wait failed",
					"error", waitErr,
					"consecutive_errors", consecutiveErrors,
					"backoff", backoff.Round(time.Second),
				)
				if !l.sleep(ctx, backoff) {
					logger.Debug("loop stopped during wait backoff",
						"phase", "wait_backoff",
						"backoff", backoff.Round(time.Second),
					)
					break
				}
				continue // retry from top
			}
			// Successful wait — clear any consecutive errors from
			// prior wait failures so backoff resets.
			l.mu.Lock()
			if l.consecutiveErrors > 0 && l.lastError != "" {
				l.consecutiveErrors = 0
				l.lastError = ""
			}
			l.mu.Unlock()

			// A nil payload with no error is a no-op wake (e.g.
			// internal housekeeping or metrics tick). Skip the
			// processing phase so it doesn't count as an iteration.
			// See Config.WaitFunc for the nil-payload contract.
			if event == nil {
				continue
			}
		}

		signals, forceSupervisor := l.consumePendingNotifies()

		// --- PROCESSING PHASE ---
		// Reset tool-provided sleep override and set current conversation ID.
		convID := fmt.Sprintf("loop-%s-%d-%d", l.config.Name, attemptCount+1, time.Now().UnixMilli())
		l.mu.Lock()
		l.nextSleep = 0
		l.currentConvID = convID
		l.mu.Unlock()

		// Determine if this is a supervisor iteration.
		isSupervisor := forceSupervisor || (l.config.Supervisor && l.config.SupervisorProb > 0 && l.deps.Rand.Float64() < l.config.SupervisorProb)

		iterLog := logger.With(
			"conversation_id", convID,
			"supervisor", isSupervisor,
			"attempt", attemptCount+1,
		)
		iterCtx := logging.WithLogger(ctx, iterLog)

		iterStartTime := time.Now()

		// Dispatch: Handler runs directly; otherwise build an agent
		// turn and let the loop runtime execute it.
		var result *IterationResult
		var err error
		var handlerSummary map[string]any
		var noOp bool
		// Transition to processing and emit iteration_start before
		// dispatching work so the dashboard sees activity immediately.
		l.setState(StateProcessing)
		l.publishEvent(events.Event{
			Timestamp: time.Now(),
			Source:    events.SourceLoop,
			Kind:      events.KindLoopIterationStart,
			Data: map[string]any{
				"loop_id":          l.id,
				"loop_name":        l.config.Name,
				"conversation_id":  convID,
				"supervisor":       isSupervisor,
				"attempt":          attemptCount + 1,
				"signal_envelopes": len(signals),
			},
		})
		iterLog.Debug("loop iteration starting")

		if l.config.Handler != nil {
			iterStart := time.Now()
			summary := make(map[string]any)
			handlerCtx := context.WithValue(iterCtx, iterSummaryKey{}, summary)
			handlerCtx = context.WithValue(handlerCtx, progressFuncKey{}, l.makeProgressFunc())
			handlerCtx = withLoopID(handlerCtx, l.id)
			handlerCtx = withFallbackContent(handlerCtx, l.config.FallbackContent)
			handlerCtx = withNotifyEnvelopes(handlerCtx, signals)
			if handlerErr := l.config.Handler(handlerCtx, event); handlerErr != nil {
				if errors.Is(handlerErr, ErrNoOp) {
					noOp = true
				} else {
					err = fmt.Errorf("handler: %w", handlerErr)
				}
			} else {
				result = &IterationResult{
					ConvID:     convID,
					Supervisor: isSupervisor,
					Elapsed:    time.Since(iterStart),
				}
			}
			if model, ok := summary["model"].(string); ok && model != "" && result != nil {
				result.Model = model
				delete(summary, "model")
			}
			if inputTokens, ok := summary["input_tokens"].(int); ok && result != nil {
				result.InputTokens = inputTokens
				delete(summary, "input_tokens")
			}
			if outputTokens, ok := summary["output_tokens"].(int); ok && result != nil {
				result.OutputTokens = outputTokens
				delete(summary, "output_tokens")
			}
			if contextWindow, ok := summary["context_window"].(int); ok && contextWindow > 0 && result != nil {
				result.ContextWindow = contextWindow
				delete(summary, "context_window")
			}
			if toolsUsed, ok := summary["tools_used"].(map[string]int); ok && result != nil {
				result.ToolsUsed = cloneToolCounts(toolsUsed)
				delete(summary, "tools_used")
			}
			if activeTags, ok := summary["active_tags"].([]string); ok && result != nil {
				result.ActiveTags = append([]string(nil), activeTags...)
				delete(summary, "active_tags")
			}
			if effectiveTools, ok := summary["effective_tools"].([]string); ok && result != nil {
				result.EffectiveTools = append([]string(nil), effectiveTools...)
				delete(summary, "effective_tools")
			}
			if loadedCapabilities, ok := summary["loaded_capabilities"].([]toolcatalog.LoadedCapabilityEntry); ok && result != nil {
				result.LoadedCapabilities = append([]toolcatalog.LoadedCapabilityEntry(nil), loadedCapabilities...)
				delete(summary, "loaded_capabilities")
			}
			// Extract request_id from summary if the handler reported
			// one (e.g., signal/OWU handlers that call agent.Run).
			// Only remove from summary when successfully copied to
			// result; on error (result == nil) keep it in summary
			// so it's available in the error snapshot for debugging.
			if rid, ok := summary["request_id"].(string); ok && rid != "" && result != nil {
				result.RequestID = rid
				delete(summary, "request_id")
			}
			if reportedConvID, ok := summary["conversation_id"].(string); ok && reportedConvID != "" {
				convID = reportedConvID
				delete(summary, "conversation_id")
				if result != nil {
					result.ConvID = reportedConvID
				}
			}
			if len(summary) > 0 {
				handlerSummary = summary
			}
			// Commit bookkeeping only when the handler did real work.
			// No-op iterations (e.g. pollers with nothing new) skip
			// this to keep the dashboard activity indicator quiet.
			if !noOp {
				l.mu.Lock()
				l.lastWakeAt = iterStartTime
				l.attempts++
				l.recentConvIDs = append([]string{convID}, l.recentConvIDs...)
				if len(l.recentConvIDs) > recentConvIDsCap {
					l.recentConvIDs = l.recentConvIDs[:recentConvIDsCap]
				}
				l.mu.Unlock()
			}
		} else {
			iterStart := time.Now()
			turnCtx := withLoopID(iterCtx, l.id)
			turnCtx = withFallbackContent(turnCtx, l.config.FallbackContent)
			turnCtx = withNotifyEnvelopes(turnCtx, signals)
			turn, buildErr := l.buildAgentTurn(turnCtx, TurnInput{
				Event:           event,
				Supervisor:      isSupervisor,
				NotifyEnvelopes: signals,
			})
			if buildErr != nil {
				if errors.Is(buildErr, ErrNoOp) {
					noOp = true
				} else {
					err = fmt.Errorf("turn builder: %w", buildErr)
				}
			} else if turn == nil {
				noOp = true
			} else {
				var turnResp *Response
				var turnErr error
				req, prepareErr := l.prepareAgentTurnRequest(turn.Request, convID, isSupervisor)
				if prepareErr != nil {
					turnErr = prepareErr
					err = turnErr
				} else {
					if req.ConversationID != "" && req.ConversationID != convID {
						convID = req.ConversationID
						l.mu.Lock()
						l.currentConvID = convID
						l.mu.Unlock()
					}
					runCtx, runCancel := mergeTurnRunContext(iterCtx, turn.RunContext)
					result, turnResp, turnErr = l.runAgentTurn(runCtx, req, turn.Stream, iterStart, isSupervisor)
					runCancel()
					err = turnErr
					if len(turn.Summary) > 0 {
						handlerSummary = cloneSummaryMap(turn.Summary)
					}
				}
				if turn.ResultSink != nil {
					turn.ResultSink(turnResp, turnErr)
				}
			}
			if !noOp {
				l.mu.Lock()
				l.lastWakeAt = iterStartTime
				l.attempts++
				l.recentConvIDs = append([]string{convID}, l.recentConvIDs...)
				if len(l.recentConvIDs) > recentConvIDsCap {
					l.recentConvIDs = l.recentConvIDs[:recentConvIDsCap]
				}
				l.mu.Unlock()
			}
		}

		// Clear in-flight state after iteration completes.
		l.mu.Lock()
		l.currentConvID = ""
		l.llmContext = nil
		l.mu.Unlock()

		if noOp {
			iterLog.Debug("handler returned no-op, skipping iteration accounting")
			// Emit a zero-token iteration_complete so the dashboard
			// sees a balanced start→complete pair for every wake.
			l.publishEvent(events.Event{
				Timestamp: time.Now(),
				Source:    events.SourceLoop,
				Kind:      events.KindLoopIterationComplete,
				Data: map[string]any{
					"loop_id":         l.id,
					"loop_name":       l.config.Name,
					"conversation_id": convID,
					"no_op":           true,
					"elapsed_ms":      time.Since(iterStartTime).Milliseconds(),
				},
			})
		}

		// Compute sleep unconditionally so timer-driven loops still
		// pause between cycles even on no-op iterations.
		var sleep time.Duration
		if noOp {
			sleep = l.computeSleep()
		}
		if !noOp {
			// Build iteration snapshot for the dashboard timeline.
			var snap IterationSnapshot
			snap.ConvID = convID
			snap.StartedAt = iterStartTime
			snap.Supervisor = isSupervisor

			if err != nil {
				if ctx.Err() != nil {
					logger.Debug("loop stopped during processing",
						"phase", "processing",
					)
					break
				}
				iterLog.Warn("loop iteration failed",
					"error", err,
					"elapsed_ms", time.Since(iterStartTime).Milliseconds(),
				)
				l.mu.Lock()
				l.lastError = err.Error()
				l.consecutiveErrors++
				l.mu.Unlock()
				l.setState(StateError)

				snap.Error = err.Error()
				snap.CompletedAt = time.Now()
				snap.ElapsedMs = snap.CompletedAt.Sub(iterStartTime).Milliseconds()

				l.publishEvent(events.Event{
					Timestamp: time.Now(),
					Source:    events.SourceLoop,
					Kind:      events.KindLoopError,
					Data: map[string]any{
						"loop_id":   l.id,
						"loop_name": l.config.Name,
						"error":     err.Error(),
					},
				})
			} else {
				l.mu.Lock()
				l.iterations++
				l.totalInputTokens += result.InputTokens
				l.totalOutputTokens += result.OutputTokens
				l.lastInputTokens = result.InputTokens
				l.lastOutputTokens = result.OutputTokens
				if result.ContextWindow > 0 {
					l.contextWindow = result.ContextWindow
				}
				l.lastError = ""
				l.consecutiveErrors = 0
				if isSupervisor {
					l.lastSupervisorIter = l.iterations
				}
				snap.Number = l.iterations
				l.mu.Unlock()

				snap.Model = result.Model
				snap.RequestID = result.RequestID
				snap.InputTokens = result.InputTokens
				snap.OutputTokens = result.OutputTokens
				snap.ContextWindow = result.ContextWindow
				snap.ElapsedMs = result.Elapsed.Milliseconds()
				snap.CompletedAt = time.Now()

				// Deep copy ToolsUsed so the snapshot is independent
				// of any caller-held references.
				if len(result.ToolsUsed) > 0 {
					snap.ToolsUsed = make(map[string]int, len(result.ToolsUsed))
					for k, v := range result.ToolsUsed {
						snap.ToolsUsed[k] = v
					}
				}
				if len(result.EffectiveTools) > 0 {
					snap.EffectiveTools = append([]string(nil), result.EffectiveTools...)
				}
				if len(result.ActiveTags) > 0 {
					snap.ActiveTags = append([]string(nil), result.ActiveTags...)
				}
				snap.Tooling = BuildToolingState(
					mergeUniqueStrings(l.config.Tags, l.requestBase.InitialTags, l.requestOverride.InitialTags),
					result.ActiveTags,
					result.EffectiveTools,
					l.config.ExcludeTools,
					result.LoadedCapabilities,
					result.ToolsUsed,
				)

				iterLog.Debug("loop iteration complete",
					"model", result.Model,
					"input_tokens", result.InputTokens,
					"output_tokens", result.OutputTokens,
					"elapsed", result.Elapsed.Round(time.Second),
				)

				eventData := map[string]any{
					"loop_id":         l.id,
					"loop_name":       l.config.Name,
					"model":           result.Model,
					"request_id":      result.RequestID,
					"input_tokens":    result.InputTokens,
					"output_tokens":   result.OutputTokens,
					"context_window":  result.ContextWindow,
					"elapsed_ms":      result.Elapsed.Milliseconds(),
					"tools_used":      result.ToolsUsed,
					"effective_tools": result.EffectiveTools,
					"active_tags":     result.ActiveTags,
					"tooling":         snap.Tooling,
					"supervisor":      result.Supervisor,
					"conversation_id": convID,
				}
				if len(handlerSummary) > 0 {
					eventData["summary"] = handlerSummary
				}
				l.publishEvent(events.Event{
					Timestamp: time.Now(),
					Source:    events.SourceLoop,
					Kind:      events.KindLoopIterationComplete,
					Data:      eventData,
				})

			}

			// Compute sleep after updating error state so backoff reflects
			// the outcome of the just-completed iteration.
			sleep = l.computeSleep()

			// Record sleep/wait info on the snapshot before buffering.
			if l.config.WaitFunc != nil {
				snap.WaitAfter = true
			} else {
				snap.SleepAfterMs = sleep.Milliseconds()
			}

			// Attach handler summary if available.
			if len(handlerSummary) > 0 {
				snap.Summary = handlerSummary
			}

			// Prepend snapshot to ring buffer (newest first).
			l.mu.Lock()
			l.recentIterations = append([]IterationSnapshot{snap}, l.recentIterations...)
			if len(l.recentIterations) > recentIterationsCap {
				l.recentIterations = l.recentIterations[:recentIterationsCap]
			}
			l.mu.Unlock()

			// Call PostIterate on success if configured. Errors are logged
			// but do not count as iteration failures. Uses iterCtx for
			// per-iteration logger correlation.
			if err == nil && l.config.PostIterate != nil {
				postResult := IterationResult{
					ConvID:         convID,
					Model:          result.Model,
					InputTokens:    result.InputTokens,
					OutputTokens:   result.OutputTokens,
					ToolsUsed:      result.ToolsUsed,
					EffectiveTools: append([]string(nil), result.EffectiveTools...),
					ActiveTags:     append([]string(nil), result.ActiveTags...),
					Elapsed:        result.Elapsed,
					Supervisor:     result.Supervisor,
					Sleep:          sleep,
				}
				if postErr := l.config.PostIterate(iterCtx, postResult); postErr != nil {
					iterLog.Warn("PostIterate callback failed", "error", postErr)
				}
			}
		}

		// --- SLEEP PHASE (bottom of loop, timer-driven loops only) ---
		// WaitFunc loops skip sleep and flow back to the top to wait
		// for the next event.
		if l.config.WaitFunc == nil {
			l.setState(StateSleeping)
			l.publishEvent(events.Event{
				Timestamp: time.Now(),
				Source:    events.SourceLoop,
				Kind:      events.KindLoopSleepStart,
				Data: map[string]any{
					"loop_id":        l.id,
					"loop_name":      l.config.Name,
					"sleep_duration": sleep.String(),
				},
			})

			iterLog.Debug("loop sleeping", "duration", sleep.Round(time.Second))

			if !l.sleep(ctx, sleep) {
				logger.Debug("loop stopped during sleep",
					"phase", "sleep",
				)
				break
			}
		}
	}

	l.emitStopped()
}

// emitStopped transitions the loop to StateStopped and publishes a
// KindLoopStopped event. It is called from every exit path in run().
func (l *Loop) emitStopped() {
	l.mu.Lock()
	iterations := l.iterations
	attempts := l.attempts
	lastError := l.lastError
	consecutiveErrors := l.consecutiveErrors
	startedAt := l.startedAt
	handlerOnly := l.config.Handler != nil
	eventDriven := l.config.WaitFunc != nil
	l.mu.Unlock()

	logger := l.deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With(
		"subsystem", logging.SubsystemLoop,
		"loop_id", l.id,
		"loop_name", l.config.Name,
	)

	attrs := []any{
		"iterations", iterations,
		"attempts", attempts,
		"handler_only", handlerOnly,
		"event_driven", eventDriven,
	}
	if !startedAt.IsZero() {
		attrs = append(attrs, "uptime", time.Since(startedAt).Round(time.Second))
	}
	if lastError != "" {
		attrs = append(attrs, "last_error", lastError)
	}
	if consecutiveErrors > 0 {
		attrs = append(attrs, "consecutive_errors", consecutiveErrors)
	}
	logger.Info("loop stopped", attrs...)

	l.setState(StateStopped)
	l.publishEvent(events.Event{
		Timestamp: time.Now(),
		Source:    events.SourceLoop,
		Kind:      events.KindLoopStopped,
		Data: map[string]any{
			"loop_id":    l.id,
			"loop_name":  l.config.Name,
			"iterations": iterations,
			"attempts":   attempts,
		},
	})
}

// makeProgressFunc returns a callback that publishes in-flight
// progress events to the event bus with loop context (id, name).
// Returns nil when there is no event bus, which disables progress
// reporting in the runner.
func (l *Loop) makeProgressFunc() func(string, map[string]any) {
	if l.deps.EventBus == nil {
		return nil
	}
	return func(kind string, data map[string]any) {
		if data == nil {
			data = make(map[string]any)
		}
		data["loop_id"] = l.id
		data["loop_name"] = l.config.Name

		// Capture LLM context so late-connecting dashboard clients
		// see enrichment data in the initial snapshot.
		if kind == events.KindLoopLLMStart {
			ctx := make(map[string]any, len(data))
			for k, v := range data {
				// Skip loop infrastructure keys — they're not useful
				// for dashboard rendering.
				if k == "loop_id" || k == "loop_name" {
					continue
				}
				ctx[k] = v
			}
			l.mu.Lock()
			l.llmContext = ctx
			l.mu.Unlock()
		}

		l.deps.EventBus.Publish(events.Event{
			Timestamp: time.Now(),
			Source:    events.SourceLoop,
			Kind:      kind,
			Data:      data,
		})
	}
}

// buildAgentTurn chooses the loop's turn construction strategy. Custom
// TurnBuilder hooks get the wake first; otherwise Task and TaskBuilder
// are adapted into the same AgentTurn shape.
func (l *Loop) buildAgentTurn(ctx context.Context, input TurnInput) (*AgentTurn, error) {
	if l.config.TurnBuilder != nil {
		return l.config.TurnBuilder(ctx, input)
	}
	return l.buildTaskTurn(ctx, input)
}

// buildTaskTurn adapts Config.Task and Config.TaskBuilder into the common
// AgentTurn representation. Prompt-only concerns live here; request-level
// routing, tool, progress, and accounting concerns stay in
// prepareAgentTurnRequest and runAgentTurn.
func (l *Loop) buildTaskTurn(ctx context.Context, input TurnInput) (*AgentTurn, error) {
	var task string
	if l.config.TaskBuilder != nil {
		var buildErr error
		task, buildErr = l.config.TaskBuilder(ctx, input.Supervisor)
		if buildErr != nil {
			return nil, fmt.Errorf("TaskBuilder: %w", buildErr)
		}
	} else {
		task = l.config.Task
		if input.Supervisor && l.config.SupervisorContext != "" {
			task = l.config.SupervisorContext + "\n\n" + task
		}
	}
	if l.taskOverride != "" {
		task = l.taskOverride
	}

	if l.requestInstructions != "" {
		task = "Instructions: " + l.requestInstructions + "\n\n" + task
	}
	if len(l.config.Outputs) > 0 && l.config.OutputContextBuilder != nil {
		outputContext, err := l.config.OutputContextBuilder(ctx, l.config.Outputs)
		if err != nil {
			return nil, fmt.Errorf("output context: %w", err)
		}
		if outputContext != "" {
			task = outputContext + "\n\n" + task
		}
	}
	if signalSummary := summarizeNotifyEnvelopes(input.NotifyEnvelopes); signalSummary != "" {
		task = signalSummary + "\n\n" + task
	}

	return &AgentTurn{
		Request: Request{
			Messages: []Message{{Role: "user", Content: task}},
		},
	}, nil
}

// prepareAgentTurnRequest applies the loop request environment to a
// prepared turn request. This is the shared boundary where task turns,
// custom TurnBuilder turns, launch overrides, and carried capability
// state become the final runner request.
func (l *Loop) prepareAgentTurnRequest(req Request, convID string, isSupervisor bool) (Request, error) {
	hints := map[string]string{
		"source":    "loop",
		"loop_id":   l.id,
		"loop_name": l.config.Name,
	}
	if isSupervisor {
		hints["supervisor"] = "true"
		if l.config.SupervisorQualityFloor > 0 {
			hints["quality_floor"] = fmt.Sprintf("%d", l.config.SupervisorQualityFloor)
		}
		hints["local_only"] = "false"
	} else {
		if l.config.QualityFloor > 0 {
			hints["quality_floor"] = fmt.Sprintf("%d", l.config.QualityFloor)
		}
		hints["local_only"] = "true"
	}
	for k, v := range l.requestBase.Hints {
		hints[k] = v
	}
	for k, v := range l.config.Hints {
		hints[k] = v
	}
	for k, v := range req.Hints {
		hints[k] = v
	}
	for k, v := range l.requestOverride.Hints {
		hints[k] = v
	}

	configuredInitialTags := mergeUniqueStrings(l.config.Tags, l.requestBase.InitialTags, req.InitialTags, l.requestOverride.InitialTags)
	req.Model = firstNonEmpty(l.requestOverride.Model, req.Model, l.requestBase.Model)
	req.ConversationID = firstNonEmpty(l.requestOverride.ConversationID, req.ConversationID, convID)
	req.ChannelBinding = firstNonNilChannelBinding(l.requestOverride.ChannelBinding, req.ChannelBinding, l.requestBase.ChannelBinding)
	req.SkipContext = l.requestOverride.SkipContext || req.SkipContext
	allowedTools, err := applyAllowedToolsOverride(req.AllowedTools, l.requestOverride.AllowedTools)
	if err != nil {
		return Request{}, err
	}
	req.AllowedTools = allowedTools
	req.ExcludeTools = mergeUniqueStrings(l.requestBase.ExcludeTools, l.config.ExcludeTools, req.ExcludeTools, l.requestOverride.ExcludeTools)
	req.SkipTagFilter = len(configuredInitialTags) == 0 || req.SkipTagFilter || l.requestOverride.SkipTagFilter
	req.Hints = hints
	req.OnProgress = composeProgressFuncs(l.makeProgressFunc(), req.OnProgress, l.requestOverride.OnProgress)
	req.InitialTags = mergeUniqueStrings(configuredInitialTags, l.activatedTags)
	req.RuntimeTools = mergeRuntimeTools(l.config.RuntimeTools, req.RuntimeTools)
	req.FallbackContent = firstNonEmpty(l.requestOverride.FallbackContent, req.FallbackContent, l.requestBase.FallbackContent, l.config.FallbackContent)
	req.MaxIterations = firstPositiveInt(l.requestOverride.MaxIterations, req.MaxIterations)
	req.MaxOutputTokens = firstPositiveInt(l.requestOverride.MaxOutputTokens, req.MaxOutputTokens)
	req.ToolTimeout = firstPositiveDuration(l.requestOverride.ToolTimeout, req.ToolTimeout)
	req.UsageRole = firstNonEmpty(l.requestOverride.UsageRole, req.UsageRole)
	req.UsageTaskName = firstNonEmpty(l.requestOverride.UsageTaskName, req.UsageTaskName)
	req.SystemPrompt = firstNonEmpty(l.requestOverride.SystemPrompt, req.SystemPrompt)
	req.PromptMode = firstPromptMode(l.requestOverride.PromptMode, req.PromptMode)
	req.SuppressAlwaysContext = l.requestOverride.SuppressAlwaysContext || req.SuppressAlwaysContext
	return req, nil
}

// runAgentTurn is the only loop-owned path that invokes the agent runner.
// It captures runner response state needed by subsequent iterations and
// returns the typed iteration result used by snapshots and telemetry.
func (l *Loop) runAgentTurn(ctx context.Context, req Request, stream StreamCallback, iterStart time.Time, isSupervisor bool) (*IterationResult, *Response, error) {
	resp, err := l.deps.Runner.Run(ctx, req, stream)
	if err == nil && resp == nil {
		err = fmt.Errorf("runner returned nil response")
	}
	// Capture activated tags for next iteration (under lock since
	// activatedTags is read during Status snapshots). Always update,
	// even to nil — if all tags were dropped during a run, the next
	// iteration should not re-seed the old tags.
	if err == nil {
		l.mu.Lock()
		l.activatedTags = resp.ActiveTags
		l.lastResponse = cloneResponse(resp)
		l.mu.Unlock()
	}
	if err != nil {
		return nil, resp, fmt.Errorf("loop LLM call: %w", err)
	}

	return &IterationResult{
		ConvID:             req.ConversationID,
		Model:              resp.Model,
		InputTokens:        resp.InputTokens,
		OutputTokens:       resp.OutputTokens,
		ContextWindow:      resp.ContextWindow,
		ToolsUsed:          resp.ToolsUsed,
		EffectiveTools:     append([]string(nil), resp.EffectiveTools...),
		ActiveTags:         append([]string(nil), resp.ActiveTags...),
		LoadedCapabilities: append([]toolcatalog.LoadedCapabilityEntry(nil), resp.LoadedCapabilities...),
		RequestID:          resp.RequestID,
		Elapsed:            time.Since(iterStart),
		Supervisor:         isSupervisor,
	}, resp, nil
}

// mergeTurnRunContext combines the loop iteration context with an optional
// caller-owned request context. It lets request/reply adapters cancel the
// runner on client disconnect without making that request context own the
// child loop's lifetime.
func mergeTurnRunContext(base context.Context, extra context.Context) (context.Context, context.CancelFunc) {
	if extra == nil {
		return base, func() {}
	}
	ctx, cancel := context.WithCancel(base)
	if extra.Err() != nil {
		cancel()
		return ctx, cancel
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-extra.Done():
			cancel()
		}
	}()
	return ctx, cancel
}

func firstNonNilChannelBinding(bindings ...*memory.ChannelBinding) *memory.ChannelBinding {
	for _, binding := range bindings {
		if binding != nil {
			return binding.Clone()
		}
	}
	return nil
}

func mergeUniqueStrings(parts ...[]string) []string {
	var merged []string
	seen := make(map[string]struct{})
	for _, part := range parts {
		for _, item := range part {
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			merged = append(merged, item)
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func applyAllowedToolsOverride(base, override []string) ([]string, error) {
	base = mergeUniqueStrings(base)
	override = mergeUniqueStrings(override)
	if len(override) == 0 {
		return base, nil
	}
	if len(base) == 0 {
		return override, nil
	}

	baseSet := make(map[string]struct{}, len(base))
	for _, name := range base {
		baseSet[name] = struct{}{}
	}
	result := make([]string, 0, len(override))
	for _, name := range override {
		if _, ok := baseSet[name]; ok {
			result = append(result, name)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("agent turn allowed_tools override has no overlap with prepared request allowed_tools")
	}
	return result, nil
}

func composeProgressFuncs(callbacks ...func(kind string, data map[string]any)) func(kind string, data map[string]any) {
	var active []func(kind string, data map[string]any)
	for _, cb := range callbacks {
		if cb != nil {
			active = append(active, cb)
		}
	}
	if len(active) == 0 {
		return nil
	}
	return func(kind string, data map[string]any) {
		for _, cb := range active {
			cb(kind, data)
		}
	}
}

func cloneResponse(resp *Response) *Response {
	if resp == nil {
		return nil
	}
	out := *resp
	if len(resp.ToolsUsed) > 0 {
		out.ToolsUsed = make(map[string]int, len(resp.ToolsUsed))
		for k, v := range resp.ToolsUsed {
			out.ToolsUsed[k] = v
		}
	}
	if len(resp.EffectiveTools) > 0 {
		out.EffectiveTools = append([]string(nil), resp.EffectiveTools...)
	}
	if len(resp.ActiveTags) > 0 {
		out.ActiveTags = append([]string(nil), resp.ActiveTags...)
	}
	return &out
}

func (l *Loop) lastResponseSnapshot() *Response {
	l.mu.Lock()
	defer l.mu.Unlock()
	return cloneResponse(l.lastResponse)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstPositiveInt(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func firstPositiveDuration(values ...time.Duration) time.Duration {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func firstPromptMode(values ...agentctx.PromptMode) agentctx.PromptMode {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func mergeRuntimeTools(parts ...[]RuntimeTool) []RuntimeTool {
	var merged []RuntimeTool
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		merged = append(merged, cloneRuntimeTools(part)...)
	}
	return merged
}

func cloneSummaryMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// computeSleep returns the sleep duration for the next cycle. If
// SetNextSleep was called during the iteration, that value is used
// (clamped to bounds). Otherwise, SleepDefault is used. On
// consecutive errors, exponential backoff doubles the sleep each
// failure (capped at SleepMax). Jitter is applied to the final value.
func (l *Loop) computeSleep() time.Duration {
	l.mu.Lock()
	requested := l.nextSleep
	errCount := l.consecutiveErrors
	l.mu.Unlock()

	d := l.config.SleepDefault
	if requested > 0 {
		d = requested
	}

	// Exponential backoff on consecutive errors: double for each
	// failure, stopping early once we reach SleepMax to avoid
	// overflow wrapping negative.
	for range errCount {
		if d >= l.config.SleepMax {
			break
		}
		d *= 2
	}

	d = l.clamp(d)
	return l.applyJitter(d)
}

// clamp restricts d to the [SleepMin, SleepMax] range.
func (l *Loop) clamp(d time.Duration) time.Duration {
	if d < l.config.SleepMin {
		d = l.config.SleepMin
	}
	if d > l.config.SleepMax {
		d = l.config.SleepMax
	}
	return d
}

// applyJitter adds randomization to break periodicity. The actual sleep
// varies by +/-Jitter of the base duration. A nil or non-positive
// Jitter disables jitter entirely.
func (l *Loop) applyJitter(d time.Duration) time.Duration {
	if l.config.Jitter == nil || *l.config.Jitter <= 0 {
		return d
	}
	factor := 1.0 + *l.config.Jitter*(2*l.deps.Rand.Float64()-1)
	result := time.Duration(float64(d) * factor)
	return l.clamp(result)
}

// setState updates the loop's state under lock. The state-change event
// is published outside the lock to avoid deadlocking if the event bus
// blocks. A monotonic sequence number (event_seq) is included so
// consumers can reorder events that arrive out of sequence.
func (l *Loop) setState(s State) {
	l.mu.Lock()
	prev := l.state
	l.state = s
	seq := l.eventSeq.Add(1)
	l.mu.Unlock()

	if prev != s {
		l.publishEvent(events.Event{
			Timestamp: time.Now(),
			Source:    events.SourceLoop,
			Kind:      events.KindLoopStateChange,
			Data: map[string]any{
				"loop_id":   l.id,
				"loop_name": l.config.Name,
				"from":      string(prev),
				"to":        string(s),
				"event_seq": seq,
			},
		})
	}
}

// publishEvent sends an event to the event bus if configured.
func (l *Loop) publishEvent(e events.Event) {
	if l.deps.EventBus != nil {
		l.deps.EventBus.Publish(e)
	}
}
