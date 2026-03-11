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
//
// The loop lifecycle is managed by the [loop] package. This package
// provides [BuildLoopConfig] to assemble a [loop.Config] with the
// correct TaskBuilder, PostIterate, and tool exclusions, plus
// [RegisterTools] for metacog-specific tool handlers.
package metacognitive

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/logging"
	"github.com/nugget/thane-ai-agent/internal/loop"
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

// Opts holds options for [BuildLoopConfig] that are not part of [Config].
type Opts struct {
	// WorkspacePath is the absolute path to the workspace directory.
	WorkspacePath string
	// EgoFile is the absolute path to ego.md. Empty disables
	// the append_ego_observation tool.
	EgoFile string
}

// BuildLoopConfig returns a [loop.Config] that implements the
// metacognitive loop. The returned config uses TaskBuilder and
// PostIterate closures to read state, build prompts, and append
// iteration logs — all previously handled by the old Loop struct.
func BuildLoopConfig(cfg Config, opts Opts) loop.Config {
	return loop.Config{
		Name:         "metacognitive",
		SleepMin:     cfg.MinSleep,
		SleepMax:     cfg.MaxSleep,
		SleepDefault: cfg.DefaultSleep,
		Jitter:       loop.Float64Ptr(cfg.Jitter),
		ExcludeTools: metacogExcludeTools,

		Supervisor:     cfg.SupervisorProbability > 0,
		SupervisorProb: cfg.SupervisorProbability,
		QualityFloor:   cfg.QualityFloor,
		// TaskBuilder handles supervisor augmentation itself via
		// prompts.MetacognitivePrompt, so SupervisorContext is empty.
		SupervisorQualityFloor: cfg.SupervisorQualityFloor,

		Hints: map[string]string{
			"source":                    "metacognitive",
			router.HintMission:          "metacognitive",
			router.HintDelegationGating: "disabled",
		},

		TaskBuilder: func(ctx context.Context, isSupervisor bool) (string, error) {
			stateContent, err := readStateFile(opts.WorkspacePath, cfg.StateFile)
			if err != nil {
				log := logging.Logger(ctx)
				if errors.Is(err, fs.ErrNotExist) {
					log.Info("metacognitive state file not found, starting fresh",
						"path", stateFilePath(opts.WorkspacePath, cfg.StateFile),
					)
				} else {
					log.Warn("metacognitive state file read failed, starting with empty state",
						"error", err,
						"path", stateFilePath(opts.WorkspacePath, cfg.StateFile),
					)
				}
				stateContent = ""
			}
			return prompts.MetacognitivePrompt(stateContent, isSupervisor), nil
		},

		PostIterate: func(ctx context.Context, result loop.IterationResult) error {
			log := logging.Logger(ctx)
			appendIterationLog(log, stateFilePath(opts.WorkspacePath, cfg.StateFile), &result)
			return nil
		},
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

// readStateFile reads the metacognitive state file from the workspace.
// Returns an error if the file does not exist (first iteration).
// Content is capped at [maxStateBytes].
func readStateFile(workspacePath, stateFile string) (string, error) {
	data, err := os.ReadFile(stateFilePath(workspacePath, stateFile))
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
func stateFilePath(workspacePath, stateFile string) string {
	return filepath.Join(workspacePath, stateFile)
}

// appendIterationLog appends an HTML comment summary block to the state
// file after a successful iteration. Old logs beyond
// [iterationLogRetention] are pruned. This is best-effort: errors are
// logged but never disrupt the loop.
//
// Unlike [readStateFile], this reads the full file without the
// maxStateBytes cap to avoid silently truncating user/model state on
// rewrite.
func appendIterationLog(log *slog.Logger, statePath string, result *loop.IterationResult) {
	// Read the full file (uncapped) to avoid truncation on rewrite.
	var content string
	data, err := os.ReadFile(statePath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			// Non-trivial read error — abort to avoid data loss.
			log.Warn("failed to read state file for iteration log, skipping append",
				"error", err,
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
		result.ConvID,
		result.Model,
		result.Supervisor,
		time.Now().UTC().Format(time.RFC3339),
		result.Elapsed.Round(time.Second),
		toolsList,
		result.InputTokens,
		result.OutputTokens,
		result.Sleep.Round(time.Second),
	)

	fullContent := content + logBlock

	// Ensure the parent directory exists (state file may be in a subdir).
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		log.Warn("failed to create directory for iteration log",
			"error", err,
		)
		return
	}
	if err := os.WriteFile(statePath, []byte(fullContent), 0o644); err != nil {
		log.Warn("failed to append iteration log",
			"error", err,
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
