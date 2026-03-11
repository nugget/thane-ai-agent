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
}

// RunMessage is a chat message for the runner.
type RunMessage struct {
	Role    string
	Content string
}

// RunResponse mirrors agent.Response fields that loops consume.
type RunResponse struct {
	Content      string
	Model        string
	InputTokens  int
	OutputTokens int
	ToolsUsed    map[string]int
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

// Deps holds injected dependencies for a loop. Using a struct avoids a
// growing parameter list as loops evolve.
type Deps struct {
	// Runner executes LLM iterations. Required.
	Runner Runner
	// Logger for loop operations. Defaults to slog.Default().
	Logger *slog.Logger
	// EventBus publishes loop lifecycle events. Nil disables events.
	EventBus *events.Bus
	// Rand provides randomness for sleep jitter and supervisor dice.
	// Nil uses math/rand/v2 default.
	Rand RandSource
}

// Loop is a persistent background goroutine that iterates on a
// randomized sleep schedule, running LLM iterations via the agent
// runner. Create with [New], start with [Start], stop with [Stop].
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
	lastError         string

	// currentConvID is the conversation ID of the in-flight iteration.
	// Set at the start of each iteration, cleared after. Tool handlers
	// read it via [Loop.CurrentConvID].
	currentConvID string

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
	if deps.Runner == nil {
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
	defer l.mu.Unlock()

	// Deep copy Config to prevent callers from mutating internal state
	// via shared slices/maps. Function fields are cleared — they can't
	// be serialized and shouldn't leak to callers.
	cfgCopy := l.config
	cfgCopy.TaskBuilder = nil
	cfgCopy.PostIterate = nil
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

	return Status{
		ID:                l.id,
		Name:              l.config.Name,
		State:             l.state,
		ParentID:          l.config.ParentID,
		StartedAt:         l.startedAt,
		LastWakeAt:        l.lastWakeAt,
		Iterations:        l.iterations,
		Attempts:          l.attempts,
		TotalInputTokens:  l.totalInputTokens,
		TotalOutputTokens: l.totalOutputTokens,
		LastError:         l.lastError,
		Config:            cfgCopy,
	}
}

// SetNextSleep sets the sleep duration for the next cycle. This is
// intended for tool handlers (e.g., set_next_sleep) to communicate the
// LLM's chosen sleep duration back to the loop.
func (l *Loop) SetNextSleep(d time.Duration) {
	l.mu.Lock()
	l.nextSleep = d
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

	l.setState(StateSleeping)
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
	)

	// Apply max duration as a context deadline if configured.
	if l.config.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, l.config.MaxDuration)
		defer cancel()
	}

	for {
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

		l.setState(StateProcessing)
		l.mu.Lock()
		l.lastWakeAt = time.Now()
		l.attempts++
		l.mu.Unlock()

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

		iterLog.Info("loop iteration starting")

		result, err := l.iterate(iterCtx, isSupervisor, convID)

		// Clear current conversation ID after iteration completes.
		l.mu.Lock()
		l.currentConvID = ""
		l.mu.Unlock()

		// Compute sleep duration (uses tool-provided override or default + backoff).
		sleep := l.computeSleep()

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
			l.lastError = ""
			l.consecutiveErrors = 0
			l.mu.Unlock()

			iterLog.Info("loop iteration complete",
				"model", result.Model,
				"input_tokens", result.InputTokens,
				"output_tokens", result.OutputTokens,
				"elapsed", result.Elapsed.Round(time.Second),
			)

			l.publishEvent(events.Event{
				Timestamp: time.Now(),
				Source:    events.SourceLoop,
				Kind:      events.KindLoopIterationComplete,
				Data: map[string]any{
					"loop_id":       l.id,
					"loop_name":     l.config.Name,
					"model":         result.Model,
					"input_tokens":  result.InputTokens,
					"output_tokens": result.OutputTokens,
					"elapsed_ms":    result.Elapsed.Milliseconds(),
				},
			})

			// Call PostIterate if configured. Errors are logged
			// but do not count as iteration failures.
			if l.config.PostIterate != nil {
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
				if postErr := l.config.PostIterate(ctx, postResult); postErr != nil {
					iterLog.Warn("PostIterate callback failed", "error", postErr)
				}
			}
		}

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
	}

	resp, err := l.deps.Runner.Run(ctx, req, nil)
	if err != nil {
		return nil, fmt.Errorf("loop LLM call: %w", err)
	}

	return &IterationResult{
		Model:        resp.Model,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		ToolsUsed:    resp.ToolsUsed,
		Elapsed:      time.Since(iterStart),
		Supervisor:   isSupervisor,
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
