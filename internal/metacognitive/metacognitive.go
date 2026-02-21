// Package metacognitive implements a perpetual self-regulating attention
// loop that reads persistent state, reasons via LLM, and adapts its own
// sleep cycle. See issue #319.
//
// Each iteration is a fresh conversation. State persists across iterations
// via a markdown file (metacognitive.md by default). The loop's cost is
// self-limiting: quiet periods produce long sleeps and few iterations.
//
// "Dice" randomly select a frontier model for supervisor iterations that
// review the loop's own behavior, catching blind spots that the cheaper
// local model's consistent reasoning patterns miss.
package metacognitive

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/prompts"
	"github.com/nugget/thane-ai-agent/internal/router"
)

// maxStateBytes is the maximum metacognitive.md content read per
// iteration. Content beyond this limit is truncated with a marker.
const maxStateBytes = 16 * 1024

// agentRunner abstracts the agent loop for LLM calls. Satisfied by
// *agent.Loop. Matches the interface in cmd/thane/taskexec.go.
type agentRunner interface {
	Run(ctx context.Context, req *agent.Request, stream agent.StreamCallback) (*agent.Response, error)
}

// RandSource abstracts randomness for deterministic testing.
type RandSource interface {
	// Float64 returns a pseudo-random float64 in the half-open interval [0.0, 1.0).
	Float64() float64
}

// defaultRand uses math/rand/v2's global source.
type defaultRand struct{}

func (defaultRand) Float64() float64 { return rand.Float64() }

// Config holds the parsed metacognitive loop configuration with
// time.Duration fields (as opposed to the YAML string representation
// in [config.MetacognitiveConfig]).
type Config struct {
	Enabled                bool
	StateFile              string // relative to workspace
	MinSleep               time.Duration
	MaxSleep               time.Duration
	DefaultSleep           time.Duration
	Jitter                 float64 // 0.0–1.0
	SupervisorProbability  float64 // 0.0–1.0
	QualityFloor           int     // normal iterations
	SupervisorQualityFloor int     // supervisor iterations
}

// ParseConfig converts a [config.MetacognitiveConfig] (string durations)
// into a [Config] (time.Duration fields). Call after config validation
// has passed.
func ParseConfig(raw config.MetacognitiveConfig) (Config, error) {
	minSleep, err := time.ParseDuration(raw.MinSleep)
	if err != nil {
		return Config{}, fmt.Errorf("min_sleep %q: %w", raw.MinSleep, err)
	}
	maxSleep, err := time.ParseDuration(raw.MaxSleep)
	if err != nil {
		return Config{}, fmt.Errorf("max_sleep %q: %w", raw.MaxSleep, err)
	}
	defaultSleep, err := time.ParseDuration(raw.DefaultSleep)
	if err != nil {
		return Config{}, fmt.Errorf("default_sleep %q: %w", raw.DefaultSleep, err)
	}
	return Config{
		Enabled:                raw.Enabled,
		StateFile:              raw.StateFile,
		MinSleep:               minSleep,
		MaxSleep:               maxSleep,
		DefaultSleep:           defaultSleep,
		Jitter:                 raw.Jitter,
		SupervisorProbability:  raw.SupervisorProbability,
		QualityFloor:           raw.Router.QualityFloor,
		SupervisorQualityFloor: raw.SupervisorRouter.QualityFloor,
	}, nil
}

// Deps holds injected dependencies for the metacognitive loop. Using a
// struct avoids a growing parameter list as the loop evolves.
type Deps struct {
	Runner        agentRunner
	Logger        *slog.Logger
	WorkspacePath string
	RandSource    RandSource // nil uses math/rand/v2 default
}

// Loop is the perpetual metacognitive attention loop. Create with [New],
// start with [Start], stop with [Stop].
type Loop struct {
	config Config
	deps   Deps

	mu        sync.Mutex
	started   bool
	cancel    context.CancelFunc
	done      chan struct{}
	nextSleep time.Duration // set by set_next_sleep tool handler
}

// New creates a metacognitive loop. Call [Loop.Start] to launch the
// background goroutine.
func New(cfg Config, deps Deps) *Loop {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.RandSource == nil {
		deps.RandSource = defaultRand{}
	}
	return &Loop{
		config: cfg,
		deps:   deps,
	}
}

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

	loopCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	l.done = make(chan struct{})

	go l.run(loopCtx)
	return nil
}

// Stop cancels the loop and waits for the goroutine to exit. Safe to
// call multiple times or before Start.
func (l *Loop) Stop() {
	l.mu.Lock()
	cancel := l.cancel
	done := l.done
	l.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// run is the main goroutine body. It iterates perpetually until the
// context is cancelled.
func (l *Loop) run(ctx context.Context) {
	defer close(l.done)

	logger := l.deps.Logger
	logger.Info("metacognitive loop started",
		"min_sleep", l.config.MinSleep,
		"max_sleep", l.config.MaxSleep,
		"default_sleep", l.config.DefaultSleep,
		"jitter", l.config.Jitter,
		"supervisor_probability", l.config.SupervisorProbability,
	)

	for {
		// Reset the tool-provided sleep for this iteration.
		l.resetNextSleep()

		// Roll dice for model selection.
		isSupervisor := l.rollDice()

		iterStart := time.Now()
		if err := l.iterate(ctx, isSupervisor); err != nil {
			if ctx.Err() != nil {
				logger.Info("metacognitive loop stopped")
				return
			}
			logger.Warn("metacognitive iteration failed",
				"error", err,
				"supervisor", isSupervisor,
			)
		} else {
			logger.Info("metacognitive iteration complete",
				"supervisor", isSupervisor,
				"elapsed", time.Since(iterStart).Round(time.Millisecond),
			)
		}

		// Compute and apply sleep.
		sleep := l.computeSleep()
		logger.Info("metacognitive sleeping",
			"duration", sleep,
			"supervisor", isSupervisor,
		)

		if !sleepCtx(ctx, sleep) {
			logger.Info("metacognitive loop stopped")
			return
		}
	}
}

// iterate performs a single metacognitive iteration: read state, build
// prompt, and run the LLM via the agent runner.
func (l *Loop) iterate(ctx context.Context, isSupervisor bool) error {
	stateContent, err := l.readStateFile()
	if err != nil {
		l.deps.Logger.Info("metacognitive state file not found, starting fresh",
			"path", l.stateFilePath(),
		)
		stateContent = ""
	}

	promptText := prompts.MetacognitivePrompt(stateContent, isSupervisor)

	// Build routing hints.
	qualityFloor := l.config.QualityFloor
	localOnly := "true"
	if isSupervisor {
		qualityFloor = l.config.SupervisorQualityFloor
		localOnly = "false"
	}

	req := &agent.Request{
		ConversationID: fmt.Sprintf("metacog-%d", time.Now().UnixMilli()),
		Messages:       []agent.Message{{Role: "user", Content: promptText}},
		Hints: map[string]string{
			"source":                    "metacognitive",
			"supervisor":                strconv.FormatBool(isSupervisor),
			router.HintLocalOnly:        localOnly,
			router.HintQualityFloor:     strconv.Itoa(qualityFloor),
			router.HintMission:          "metacognitive",
			router.HintDelegationGating: "disabled",
		},
	}

	resp, err := l.deps.Runner.Run(ctx, req, nil)
	if err != nil {
		return fmt.Errorf("metacognitive LLM call: %w", err)
	}

	l.deps.Logger.Debug("metacognitive iteration result",
		"result_len", len(resp.Content),
		"model", resp.Model,
		"supervisor", isSupervisor,
	)

	return nil
}

// rollDice determines whether this iteration uses a frontier supervisor
// model. Returns true with probability [Config.SupervisorProbability].
func (l *Loop) rollDice() bool {
	if l.config.SupervisorProbability <= 0 {
		return false
	}
	if l.config.SupervisorProbability >= 1.0 {
		return true
	}
	return l.deps.RandSource.Float64() < l.config.SupervisorProbability
}

// computeSleep returns the sleep duration for the next cycle. If
// set_next_sleep was called during the iteration, that value is used
// (clamped to min/max). Otherwise, DefaultSleep is used. Jitter is
// applied to the final value.
func (l *Loop) computeSleep() time.Duration {
	l.mu.Lock()
	requested := l.nextSleep
	l.mu.Unlock()

	d := l.config.DefaultSleep
	if requested > 0 {
		d = requested
	}

	// Clamp to bounds.
	d = l.clamp(d)

	// Apply jitter.
	return l.applyJitter(d)
}

// clamp restricts d to the [MinSleep, MaxSleep] range.
func (l *Loop) clamp(d time.Duration) time.Duration {
	if d < l.config.MinSleep {
		d = l.config.MinSleep
	}
	if d > l.config.MaxSleep {
		d = l.config.MaxSleep
	}
	return d
}

// applyJitter adds randomization to break periodicity. The actual sleep
// varies by ±Jitter of the base duration.
func (l *Loop) applyJitter(d time.Duration) time.Duration {
	if l.config.Jitter <= 0 {
		return d
	}
	// Random value in [-jitter, +jitter].
	factor := 1.0 + l.config.Jitter*(2*l.deps.RandSource.Float64()-1)
	result := time.Duration(float64(d) * factor)
	// Re-clamp after jitter to stay within bounds.
	return l.clamp(result)
}

// setNextSleep is called by the set_next_sleep tool handler to
// communicate the LLM's chosen sleep duration back to the loop.
func (l *Loop) setNextSleep(d time.Duration) {
	l.mu.Lock()
	l.nextSleep = d
	l.mu.Unlock()
}

// resetNextSleep clears the tool-provided sleep before each iteration.
func (l *Loop) resetNextSleep() {
	l.mu.Lock()
	l.nextSleep = 0
	l.mu.Unlock()
}

// readStateFile reads the metacognitive state file from the workspace.
// Returns an error if the file does not exist (first iteration).
// Content is capped at [maxStateBytes].
func (l *Loop) readStateFile() (string, error) {
	data, err := os.ReadFile(l.stateFilePath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		return "", fmt.Errorf("read state file: %w", err)
	}
	if len(data) > maxStateBytes {
		return string(data[:maxStateBytes]) + "\n\n[metacognitive.md truncated — exceeded 16 KB limit]", nil
	}
	return string(data), nil
}

// stateFilePath returns the absolute path to the state file.
func (l *Loop) stateFilePath() string {
	return filepath.Join(l.deps.WorkspacePath, l.config.StateFile)
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
