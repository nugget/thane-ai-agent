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
	"github.com/nugget/thane-ai-agent/internal/events"
	"github.com/nugget/thane-ai-agent/internal/logging"
)

// Runner abstracts the agent loop for LLM calls. Satisfied by
// *agent.Loop. Defined here to avoid a circular import.
type Runner interface {
	Run(ctx context.Context, req RunRequest, stream StreamCallback) (*RunResponse, error)
}

// RunRequest mirrors the fields of agent.Request that loops need.
// The loop package defines its own type to avoid importing agent.
type RunRequest struct {
	ConversationID string
	Messages       []RunMessage
	ExcludeTools   []string
	SkipTagFilter  bool
	Hints          map[string]string
	// SeedTags are capability tags to activate at the start of the Run,
	// in addition to always-active and channel-pinned tags. Used by loops
	// to carry forward tags activated in previous iterations.
	SeedTags []string

	// OnProgress is called by the Runner during execution to report
	// in-flight activity (tool calls, LLM responses). The kind
	// parameter maps to an [events.Kind] constant; data holds
	// event-specific fields. The loop automatically injects loop_id
	// and loop_name into data before publishing. Nil means no
	// progress reporting.
	OnProgress func(kind string, data map[string]any) `json:"-"`
}

// RunMessage is a chat message for the runner.
type RunMessage struct {
	Role    string
	Content string
}

// RunResponse mirrors agent.Response fields that loops consume.
// RunResponse holds the result of an LLM call executed by a [Runner].
type RunResponse struct {
	Content       string
	Model         string
	InputTokens   int
	OutputTokens  int
	ContextWindow int
	ToolsUsed     map[string]int
	RequestID     string
	// ActiveTags is the set of capability tags that were active at the
	// end of the Run. Loops use this to carry forward activations to
	// subsequent iterations.
	ActiveTags []string
}

// StreamCallback receives streaming events. Nil disables streaming.
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
// via the agent runner, or a direct [Config.Handler] function for
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

	// activatedTags tracks capability tags activated during previous
	// iterations. Carried forward via SeedTags on the next RunRequest
	// so activations persist across the loop's lifetime.
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
	}, nil
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
	cfgCopy.PostIterate = nil
	cfgCopy.WaitFunc = nil
	cfgCopy.Handler = nil
	cfgCopy.Setup = nil
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
	if atFunc != nil {
		if tags := atFunc(); len(tags) > 0 {
			cp := make([]string, len(tags))
			copy(cp, tags)
			s.ActiveTags = cp
		}
	}

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

		logger.Info("loop initial sleep before first iteration",
			"duration", initialSleep.Round(time.Second),
		)

		if !sleepCtx(ctx, initialSleep) {
			logger.Info("loop stopped during initial sleep")
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
			event, waitErr = l.config.WaitFunc(ctx)
			if waitErr != nil {
				if ctx.Err() != nil {
					logger.Info("loop stopped")
					break
				}
				// WaitFunc error (not cancellation) — apply backoff
				// before retrying. This prevents tight-looping when
				// the upstream source is temporarily broken.
				logger.Warn("wait failed", "error", waitErr)
				l.mu.Lock()
				l.lastError = waitErr.Error()
				l.consecutiveErrors++
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
				if !sleepCtx(ctx, backoff) {
					logger.Info("loop stopped during wait backoff")
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

		// --- PROCESSING PHASE ---
		// Reset tool-provided sleep override and set current conversation ID.
		convID := fmt.Sprintf("loop-%s-%d-%d", l.config.Name, attemptCount+1, time.Now().UnixMilli())
		l.mu.Lock()
		l.nextSleep = 0
		l.currentConvID = convID
		l.mu.Unlock()

		// Determine if this is a supervisor iteration.
		isSupervisor := l.config.Supervisor && l.config.SupervisorProb > 0 && l.deps.Rand.Float64() < l.config.SupervisorProb

		iterLog := logger.With(
			"conversation_id", convID,
			"supervisor", isSupervisor,
			"attempt", attemptCount+1,
		)
		iterCtx := logging.WithLogger(ctx, iterLog)

		iterStartTime := time.Now()

		// Dispatch: Handler runs directly; otherwise use LLM via iterate().
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
				"loop_id":         l.id,
				"loop_name":       l.config.Name,
				"conversation_id": convID,
				"supervisor":      isSupervisor,
				"attempt":         attemptCount + 1,
			},
		})
		iterLog.Debug("loop iteration starting")

		if l.config.Handler != nil {
			iterStart := time.Now()
			summary := make(map[string]any)
			handlerCtx := context.WithValue(iterCtx, iterSummaryKey{}, summary)
			handlerCtx = context.WithValue(handlerCtx, progressFuncKey{}, l.makeProgressFunc())
			handlerCtx = withLoopID(handlerCtx, l.id)
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
			// Extract request_id from summary if the handler reported
			// one (e.g., signal/OWU handlers that call agent.Run).
			// Only remove from summary when successfully copied to
			// result; on error (result == nil) keep it in summary
			// so it's available in the error snapshot for debugging.
			if rid, ok := summary["request_id"].(string); ok && rid != "" && result != nil {
				result.RequestID = rid
				delete(summary, "request_id")
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
			// LLM-based loops always do meaningful work.
			l.mu.Lock()
			l.lastWakeAt = iterStartTime
			l.attempts++
			l.recentConvIDs = append([]string{convID}, l.recentConvIDs...)
			if len(l.recentConvIDs) > recentConvIDsCap {
				l.recentConvIDs = l.recentConvIDs[:recentConvIDsCap]
			}
			l.mu.Unlock()
			result, err = l.iterate(iterCtx, isSupervisor, convID)
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
					logger.Info("loop stopped")
					break
				}
				iterLog.Warn("loop iteration failed", "error", err)
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
					ConvID:       convID,
					Model:        result.Model,
					InputTokens:  result.InputTokens,
					OutputTokens: result.OutputTokens,
					ToolsUsed:    result.ToolsUsed,
					Elapsed:      result.Elapsed,
					Supervisor:   result.Supervisor,
					Sleep:        sleep,
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

			iterLog.Info("loop sleeping", "duration", sleep.Round(time.Second))

			if !sleepCtx(ctx, sleep) {
				logger.Info("loop stopped")
				break
			}
		}
	}

	l.emitStopped()
}

// emitStopped transitions the loop to StateStopped and publishes a
// KindLoopStopped event. It is called from every exit path in run().
func (l *Loop) emitStopped() {
	l.setState(StateStopped)
	l.publishEvent(events.Event{
		Timestamp: time.Now(),
		Source:    events.SourceLoop,
		Kind:      events.KindLoopStopped,
		Data: map[string]any{
			"loop_id":    l.id,
			"loop_name":  l.config.Name,
			"iterations": l.iterations,
			"attempts":   l.attempts,
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

// iterate performs a single loop iteration: build prompt and run the
// LLM via the agent runner.
func (l *Loop) iterate(ctx context.Context, isSupervisor bool, convID string) (*IterationResult, error) {
	iterStart := time.Now()

	// Build routing hints.
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

	// Build the task prompt. TaskBuilder takes priority over static Task.
	var task string
	if l.config.TaskBuilder != nil {
		var buildErr error
		task, buildErr = l.config.TaskBuilder(ctx, isSupervisor)
		if buildErr != nil {
			return nil, fmt.Errorf("TaskBuilder: %w", buildErr)
		}
	} else {
		task = l.config.Task
		if isSupervisor && l.config.SupervisorContext != "" {
			task = l.config.SupervisorContext + "\n\n" + task
		}
	}

	// Merge config hints over loop-generated defaults.
	for k, v := range l.config.Hints {
		hints[k] = v
	}

	req := RunRequest{
		ConversationID: convID,
		Messages: []RunMessage{
			{Role: "user", Content: task},
		},
		ExcludeTools:  l.config.ExcludeTools,
		SkipTagFilter: len(l.config.Tags) == 0,
		Hints:         hints,
		OnProgress:    l.makeProgressFunc(),
		SeedTags:      l.activatedTags, // carry forward from previous iterations
	}

	resp, err := l.deps.Runner.Run(ctx, req, nil)
	// Capture activated tags for next iteration (under lock since
	// activatedTags is read during Status snapshots). Always update,
	// even to nil — if all tags were dropped during a run, the next
	// iteration should not re-seed the old tags.
	if err == nil {
		l.mu.Lock()
		l.activatedTags = resp.ActiveTags
		l.mu.Unlock()
	}
	if err != nil {
		return nil, fmt.Errorf("loop LLM call: %w", err)
	}

	return &IterationResult{
		Model:         resp.Model,
		InputTokens:   resp.InputTokens,
		OutputTokens:  resp.OutputTokens,
		ContextWindow: resp.ContextWindow,
		ToolsUsed:     resp.ToolsUsed,
		RequestID:     resp.RequestID,
		Elapsed:       time.Since(iterStart),
		Supervisor:    isSupervisor,
	}, nil
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

// sleepCtx sleeps for d or until ctx is cancelled. Returns false if
// cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
