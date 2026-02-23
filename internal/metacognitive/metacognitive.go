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
	"sort"
	"strconv"
	"strings"
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

// iterationLogRetention is the number of iteration log blocks to keep
// when pruning. Oldest beyond this limit are removed.
const iterationLogRetention = 5

// iterationLogPrefix is the HTML comment prefix used for iteration log
// blocks. Used for scanning/pruning.
const iterationLogPrefix = "<!-- iteration_log:"

// iterationResult holds data returned from a single metacognitive
// iteration, used to build the auto-appended summary log.
type iterationResult struct {
	Model        string
	InputTokens  int
	OutputTokens int
	ToolsUsed    map[string]int // tool name → call count
	Elapsed      time.Duration
	Supervisor   bool
}

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

	mu            sync.Mutex
	started       bool
	cancel        context.CancelFunc
	done          chan struct{}
	nextSleep     time.Duration // set by set_next_sleep tool handler
	currentConvID string        // set before each iterate(), read by tool handlers
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

		// Generate a conversation ID for this iteration.
		convID := fmt.Sprintf("metacog-%d", time.Now().UnixMilli())
		l.setCurrentConvID(convID)

		logger.Info("metacognitive iteration starting",
			"conversation_id", convID,
			"supervisor", isSupervisor,
		)

		result, err := l.iterate(ctx, isSupervisor, convID)
		if err != nil {
			if ctx.Err() != nil {
				logger.Info("metacognitive loop stopped")
				return
			}
			logger.Warn("metacognitive iteration failed",
				"error", err,
				"conversation_id", convID,
				"supervisor", isSupervisor,
			)
		} else {
			logger.Info("metacognitive iteration complete",
				"conversation_id", convID,
				"supervisor", isSupervisor,
				"elapsed", result.Elapsed.Round(time.Millisecond),
			)
		}

		// Compute and apply sleep.
		sleep := l.computeSleep()

		// Auto-append iteration summary on success.
		if result != nil {
			l.appendIterationLog(result, convID, sleep)
		}

		logger.Info("metacognitive sleeping",
			"duration", sleep,
			"conversation_id", convID,
			"supervisor", isSupervisor,
		)

		if !sleepCtx(ctx, sleep) {
			logger.Info("metacognitive loop stopped")
			return
		}
	}
}

// metacogExcludeTools lists tools that the metacognitive loop should not
// have access to. File tools are replaced by update_metacognitive_state,
// exec is unnecessary and dangerous, session management is for interactive
// use only.
var metacogExcludeTools = []string{
	"file_read", "file_write", "file_edit", "file_list",
	"file_search", "file_grep", "file_stat", "file_tree",
	"exec",
	"conversation_reset", "session_close", "session_split", "session_checkpoint",
	"create_temp_file",
	"request_capability", "drop_capability",
}

// iterate performs a single metacognitive iteration: read state, build
// prompt, and run the LLM via the agent runner. Returns an
// [iterationResult] with model, token, and tool data for the
// auto-appended summary log.
func (l *Loop) iterate(ctx context.Context, isSupervisor bool, convID string) (*iterationResult, error) {
	iterStart := time.Now()

	stateContent, err := l.readStateFile()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			l.deps.Logger.Info("metacognitive state file not found, starting fresh",
				"conversation_id", convID,
				"path", l.stateFilePath(),
			)
		} else {
			l.deps.Logger.Warn("metacognitive state file read failed, starting with empty state",
				"error", err,
				"conversation_id", convID,
				"path", l.stateFilePath(),
			)
		}
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
		ConversationID: convID,
		Messages:       []agent.Message{{Role: "user", Content: promptText}},
		ExcludeTools:   metacogExcludeTools,
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
		return nil, fmt.Errorf("metacognitive LLM call: %w", err)
	}

	l.deps.Logger.Debug("metacognitive iteration result",
		"result_len", len(resp.Content),
		"model", resp.Model,
		"conversation_id", convID,
		"supervisor", isSupervisor,
	)

	return &iterationResult{
		Model:        resp.Model,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		ToolsUsed:    resp.ToolsUsed,
		Elapsed:      time.Since(iterStart),
		Supervisor:   isSupervisor,
	}, nil
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

// setCurrentConvID stores the conversation ID for the current iteration
// so tool handlers can include it in metadata.
func (l *Loop) setCurrentConvID(id string) {
	l.mu.Lock()
	l.currentConvID = id
	l.mu.Unlock()
}

// getCurrentConvID returns the conversation ID for the current iteration.
func (l *Loop) getCurrentConvID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.currentConvID
}

// appendIterationLog appends an HTML comment summary block to the state
// file after a successful iteration. Old logs beyond
// [iterationLogRetention] are pruned. This is best-effort: errors are
// logged but never disrupt the loop.
//
// Unlike [Loop.readStateFile], this reads the full file without the
// maxStateBytes cap to avoid silently truncating user/model state on
// rewrite.
func (l *Loop) appendIterationLog(result *iterationResult, convID string, sleep time.Duration) {
	statePath := l.stateFilePath()

	// Read the full file (uncapped) to avoid truncation on rewrite.
	var content string
	data, err := os.ReadFile(statePath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			// Non-trivial read error — abort to avoid data loss.
			l.deps.Logger.Warn("failed to read state file for iteration log, skipping append",
				"error", err,
				"conversation_id", convID,
			)
			return
		}
		// File doesn't exist yet — start with empty content.
		content = ""
	} else {
		content = string(data)
	}

	// Prune old iteration logs before appending.
	content = pruneIterationLogs(content, iterationLogRetention)

	// Format tools_called as "[tool_a x3, tool_b x1]".
	toolsList := formatToolsUsed(result.ToolsUsed)

	logBlock := fmt.Sprintf(
		"\n%s conversation=%s model=%s supervisor=%v\n"+
			"     timestamp=%s elapsed=%s\n"+
			"     tools_called=%s\n"+
			"     tokens_in=%d tokens_out=%d sleep_set=%s -->\n",
		iterationLogPrefix,
		convID,
		result.Model,
		result.Supervisor,
		time.Now().UTC().Format(time.RFC3339),
		result.Elapsed.Round(time.Millisecond),
		toolsList,
		result.InputTokens,
		result.OutputTokens,
		sleep.Round(time.Second),
	)

	fullContent := content + logBlock

	// Ensure the parent directory exists (state file may be in a subdir).
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		l.deps.Logger.Warn("failed to create directory for iteration log",
			"error", err,
			"conversation_id", convID,
		)
		return
	}
	if err := os.WriteFile(statePath, []byte(fullContent), 0o644); err != nil {
		l.deps.Logger.Warn("failed to append iteration log",
			"error", err,
			"conversation_id", convID,
		)
	}
}

// formatToolsUsed formats a map[string]int as "[tool_a x3, tool_b]".
// Tools with a count of 1 omit the multiplier.
func formatToolsUsed(tools map[string]int) string {
	if len(tools) == 0 {
		return "[]"
	}

	// Sort for deterministic output.
	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)

	var parts []string
	for _, name := range names {
		count := tools[name]
		if count > 1 {
			parts = append(parts, fmt.Sprintf("%s x%d", name, count))
		} else {
			parts = append(parts, name)
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// pruneIterationLogs removes old iteration log blocks from content,
// keeping the last keepN blocks. Each block is removed individually so
// any non-log content interleaved between blocks is preserved. Returns
// the pruned content. This is a pure function for easy testing.
func pruneIterationLogs(content string, keepN int) string {
	// Find all iteration log block positions.
	var positions []int
	searchFrom := 0
	for {
		idx := strings.Index(content[searchFrom:], iterationLogPrefix)
		if idx < 0 {
			break
		}
		positions = append(positions, searchFrom+idx)
		searchFrom += idx + len(iterationLogPrefix)
	}

	if len(positions) <= keepN {
		return content
	}

	// Remove the oldest blocks individually, preserving content between them.
	removeCount := len(positions) - keepN
	var result strings.Builder
	currentPos := 0

	for i := 0; i < removeCount; i++ {
		blockPos := positions[i]

		// Include a preceding newline in the removal if present.
		blockStart := blockPos
		if blockStart > 0 && content[blockStart-1] == '\n' {
			blockStart--
		}

		// Write any content between the previous position and this block.
		if currentPos < blockStart {
			result.WriteString(content[currentPos:blockStart])
		}

		// Find the end of this block: "-->\n".
		endMarker := strings.Index(content[blockPos:], "-->\n")
		if endMarker < 0 {
			// Malformed block — treat as running to end of content.
			currentPos = len(content)
			break
		}
		currentPos = blockPos + endMarker + len("-->\n")
	}

	// Append any remaining content after the last removed block.
	if currentPos < len(content) {
		result.WriteString(content[currentPos:])
	}
	return result.String()
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
