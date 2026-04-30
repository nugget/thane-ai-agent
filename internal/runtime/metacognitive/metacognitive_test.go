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

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
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

// --- ParseConfig tests ---

func TestParseConfig_Valid(t *testing.T) {
	raw := config.MetacognitiveConfig{
		Enabled:               true,
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
	if cfg.StateFile != "metacognitive.md" {
		t.Errorf("StateFile = %q, want %q", cfg.StateFile, "metacognitive.md")
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
		ToolsUsed:    map[string]int{"get_state": 3, "replace_output_metacognitive_state": 1},
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
	if !strings.Contains(s, "replace_output_metacognitive_state") {
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
		"set_next_sleep":                     1,
		"get_state":                          3,
		"replace_output_metacognitive_state": 1,
	}
	got := formatToolsUsed(toolsMap)

	// Should be sorted alphabetically.
	if got != "[get_state x3, replace_output_metacognitive_state, set_next_sleep]" {
		t.Errorf("formatToolsUsed = %q, want sorted with counts", got)
	}
}

// --- BuildLoopConfig tests ---

func TestBuildSpec(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig()
	opts := Opts{
		WorkspacePath: tmpDir,
		StateFilePath: filepath.Join(tmpDir, cfg.StateFile),
	}

	spec := BuildSpec(cfg, opts)
	if spec.Name != "metacognitive" {
		t.Errorf("Name = %q, want metacognitive", spec.Name)
	}
	if spec.Operation != loop.OperationService {
		t.Errorf("Operation = %q, want %q", spec.Operation, loop.OperationService)
	}
	if spec.Completion != loop.CompletionNone {
		t.Errorf("Completion = %q, want %q", spec.Completion, loop.CompletionNone)
	}
	if spec.Profile.Mission != "metacognitive" {
		t.Errorf("Profile.Mission = %q, want metacognitive", spec.Profile.Mission)
	}
	if spec.Profile.DelegationGating != "disabled" {
		t.Errorf("Profile.DelegationGating = %q, want disabled", spec.Profile.DelegationGating)
	}
	if spec.Profile.ExtraHints["source"] != "metacognitive" {
		t.Errorf("Profile.ExtraHints[source] = %q, want metacognitive", spec.Profile.ExtraHints["source"])
	}
	if len(spec.Profile.InitialTags) != 1 || spec.Profile.InitialTags[0] != "metacog" {
		t.Errorf("Profile.InitialTags = %v, want [metacog]", spec.Profile.InitialTags)
	}
	if len(spec.Outputs) != 1 {
		t.Fatalf("Outputs len = %d, want 1", len(spec.Outputs))
	}
	if spec.Outputs[0].Name != "metacognitive_state" {
		t.Errorf("Outputs[0].Name = %q, want metacognitive_state", spec.Outputs[0].Name)
	}
	if spec.Outputs[0].Ref != "core:metacognitive.md" {
		t.Errorf("Outputs[0].Ref = %q, want core:metacognitive.md", spec.Outputs[0].Ref)
	}
}

func TestDefinitionSpecPersistable(t *testing.T) {
	cfg := testConfig()

	spec := DefinitionSpec(cfg)
	if spec.Name != DefinitionName {
		t.Errorf("Name = %q, want %q", spec.Name, DefinitionName)
	}
	if spec.TaskBuilder != nil || spec.PostIterate != nil || spec.Setup != nil {
		t.Fatal("DefinitionSpec should not include runtime hooks")
	}
	if err := spec.ValidatePersistable(); err != nil {
		t.Fatalf("ValidatePersistable: %v", err)
	}
}

func TestHydrateSpecAttachesLoopRuntimeHooks(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig()
	opts := Opts{
		WorkspacePath: tmpDir,
		StateFilePath: filepath.Join(tmpDir, cfg.StateFile),
		StateFileName: cfg.StateFile,
	}

	spec := HydrateSpec(DefinitionSpec(cfg), cfg, opts)
	if spec.TaskBuilder == nil {
		t.Fatal("TaskBuilder should be set after hydration")
	}
	if spec.PostIterate == nil {
		t.Fatal("PostIterate should be set after hydration")
	}
	if spec.Setup != nil {
		t.Fatal("HydrateSpec should not attach app-level setup hooks")
	}
}

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

	// Write a state file that the old TaskBuilder path would have read.
	// Current content now comes from declared output context instead.
	stateContent := "## Current Sense\nAll systems nominal."
	if err := os.WriteFile(statePath, []byte(stateContent), 0644); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	lc := BuildLoopConfig(cfg, opts)
	prompt, err := lc.TaskBuilder(context.Background(), false)
	if err != nil {
		t.Fatalf("TaskBuilder: %v", err)
	}

	if strings.Contains(prompt, "All systems nominal") {
		t.Error("TaskBuilder prompt should not inline state file content")
	}
	if !strings.Contains(prompt, "replace_output_metacognitive_state") {
		t.Error("TaskBuilder prompt should mention generated output tool")
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

	if strings.Contains(prompt, "does not exist yet") {
		t.Error("TaskBuilder prompt should not carry old first-iteration placeholder")
	}
	if !strings.Contains(prompt, "Declared Durable") {
		t.Error("TaskBuilder prompt should point to declared output context")
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
