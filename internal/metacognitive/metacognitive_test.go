package metacognitive

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// --- Test helpers ---

func testConfig() Config {
	return Config{
		Enabled:                true,
		StateFile:              "metacognitive.md",
		MinSleep:               2 * time.Minute,
		MaxSleep:               30 * time.Minute,
		DefaultSleep:           10 * time.Minute,
		Jitter:                 0.0, // deterministic by default
		SupervisorProbability:  0.0,
		QualityFloor:           3,
		SupervisorQualityFloor: 8,
	}
}

// noopRunner satisfies loop.Runner for tests that don't need real LLM calls.
type noopRunner struct{}

func (r *noopRunner) Run(_ context.Context, _ loop.RunRequest, _ loop.StreamCallback) (*loop.RunResponse, error) {
	return &loop.RunResponse{
		Content:      "ok",
		Model:        "test-model",
		InputTokens:  10,
		OutputTokens: 5,
	}, nil
}

// testLoopForTools creates a *loop.Loop suitable for tool handler tests.
func testLoopForTools(t *testing.T) *loop.Loop {
	t.Helper()
	l, err := loop.New(loop.Config{Name: "test-metacog", Task: "test"}, loop.Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("loop.New: %v", err)
	}
	return l
}

// --- ParseConfig tests ---

func TestParseConfig_Valid(t *testing.T) {
	raw := config.MetacognitiveConfig{
		Enabled:               true,
		StateFile:             "state.md",
		MinSleep:              "2m",
		MaxSleep:              "30m",
		DefaultSleep:          "10m",
		Jitter:                0.2,
		SupervisorProbability: 0.1,
		Router:                config.MetacognitiveRouterConfig{QualityFloor: 3},
		SupervisorRouter:      config.MetacognitiveRouterConfig{QualityFloor: 8},
	}

	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	if cfg.MinSleep != 2*time.Minute {
		t.Errorf("MinSleep = %v, want 2m", cfg.MinSleep)
	}
	if cfg.MaxSleep != 30*time.Minute {
		t.Errorf("MaxSleep = %v, want 30m", cfg.MaxSleep)
	}
	if cfg.DefaultSleep != 10*time.Minute {
		t.Errorf("DefaultSleep = %v, want 10m", cfg.DefaultSleep)
	}
	if cfg.QualityFloor != 3 {
		t.Errorf("QualityFloor = %d, want 3", cfg.QualityFloor)
	}
	if cfg.SupervisorQualityFloor != 8 {
		t.Errorf("SupervisorQualityFloor = %d, want 8", cfg.SupervisorQualityFloor)
	}
}

func TestParseConfig_InvalidDuration(t *testing.T) {
	tests := []struct {
		name string
		raw  config.MetacognitiveConfig
	}{
		{"bad_min_sleep", config.MetacognitiveConfig{MinSleep: "bogus", MaxSleep: "30m", DefaultSleep: "10m"}},
		{"bad_max_sleep", config.MetacognitiveConfig{MinSleep: "2m", MaxSleep: "bogus", DefaultSleep: "10m"}},
		{"bad_default_sleep", config.MetacognitiveConfig{MinSleep: "2m", MaxSleep: "30m", DefaultSleep: "bogus"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseConfig(tt.raw)
			if err == nil {
				t.Error("ParseConfig should fail for invalid duration")
			}
		})
	}
}

// --- readStateFile tests ---

func TestReadStateFile_Missing(t *testing.T) {
	tmpDir := t.TempDir()
	_, err := readStateFile(filepath.Join(tmpDir, "metacognitive.md"))
	if err == nil {
		t.Fatal("readStateFile should return error for missing file")
	}
}

func TestReadStateFile_Present(t *testing.T) {
	tmpDir := t.TempDir()
	stateContent := "## Current Sense\nEverything is calm."
	stateFile := filepath.Join(tmpDir, "metacognitive.md")
	if err := os.WriteFile(stateFile, []byte(stateContent), 0644); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	got, err := readStateFile(stateFile)
	if err != nil {
		t.Fatalf("readStateFile: %v", err)
	}
	if got != stateContent {
		t.Errorf("readStateFile = %q, want %q", got, stateContent)
	}
}

func TestReadStateFile_Truncated(t *testing.T) {
	tmpDir := t.TempDir()
	big := strings.Repeat("x", maxStateBytes+1000)
	stateFile := filepath.Join(tmpDir, "metacognitive.md")
	if err := os.WriteFile(stateFile, []byte(big), 0644); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	got, err := readStateFile(stateFile)
	if err != nil {
		t.Fatalf("readStateFile: %v", err)
	}
	if !strings.Contains(got, "truncated") {
		t.Error("oversized state file should be truncated with marker")
	}
	if len(got) <= maxStateBytes {
		t.Error("truncated content should be roughly maxStateBytes plus marker")
	}
}

// --- appendIterationLog tests ---

func TestAppendIterationLog(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "metacognitive.md")

	// Write an initial state file.
	initial := "## Current Sense\nAll quiet.\n"
	if err := os.WriteFile(statePath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	result := &loop.IterationResult{
		ConvID:       "metacog-12345",
		Model:        "llama3:8b",
		InputTokens:  12000,
		OutputTokens: 500,
		ToolsUsed:    map[string]int{"get_state": 3, "update_metacognitive_state": 1},
		Elapsed:      3*time.Minute + 12*time.Second,
		Supervisor:   false,
		Sleep:        8 * time.Minute,
	}

	appendIterationLog(context.Background(), slog.Default(), statePath, nil, "", result)

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	s := string(data)

	// Verify the original content is preserved.
	if !strings.Contains(s, "All quiet.") {
		t.Error("original content should be preserved")
	}

	// Verify log block fields.
	if !strings.Contains(s, "<!-- iteration_log:") {
		t.Error("should contain iteration_log prefix")
	}
	if !strings.Contains(s, "conversation=metacog-12345") {
		t.Error("should contain conversation ID")
	}
	if !strings.Contains(s, "model=llama3:8b") {
		t.Error("should contain model name")
	}
	if !strings.Contains(s, "supervisor=false") {
		t.Error("should contain supervisor flag")
	}
	if !strings.Contains(s, "tokens_in=12000") {
		t.Error("should contain input tokens")
	}
	if !strings.Contains(s, "tokens_out=500") {
		t.Error("should contain output tokens")
	}
	if !strings.Contains(s, "sleep_set=8m0s") {
		t.Error("should contain sleep duration")
	}
	if !strings.Contains(s, "get_state x3") {
		t.Error("should contain tools with counts")
	}
	if !strings.Contains(s, "update_metacognitive_state") {
		t.Error("should contain tool names")
	}
	if !strings.Contains(s, "elapsed=3m12s") {
		t.Error("should contain elapsed time")
	}
}

func TestAppendIterationLog_NoStateFile(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "metacognitive.md")

	result := &loop.IterationResult{
		ConvID:       "metacog-first",
		Model:        "test-model",
		InputTokens:  100,
		OutputTokens: 50,
		Elapsed:      time.Second,
		Sleep:        5 * time.Minute,
	}

	appendIterationLog(context.Background(), slog.Default(), statePath, nil, "", result)

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	s := string(data)

	if !strings.Contains(s, "<!-- iteration_log:") {
		t.Error("log block should be written even when no state file existed")
	}
	if !strings.Contains(s, "conversation=metacog-first") {
		t.Error("should contain conversation ID")
	}
}

type panicProvenanceWriter struct{}

func (*panicProvenanceWriter) Read(string) (string, error) {
	panic("unexpected provenance read")
}

func (*panicProvenanceWriter) Write(context.Context, string, string, string) error {
	panic("unexpected provenance write")
}

func TestAppendIterationLog_TypedNilProvenanceWriterFallsBackToFileIO(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "metacognitive.md")
	if err := os.WriteFile(statePath, []byte("## Current Sense\nStill here.\n"), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	var store ProvenanceWriter = (*panicProvenanceWriter)(nil)
	result := &loop.IterationResult{
		ConvID:       "metacog-typed-nil",
		Model:        "llama3:8b",
		InputTokens:  111,
		OutputTokens: 22,
		Elapsed:      2 * time.Second,
		Sleep:        3 * time.Minute,
	}

	appendIterationLog(context.Background(), slog.Default(), statePath, store, "metacognitive.md", result)

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !strings.Contains(string(data), "conversation=metacog-typed-nil") {
		t.Error("iteration log should still be appended via direct file I/O")
	}
}

// --- pruneIterationLogs tests ---

func TestPruneIterationLogs_NoLogs(t *testing.T) {
	content := "## Current Sense\nAll quiet.\n\n<!-- metacognitive: iteration=x updated=y -->\n"
	got := pruneIterationLogs(content, 5)
	if got != content {
		t.Errorf("content with no iteration logs should be unchanged\ngot: %q\nwant: %q", got, content)
	}
}

func TestPruneIterationLogs_UnderLimit(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("## State\n")
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&sb, "\n<!-- iteration_log: conversation=c%d model=m supervisor=false\n     tokens_in=100 tokens_out=50 -->\n", i)
	}
	content := sb.String()

	got := pruneIterationLogs(content, 5)
	if got != content {
		t.Error("content with fewer logs than limit should be unchanged")
	}
}

func TestPruneIterationLogs_AtLimit(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("## State\n")
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&sb, "\n<!-- iteration_log: conversation=c%d model=m supervisor=false\n     tokens_in=100 tokens_out=50 -->\n", i)
	}
	content := sb.String()

	got := pruneIterationLogs(content, 5)
	if got != content {
		t.Error("content with exactly limit logs should be unchanged")
	}
}

func TestPruneIterationLogs_OverLimit(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("## State\nSome content.\n")
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&sb, "\n<!-- iteration_log: conversation=c%d model=m supervisor=false\n     tokens_in=100 tokens_out=50 -->\n", i)
	}
	content := sb.String()

	got := pruneIterationLogs(content, 5)

	// Should contain last 5 (c3..c7) but not first 3 (c0..c2).
	for i := 0; i < 3; i++ {
		if strings.Contains(got, fmt.Sprintf("conversation=c%d", i)) {
			t.Errorf("pruned content should not contain conversation=c%d", i)
		}
	}
	for i := 3; i < 8; i++ {
		if !strings.Contains(got, fmt.Sprintf("conversation=c%d", i)) {
			t.Errorf("pruned content should contain conversation=c%d", i)
		}
	}

	// Original non-log content should be preserved.
	if !strings.Contains(got, "Some content.") {
		t.Error("non-log content should be preserved")
	}
}

// --- formatToolsUsed tests ---

func TestFormatToolsUsed_Empty(t *testing.T) {
	got := formatToolsUsed(nil)
	if got != "[]" {
		t.Errorf("formatToolsUsed(nil) = %q, want %q", got, "[]")
	}
}

func TestFormatToolsUsed_Sorted(t *testing.T) {
	toolsMap := map[string]int{
		"set_next_sleep":             1,
		"get_state":                  3,
		"update_metacognitive_state": 1,
	}
	got := formatToolsUsed(toolsMap)

	// Should be sorted alphabetically.
	if got != "[get_state x3, set_next_sleep, update_metacognitive_state]" {
		t.Errorf("formatToolsUsed = %q, want sorted with counts", got)
	}
}

// --- BuildLoopConfig tests ---

func TestBuildLoopConfig(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig()
	opts := Opts{
		WorkspacePath: tmpDir,
		StateFilePath: filepath.Join(tmpDir, cfg.StateFile),
	}

	lc := BuildLoopConfig(cfg, opts)

	if lc.Name != "metacognitive" {
		t.Errorf("Name = %q, want metacognitive", lc.Name)
	}
	if lc.SleepMin != cfg.MinSleep {
		t.Errorf("SleepMin = %v, want %v", lc.SleepMin, cfg.MinSleep)
	}
	if lc.SleepMax != cfg.MaxSleep {
		t.Errorf("SleepMax = %v, want %v", lc.SleepMax, cfg.MaxSleep)
	}
	if lc.SleepDefault != cfg.DefaultSleep {
		t.Errorf("SleepDefault = %v, want %v", lc.SleepDefault, cfg.DefaultSleep)
	}
	if lc.Jitter == nil || *lc.Jitter != cfg.Jitter {
		t.Errorf("Jitter = %v, want %v", lc.Jitter, cfg.Jitter)
	}
	if lc.TaskBuilder == nil {
		t.Error("TaskBuilder should be set")
	}
	if lc.PostIterate == nil {
		t.Error("PostIterate should be set")
	}
	if lc.Hints["source"] != "metacognitive" {
		t.Errorf("Hints[source] = %q, want metacognitive", lc.Hints["source"])
	}
	if lc.Hints["mission"] != "metacognitive" {
		t.Errorf("Hints[mission] = %q, want metacognitive", lc.Hints["mission"])
	}
	if lc.Hints["delegation_gating"] != "disabled" {
		t.Errorf("Hints[delegation_gating] = %q, want disabled", lc.Hints["delegation_gating"])
	}

	// Verify ExcludeTools contains key entries.
	excluded := make(map[string]bool)
	for _, name := range lc.ExcludeTools {
		excluded[name] = true
	}
	for _, want := range []string{"file_grep", "file_write", "exec"} {
		if !excluded[want] {
			t.Errorf("expected %q in ExcludeTools", want)
		}
	}
}

func TestBuildLoopConfig_TaskBuilder(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig()
	statePath := filepath.Join(tmpDir, cfg.StateFile)
	opts := Opts{
		WorkspacePath: tmpDir,
		StateFilePath: statePath,
	}

	// Write a state file for the TaskBuilder to read.
	stateContent := "## Current Sense\nAll systems nominal."
	if err := os.WriteFile(statePath, []byte(stateContent), 0644); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	lc := BuildLoopConfig(cfg, opts)
	prompt, err := lc.TaskBuilder(context.Background(), false)
	if err != nil {
		t.Fatalf("TaskBuilder: %v", err)
	}

	if !strings.Contains(prompt, "All systems nominal") {
		t.Error("TaskBuilder prompt should include state file content")
	}
}

func TestBuildLoopConfig_TaskBuilderNoState(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig()
	opts := Opts{
		WorkspacePath: tmpDir,
		StateFilePath: filepath.Join(tmpDir, cfg.StateFile),
	}

	lc := BuildLoopConfig(cfg, opts)
	prompt, err := lc.TaskBuilder(context.Background(), false)
	if err != nil {
		t.Fatalf("TaskBuilder: %v", err)
	}

	// With no state file, prompt should contain the first-iteration placeholder.
	if !strings.Contains(prompt, "does not exist yet") {
		t.Error("TaskBuilder prompt should contain first-iteration placeholder when state file is missing")
	}
}

func TestBuildLoopConfig_PostIterate(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig()
	statePath := filepath.Join(tmpDir, cfg.StateFile)
	opts := Opts{
		WorkspacePath: tmpDir,
		StateFilePath: statePath,
	}

	lc := BuildLoopConfig(cfg, opts)

	result := loop.IterationResult{
		ConvID:       "metacog-post-test",
		Model:        "test-model",
		InputTokens:  100,
		OutputTokens: 50,
		Elapsed:      time.Second,
		Sleep:        5 * time.Minute,
	}

	err := lc.PostIterate(context.Background(), result)
	if err != nil {
		t.Fatalf("PostIterate: %v", err)
	}

	// Verify state file was written with iteration log.
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	s := string(data)

	if !strings.Contains(s, "conversation=metacog-post-test") {
		t.Error("PostIterate should append iteration log with conversation ID")
	}
}

// --- Tool handler tests ---

func TestSetNextSleep_Valid(t *testing.T) {
	cfg := testConfig()
	workspace := t.TempDir()
	theLoop := testLoopForTools(t)

	reg := tools.NewRegistry(nil, nil)
	RegisterTools(reg, theLoop, cfg, filepath.Join(workspace, cfg.StateFile), nil)

	tool := reg.Get("set_next_sleep")
	if tool == nil {
		t.Fatal("set_next_sleep tool not registered")
	}

	result, err := tool.Handler(context.Background(), map[string]any{
		"duration": "5m",
		"reason":   "testing",
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	if !strings.Contains(result, "5m") {
		t.Errorf("result = %q, want mention of 5m", result)
	}
}

func TestSetNextSleep_Clamped(t *testing.T) {
	cfg := testConfig()
	workspace := t.TempDir()
	theLoop := testLoopForTools(t)

	reg := tools.NewRegistry(nil, nil)
	RegisterTools(reg, theLoop, cfg, filepath.Join(workspace, cfg.StateFile), nil)

	tool := reg.Get("set_next_sleep")

	// Below minimum — should succeed but clamp.
	result, err := tool.Handler(context.Background(), map[string]any{
		"duration": "30s",
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(result, "2m") {
		t.Errorf("below-min: result = %q, want clamped to 2m", result)
	}

	// Above maximum — should succeed but clamp.
	result, err = tool.Handler(context.Background(), map[string]any{
		"duration": "1h",
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(result, "30m") {
		t.Errorf("above-max: result = %q, want clamped to 30m", result)
	}
}

func TestSetNextSleep_InvalidFormat(t *testing.T) {
	cfg := testConfig()
	workspace := t.TempDir()
	theLoop := testLoopForTools(t)

	reg := tools.NewRegistry(nil, nil)
	RegisterTools(reg, theLoop, cfg, filepath.Join(workspace, cfg.StateFile), nil)

	tool := reg.Get("set_next_sleep")

	_, err := tool.Handler(context.Background(), map[string]any{
		"duration": "not-a-duration",
	})
	if err == nil {
		t.Fatal("handler should fail for invalid duration format")
	}
}

func TestSetNextSleep_MissingDuration(t *testing.T) {
	cfg := testConfig()
	workspace := t.TempDir()
	theLoop := testLoopForTools(t)

	reg := tools.NewRegistry(nil, nil)
	RegisterTools(reg, theLoop, cfg, filepath.Join(workspace, cfg.StateFile), nil)

	tool := reg.Get("set_next_sleep")

	_, err := tool.Handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("handler should fail when duration is missing")
	}
}

func TestSetNextSleep_IntegerMinutes(t *testing.T) {
	cfg := testConfig()
	workspace := t.TempDir()
	theLoop := testLoopForTools(t)

	reg := tools.NewRegistry(nil, nil)
	RegisterTools(reg, theLoop, cfg, filepath.Join(workspace, cfg.StateFile), nil)

	tool := reg.Get("set_next_sleep")

	// Local models often pass integers meaning minutes (JSON numbers
	// unmarshal to float64).
	result, err := tool.Handler(context.Background(), map[string]any{
		"duration": float64(8),
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(result, "8m") {
		t.Errorf("result = %q, want mention of 8m", result)
	}
}

// --- update_metacognitive_state tool tests ---

func TestUpdateMetacognitiveState_Valid(t *testing.T) {
	cfg := testConfig()
	workspace := t.TempDir()
	theLoop := testLoopForTools(t)

	reg := tools.NewRegistry(nil, nil)
	RegisterTools(reg, theLoop, cfg, filepath.Join(workspace, cfg.StateFile), nil)

	tool := reg.Get("update_metacognitive_state")
	if tool == nil {
		t.Fatal("update_metacognitive_state tool not registered")
	}

	content := "## Current Sense\nEverything is calm. Garage closed. Nobody home. Monitoring continues."
	result, err := tool.Handler(context.Background(), map[string]any{
		"content": content,
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(result, "updated") {
		t.Errorf("result = %q, want confirmation", result)
	}

	// Verify file was written.
	statePath := filepath.Join(workspace, "metacognitive.md")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if !strings.Contains(string(data), "Everything is calm") {
		t.Error("state file should contain the written content")
	}
}

func TestUpdateMetacognitiveState_MetadataFooter(t *testing.T) {
	cfg := testConfig()
	workspace := t.TempDir()
	theLoop := testLoopForTools(t)

	reg := tools.NewRegistry(nil, nil)
	RegisterTools(reg, theLoop, cfg, filepath.Join(workspace, cfg.StateFile), nil)

	tool := reg.Get("update_metacognitive_state")
	content := "## Current Sense\nAll systems nominal. Nothing to report. Sleeping for a while."

	_, err := tool.Handler(context.Background(), map[string]any{
		"content": content,
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	statePath := filepath.Join(workspace, "metacognitive.md")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}

	s := string(data)
	// ConvID will be empty in unit tests (loop not running), but footer should still be present.
	if !strings.Contains(s, "<!-- metacognitive: iteration=") {
		t.Error("state file should contain metadata footer")
	}
	if !strings.Contains(s, "updated=") {
		t.Error("state file should contain updated timestamp in footer")
	}
}

func TestUpdateMetacognitiveState_PrevFile(t *testing.T) {
	cfg := testConfig()
	workspace := t.TempDir()
	theLoop := testLoopForTools(t)

	// Write an initial state file.
	statePath := filepath.Join(workspace, "metacognitive.md")
	original := "## Original Content\nThis was here before the update."
	if err := os.WriteFile(statePath, []byte(original), 0o644); err != nil {
		t.Fatalf("write initial state: %v", err)
	}

	reg := tools.NewRegistry(nil, nil)
	RegisterTools(reg, theLoop, cfg, filepath.Join(workspace, cfg.StateFile), nil)

	tool := reg.Get("update_metacognitive_state")
	newContent := "## Updated Content\nNew observations from the latest iteration of monitoring."

	_, err := tool.Handler(context.Background(), map[string]any{
		"content": newContent,
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	// Verify .prev backup exists with original content.
	prevPath := statePath + ".prev"
	prevData, err := os.ReadFile(prevPath)
	if err != nil {
		t.Fatalf("read .prev file: %v", err)
	}
	if string(prevData) != original {
		t.Errorf(".prev content = %q, want original content", string(prevData))
	}
}

func TestUpdateMetacognitiveState_TypedNilProvenanceWriterFallsBackToFileIO(t *testing.T) {
	cfg := testConfig()
	workspace := t.TempDir()
	theLoop := testLoopForTools(t)

	var store ProvenanceWriter = (*panicProvenanceWriter)(nil)

	reg := tools.NewRegistry(nil, nil)
	RegisterTools(reg, theLoop, cfg, filepath.Join(workspace, cfg.StateFile), store)

	tool := reg.Get("update_metacognitive_state")
	content := "## Current Sense\nBattery device drift noted. Watching closely and preserving local fallback path."

	result, err := tool.Handler(context.Background(), map[string]any{
		"content": content,
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !strings.Contains(result, "updated") {
		t.Errorf("result = %q, want local file update confirmation", result)
	}

	statePath := filepath.Join(workspace, "metacognitive.md")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if !strings.Contains(string(data), "Battery device drift noted") {
		t.Error("state file should be written via direct file I/O when provenance writer is typed nil")
	}
}

func TestUpdateMetacognitiveState_EmptyRejected(t *testing.T) {
	cfg := testConfig()
	workspace := t.TempDir()
	theLoop := testLoopForTools(t)

	reg := tools.NewRegistry(nil, nil)
	RegisterTools(reg, theLoop, cfg, filepath.Join(workspace, cfg.StateFile), nil)

	tool := reg.Get("update_metacognitive_state")

	tests := []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"too_short", "Short."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tool.Handler(context.Background(), map[string]any{
				"content": tt.content,
			})
			if err == nil {
				t.Error("handler should reject short/empty content")
			}
		})
	}
}
