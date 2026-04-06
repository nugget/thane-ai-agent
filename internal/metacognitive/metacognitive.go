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
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/logging"
	"github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/prompts"
	"github.com/nugget/thane-ai-agent/internal/router"
)

// DefinitionName is the durable loops-ng definition name for the
// metacognitive service.
const DefinitionName = "metacognitive"

// maxStateBytes is the maximum metacognitive.md content read per
// iteration. Content beyond this limit is truncated with a marker.
const maxStateBytes = 16 * 1024

// iterationLogRetention is the number of iteration log blocks to keep
// when pruning. Oldest beyond this limit are removed.
const iterationLogRetention = 5

// iterationLogPrefix is the HTML comment prefix used for iteration log
// blocks. Used for scanning/pruning.
const iterationLogPrefix = "<!-- iteration_log:"

// ProvenanceWriter is the subset of [provenance.Store] needed by the
// metacognitive loop for committing state file updates.
type ProvenanceWriter interface {
	Read(filename string) (string, error)
	Write(ctx context.Context, filename, content, message string) error
}

// hasProvenanceWriter guards against typed-nil interfaces such as a
// nil *provenance.Store assigned to ProvenanceWriter. A plain store !=
// nil check is insufficient in that case and can lead to nil receiver
// panics at call time.
func hasProvenanceWriter(store ProvenanceWriter) bool {
	if store == nil {
		return false
	}
	v := reflect.ValueOf(store)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface, reflect.Func:
		return !v.IsNil()
	default:
		return true
	}
}

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
	// Used only as a fallback when ProvenanceStore is nil.
	WorkspacePath string

	// StateFilePath is the resolved absolute path to the state file.
	// When a provenance store is configured, this points inside the
	// store; otherwise it falls back to workspace-relative resolution.
	StateFilePath string

	// ProvenanceStore, when non-nil, is used by the iteration logger
	// to commit state file updates with SSH signatures.
	ProvenanceStore ProvenanceWriter

	// StateFileName is the bare filename (e.g. "metacognitive.md") used
	// for provenance store reads and writes. Ignored when ProvenanceStore
	// is nil.
	StateFileName string
}

// DefinitionSpec returns the persistable loops-ng definition for the
// metacognitive service. Runtime hooks are attached later by
// [HydrateSpec] so the definition can live in the durable registry.
func DefinitionSpec(cfg Config) loop.Spec {
	return loop.Spec{
		Name:         DefinitionName,
		Enabled:      cfg.Enabled,
		Task:         "Observe the system, reason about its recent behavior, and update metacognitive state when needed.",
		Operation:    loop.OperationService,
		Completion:   loop.CompletionNone,
		SleepMin:     cfg.MinSleep,
		SleepMax:     cfg.MaxSleep,
		SleepDefault: cfg.DefaultSleep,
		Jitter:       loop.Float64Ptr(cfg.Jitter),
		ExcludeTools: metacogExcludeTools,
		Profile: router.LoopProfile{
			Mission:          "metacognitive",
			DelegationGating: "disabled",
			ExtraHints:       map[string]string{"source": "metacognitive"},
		},

		Supervisor:             cfg.SupervisorProbability > 0,
		SupervisorProb:         cfg.SupervisorProbability,
		QualityFloor:           cfg.QualityFloor,
		SupervisorQualityFloor: cfg.SupervisorQualityFloor,
		Metadata: map[string]string{
			"subsystem": "metacognitive",
			"category":  "service",
		},
	}
}

// HydrateSpec attaches the runtime-only hooks needed to execute the
// metacognitive service from a durable loops-ng definition.
func HydrateSpec(spec loop.Spec, cfg Config, opts Opts) loop.Spec {
	spec = loop.Spec(spec)
	if strings.TrimSpace(spec.Name) == "" {
		spec.Name = DefinitionName
	}
	spec.TaskBuilder = func(ctx context.Context, isSupervisor bool) (string, error) {
		stateContent, err := readStateFile(opts.StateFilePath)
		if err != nil {
			log := logging.Logger(ctx)
			if errors.Is(err, fs.ErrNotExist) {
				log.Info("metacognitive state file not found, starting fresh",
					"path", opts.StateFilePath,
				)
			} else {
				log.Warn("metacognitive state file read failed, starting with empty state",
					"error", err,
					"path", opts.StateFilePath,
				)
			}
			stateContent = ""
		}
		return prompts.MetacognitivePrompt(stateContent, isSupervisor), nil
	}
	spec.PostIterate = func(ctx context.Context, result loop.IterationResult) error {
		log := logging.Logger(ctx)
		appendIterationLog(ctx, log, opts.StateFilePath, opts.ProvenanceStore, opts.StateFileName, &result)
		return nil
	}
	return spec
}

// BuildSpec returns a [loop.Spec] that implements the metacognitive
// loop as a standard loops-ng service. The returned spec uses
// TaskBuilder and PostIterate closures to read state, build prompts,
// and append iteration logs.
func BuildSpec(cfg Config, opts Opts) loop.Spec {
	spec := DefinitionSpec(cfg)
	// TaskBuilder handles supervisor augmentation itself via
	// prompts.MetacognitivePrompt, so SupervisorContext is empty.
	spec.SupervisorContext = ""
	return HydrateSpec(spec, cfg, opts)
}

// BuildLoopConfig returns the engine-facing [loop.Config] view of the
// metacognitive loop. Kept as a compatibility bridge while loops-ng
// adoption is in progress.
func BuildLoopConfig(cfg Config, opts Opts) loop.Config {
	spec := BuildSpec(cfg, opts)
	out := spec.ToConfig()
	profileHints := spec.Profile.Hints()
	if len(profileHints) > 0 {
		if out.Hints == nil {
			out.Hints = make(map[string]string, len(profileHints))
		}
		for k, v := range profileHints {
			out.Hints[k] = v
		}
	}
	return out
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
	"activate_capability", "deactivate_capability",
}

// readStateFile reads the metacognitive state file from the given path.
// Returns an error if the file does not exist (first iteration).
// Content is capped at [maxStateBytes].
func readStateFile(path string) (string, error) {
	data, err := os.ReadFile(path)
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

// appendIterationLog appends an HTML comment summary block to the state
// file after a successful iteration. Old logs beyond
// [iterationLogRetention] are pruned. This is best-effort: errors are
// logged but never disrupt the loop.
//
// When store is non-nil, reads and writes go through the provenance
// store (committed with SSH signatures). When nil, direct file I/O is
// used at statePath.
//
// Unlike [readStateFile], this reads the full file without the
// maxStateBytes cap to avoid silently truncating user/model state on
// rewrite.
func appendIterationLog(ctx context.Context, log *slog.Logger, statePath string, store ProvenanceWriter, stateFileName string, result *loop.IterationResult) {
	// Read the full file (uncapped) to avoid truncation on rewrite.
	var content string
	if hasProvenanceWriter(store) {
		existing, err := store.Read(stateFileName)
		if err != nil && !os.IsNotExist(err) {
			log.Warn("failed to read state file from provenance for iteration log, skipping append",
				"error", err,
			)
			return
		}
		content = existing
	} else {
		data, err := os.ReadFile(statePath)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				log.Warn("failed to read state file for iteration log, skipping append",
					"error", err,
				)
				return
			}
			// File doesn't exist yet — start with empty content.
		} else {
			content = string(data)
		}
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

	if hasProvenanceWriter(store) {
		if err := store.Write(ctx, stateFileName, fullContent, "iteration-log"); err != nil {
			log.Warn("failed to commit iteration log to provenance",
				"error", err,
			)
		}
		return
	}

	// Fallback: direct file I/O.
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
