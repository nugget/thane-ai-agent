package loop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
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
	cancel    context.CancelFunc
	done      chan struct{}
	startedAt time.Time

	// Metrics updated during execution.
	lastWakeAt        time.Time
	iterations        int // successful iterations
	attempts          int // total attempts (including failures)
	totalInputTokens  int
	totalOutputTokens int
	lastError         string

	// nextSleep can be set externally (e.g., by a set_next_sleep
	// tool handler) to override the default sleep for one cycle.
	nextSleep time.Duration
}

// New creates a loop with the given configuration and dependencies.
// Returns an error if required dependencies are missing (e.g., Runner).
// Call [Loop.Start] to launch the background goroutine.
func New(cfg Config, deps Deps) (*Loop, error) {
	if deps.Runner == nil {
		return nil, ErrNilRunner
	}

	cfg.applyDefaults()

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

// Start launches the background goroutine. Calling Start on an already
// running loop is a no-op (returns nil). The goroutine runs until ctx is
// cancelled or [Loop.Stop] is called.
func (l *Loop) Start(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()

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
// call multiple times or before Start. Blocks until the goroutine exits
// or 10 seconds elapse.
func (l *Loop) Stop() {
	l.mu.Lock()
	cancel := l.cancel
	done := l.done
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

// Done returns a channel that is closed when the loop's goroutine exits.
// Returns nil if the loop has not been started.
func (l *Loop) Done() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.done
}

// Status returns a snapshot of the loop's current state and metrics.
func (l *Loop) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
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
		Config:            l.config,
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

		// Reset tool-provided sleep override.
		l.mu.Lock()
		l.nextSleep = 0
		l.mu.Unlock()

		// Determine if this is a supervisor iteration.
		isSupervisor := l.config.Supervisor && l.config.SupervisorProb > 0 && l.deps.Rand.Float64() < l.config.SupervisorProb

		// Generate conversation ID for this iteration.
		convID := fmt.Sprintf("loop-%s-%d", l.config.Name, time.Now().UnixMilli())

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
		if err != nil {
			if ctx.Err() != nil {
				logger.Info("loop stopped")
				break
			}
			iterLog.Warn("loop iteration failed", "error", err)
			l.mu.Lock()
			l.lastError = err.Error()
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
		}

		// Compute sleep duration.
		sleep := l.computeSleep()

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

// iterationResult holds data returned from a single loop iteration.
type iterationResult struct {
	Model        string
	InputTokens  int
	OutputTokens int
	ToolsUsed    map[string]int
	Elapsed      time.Duration
	Supervisor   bool
}

// iterate performs a single loop iteration: build prompt and run the
// LLM via the agent runner.
func (l *Loop) iterate(ctx context.Context, isSupervisor bool, convID string) (*iterationResult, error) {
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

	req := RunRequest{
		ConversationID: convID,
		Messages: []RunMessage{
			{Role: "user", Content: l.config.Task},
		},
		ExcludeTools:  l.config.ExcludeTools,
		SkipTagFilter: len(l.config.Tags) == 0,
		Hints:         hints,
	}

	resp, err := l.deps.Runner.Run(ctx, req, nil)
	if err != nil {
		return nil, fmt.Errorf("loop LLM call: %w", err)
	}

	return &iterationResult{
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
// (clamped to bounds). Otherwise, SleepDefault is used. Jitter is
// applied to the final value.
func (l *Loop) computeSleep() time.Duration {
	l.mu.Lock()
	requested := l.nextSleep
	l.mu.Unlock()

	d := l.config.SleepDefault
	if requested > 0 {
		d = requested
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

// setState updates the loop's state under lock.
func (l *Loop) setState(s State) {
	l.mu.Lock()
	prev := l.state
	l.state = s
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
