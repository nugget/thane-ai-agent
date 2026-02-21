package metacognitive

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// --- Test helpers ---

// fixedRand returns a deterministic value for testing.
type fixedRand struct{ value float64 }

func (r *fixedRand) Float64() float64 { return r.value }

// mockRunner captures requests and returns a canned response.
type mockRunner struct {
	mu       sync.Mutex
	requests []*agent.Request
	resp     *agent.Response
	err      error
}

func (m *mockRunner) Run(_ context.Context, req *agent.Request, _ agent.StreamCallback) (*agent.Response, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	return m.resp, m.err
}

func (m *mockRunner) getRequests() []*agent.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*agent.Request, len(m.requests))
	copy(out, m.requests)
	return out
}

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

func testDeps(t *testing.T, runner agentRunner) Deps {
	t.Helper()
	return Deps{
		Runner:        runner,
		Logger:        slog.Default(),
		WorkspacePath: t.TempDir(),
		RandSource:    &fixedRand{value: 0.5},
	}
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

// --- Dice tests ---

func TestRollDice_NeverSupervisor(t *testing.T) {
	l := New(testConfig(), testDeps(t, nil))
	// SupervisorProbability is 0.0 by default in testConfig.
	for range 100 {
		if l.rollDice() {
			t.Fatal("rollDice should never return true with probability 0.0")
		}
	}
}

func TestRollDice_AlwaysSupervisor(t *testing.T) {
	cfg := testConfig()
	cfg.SupervisorProbability = 1.0
	l := New(cfg, testDeps(t, nil))

	for range 100 {
		if !l.rollDice() {
			t.Fatal("rollDice should always return true with probability 1.0")
		}
	}
}

func TestRollDice_Deterministic(t *testing.T) {
	cfg := testConfig()
	cfg.SupervisorProbability = 0.5

	tests := []struct {
		name     string
		randVal  float64
		wantSupv bool
	}{
		{"below_threshold", 0.3, true},  // 0.3 < 0.5
		{"above_threshold", 0.7, false}, // 0.7 >= 0.5
		{"at_threshold", 0.5, false},    // 0.5 >= 0.5 (not strictly less)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := testDeps(t, nil)
			deps.RandSource = &fixedRand{value: tt.randVal}
			l := New(cfg, deps)

			got := l.rollDice()
			if got != tt.wantSupv {
				t.Errorf("rollDice() = %v, want %v (rand=%f, prob=%f)",
					got, tt.wantSupv, tt.randVal, cfg.SupervisorProbability)
			}
		})
	}
}

// --- Sleep computation tests ---

func TestComputeSleep_Default(t *testing.T) {
	l := New(testConfig(), testDeps(t, nil))
	// No tool call → uses DefaultSleep.
	got := l.computeSleep()
	if got != 10*time.Minute {
		t.Errorf("computeSleep = %v, want 10m (default)", got)
	}
}

func TestComputeSleep_ToolProvided(t *testing.T) {
	l := New(testConfig(), testDeps(t, nil))
	l.setNextSleep(5 * time.Minute)

	got := l.computeSleep()
	if got != 5*time.Minute {
		t.Errorf("computeSleep = %v, want 5m (tool-provided)", got)
	}
}

func TestComputeSleep_ClampMin(t *testing.T) {
	l := New(testConfig(), testDeps(t, nil))
	l.setNextSleep(30 * time.Second) // below MinSleep of 2m

	got := l.computeSleep()
	if got != 2*time.Minute {
		t.Errorf("computeSleep = %v, want 2m (clamped to min)", got)
	}
}

func TestComputeSleep_ClampMax(t *testing.T) {
	l := New(testConfig(), testDeps(t, nil))
	l.setNextSleep(1 * time.Hour) // above MaxSleep of 30m

	got := l.computeSleep()
	if got != 30*time.Minute {
		t.Errorf("computeSleep = %v, want 30m (clamped to max)", got)
	}
}

func TestComputeSleep_ZeroJitter(t *testing.T) {
	cfg := testConfig()
	cfg.Jitter = 0.0
	l := New(cfg, testDeps(t, nil))
	l.setNextSleep(15 * time.Minute)

	got := l.computeSleep()
	if got != 15*time.Minute {
		t.Errorf("computeSleep = %v, want exactly 15m with zero jitter", got)
	}
}

func TestComputeSleep_Jitter(t *testing.T) {
	cfg := testConfig()
	cfg.Jitter = 0.2 // ±20%

	deps := testDeps(t, nil)
	deps.RandSource = &fixedRand{value: 0.0} // worst case: factor = 1 + 0.2*(2*0-1) = 0.8
	l := New(cfg, deps)
	l.setNextSleep(10 * time.Minute)

	got := l.computeSleep()
	expected := 8 * time.Minute // 10m * 0.8
	if got != expected {
		t.Errorf("computeSleep = %v, want %v (jitter with rand=0.0)", got, expected)
	}
}

func TestComputeSleep_JitterClampedToBounds(t *testing.T) {
	cfg := testConfig()
	cfg.Jitter = 0.5 // ±50%
	cfg.MinSleep = 5 * time.Minute

	deps := testDeps(t, nil)
	deps.RandSource = &fixedRand{value: 0.0} // factor = 1 + 0.5*(0-1) = 0.5
	l := New(cfg, deps)
	l.setNextSleep(8 * time.Minute)

	got := l.computeSleep()
	// 8m * 0.5 = 4m, but clamped to min of 5m.
	if got != 5*time.Minute {
		t.Errorf("computeSleep = %v, want 5m (jittered below min, clamped)", got)
	}
}

func TestResetNextSleep(t *testing.T) {
	l := New(testConfig(), testDeps(t, nil))
	l.setNextSleep(5 * time.Minute)
	l.resetNextSleep()

	got := l.computeSleep()
	if got != 10*time.Minute {
		t.Errorf("after reset, computeSleep = %v, want 10m (default)", got)
	}
}

// --- Lifecycle tests ---

func TestStartStop(t *testing.T) {
	runner := &mockRunner{
		resp: &agent.Response{Content: "ok"},
	}
	cfg := testConfig()
	cfg.DefaultSleep = 1 * time.Hour // long sleep so the loop doesn't iterate fast

	l := New(cfg, testDeps(t, runner))

	ctx, cancel := context.WithCancel(context.Background())

	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the goroutine time to start its first iteration.
	time.Sleep(100 * time.Millisecond)

	cancel()
	l.Stop()
	// If Stop() doesn't return, the test deadlocks (caught by -timeout).
}

func TestDoubleStartNoop(t *testing.T) {
	runner := &mockRunner{
		resp: &agent.Response{Content: "ok"},
	}

	l := New(testConfig(), testDeps(t, runner))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := l.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if err := l.Start(ctx); err != nil {
		t.Fatalf("second Start should be noop: %v", err)
	}

	cancel()
	l.Stop()
}

func TestDoubleStopNoop(t *testing.T) {
	runner := &mockRunner{
		resp: &agent.Response{Content: "ok"},
	}

	l := New(testConfig(), testDeps(t, runner))
	ctx, cancel := context.WithCancel(context.Background())

	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	cancel()
	l.Stop()
	l.Stop() // second stop should not panic or deadlock
}

func TestStopBeforeStart(t *testing.T) {
	l := New(testConfig(), testDeps(t, nil))
	l.Stop() // should not panic
}

// --- Iteration tests ---

func TestIterate_NormalHints(t *testing.T) {
	runner := &mockRunner{
		resp: &agent.Response{Content: "ok", Model: "llama3"},
	}

	deps := testDeps(t, runner)
	l := New(testConfig(), deps)

	if err := l.iterate(context.Background(), false); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	reqs := runner.getRequests()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}

	req := reqs[0]
	if req.Hints["source"] != "metacognitive" {
		t.Errorf("source hint = %q, want %q", req.Hints["source"], "metacognitive")
	}
	if req.Hints["local_only"] != "true" {
		t.Errorf("local_only hint = %q, want %q", req.Hints["local_only"], "true")
	}
	if req.Hints["quality_floor"] != "3" {
		t.Errorf("quality_floor hint = %q, want %q", req.Hints["quality_floor"], "3")
	}
	if req.Hints["supervisor"] != "false" {
		t.Errorf("supervisor hint = %q, want %q", req.Hints["supervisor"], "false")
	}
	if req.Hints["mission"] != "metacognitive" {
		t.Errorf("mission hint = %q, want %q", req.Hints["mission"], "metacognitive")
	}
	if !strings.HasPrefix(req.ConversationID, "metacog-") {
		t.Errorf("ConversationID = %q, want prefix metacog-", req.ConversationID)
	}
}

func TestIterate_SupervisorHints(t *testing.T) {
	runner := &mockRunner{
		resp: &agent.Response{Content: "ok", Model: "claude-opus"},
	}

	deps := testDeps(t, runner)
	l := New(testConfig(), deps)

	if err := l.iterate(context.Background(), true); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	reqs := runner.getRequests()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}

	req := reqs[0]
	if req.Hints["local_only"] != "false" {
		t.Errorf("supervisor local_only = %q, want %q", req.Hints["local_only"], "false")
	}
	if req.Hints["quality_floor"] != "8" {
		t.Errorf("supervisor quality_floor = %q, want %q", req.Hints["quality_floor"], "8")
	}
	if req.Hints["supervisor"] != "true" {
		t.Errorf("supervisor hint = %q, want %q", req.Hints["supervisor"], "true")
	}
}

func TestIterate_NoStateFile(t *testing.T) {
	runner := &mockRunner{
		resp: &agent.Response{Content: "created state file"},
	}

	deps := testDeps(t, runner)
	l := New(testConfig(), deps)

	// No state file exists in the temp dir.
	if err := l.iterate(context.Background(), false); err != nil {
		t.Fatalf("iterate with no state file: %v", err)
	}

	reqs := runner.getRequests()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}

	// The prompt should contain the first-iteration placeholder.
	msg := reqs[0].Messages[0].Content
	if !strings.Contains(msg, "does not exist yet") {
		t.Error("prompt should contain first-iteration placeholder when state file is missing")
	}
}

func TestIterate_WithStateFile(t *testing.T) {
	runner := &mockRunner{
		resp: &agent.Response{Content: "observed"},
	}

	deps := testDeps(t, runner)
	stateContent := "## Current Sense\nEverything is calm."
	stateFile := filepath.Join(deps.WorkspacePath, "metacognitive.md")
	if err := os.WriteFile(stateFile, []byte(stateContent), 0644); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	l := New(testConfig(), deps)

	if err := l.iterate(context.Background(), false); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	reqs := runner.getRequests()
	msg := reqs[0].Messages[0].Content
	if !strings.Contains(msg, "Everything is calm.") {
		t.Error("prompt should contain state file content")
	}
}

func TestIterate_RunnerError(t *testing.T) {
	runner := &mockRunner{
		err: context.DeadlineExceeded,
	}

	deps := testDeps(t, runner)
	l := New(testConfig(), deps)

	err := l.iterate(context.Background(), false)
	if err == nil {
		t.Fatal("iterate should return error when runner fails")
	}
	if !strings.Contains(err.Error(), "metacognitive LLM call") {
		t.Errorf("error = %v, want wrapped metacognitive LLM call error", err)
	}
}

func TestIterate_StateFileTruncated(t *testing.T) {
	runner := &mockRunner{
		resp: &agent.Response{Content: "ok"},
	}

	deps := testDeps(t, runner)

	// Create a state file larger than maxStateBytes.
	big := strings.Repeat("x", maxStateBytes+1000)
	stateFile := filepath.Join(deps.WorkspacePath, "metacognitive.md")
	if err := os.WriteFile(stateFile, []byte(big), 0644); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	l := New(testConfig(), deps)

	if err := l.iterate(context.Background(), false); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	reqs := runner.getRequests()
	msg := reqs[0].Messages[0].Content
	if !strings.Contains(msg, "truncated") {
		t.Error("oversized state file should be truncated with marker")
	}
}

// --- Tool handler tests ---

func TestSetNextSleep_Valid(t *testing.T) {
	l := New(testConfig(), testDeps(t, nil))

	reg := tools.NewRegistry(nil, nil)
	l.RegisterTools(reg)

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

	// Verify the loop picked up the duration.
	got := l.computeSleep()
	if got != 5*time.Minute {
		t.Errorf("after tool call, computeSleep = %v, want 5m", got)
	}
}

func TestSetNextSleep_Clamped(t *testing.T) {
	l := New(testConfig(), testDeps(t, nil))

	reg := tools.NewRegistry(nil, nil)
	l.RegisterTools(reg)

	tool := reg.Get("set_next_sleep")

	// Below minimum.
	_, err := tool.Handler(context.Background(), map[string]any{
		"duration": "30s",
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	got := l.computeSleep()
	if got != 2*time.Minute {
		t.Errorf("below-min: computeSleep = %v, want 2m", got)
	}

	// Above maximum.
	l.resetNextSleep()
	_, err = tool.Handler(context.Background(), map[string]any{
		"duration": "1h",
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	got = l.computeSleep()
	if got != 30*time.Minute {
		t.Errorf("above-max: computeSleep = %v, want 30m", got)
	}
}

func TestSetNextSleep_InvalidFormat(t *testing.T) {
	l := New(testConfig(), testDeps(t, nil))

	reg := tools.NewRegistry(nil, nil)
	l.RegisterTools(reg)

	tool := reg.Get("set_next_sleep")

	_, err := tool.Handler(context.Background(), map[string]any{
		"duration": "not-a-duration",
	})
	if err == nil {
		t.Fatal("handler should fail for invalid duration format")
	}
}

func TestSetNextSleep_MissingDuration(t *testing.T) {
	l := New(testConfig(), testDeps(t, nil))

	reg := tools.NewRegistry(nil, nil)
	l.RegisterTools(reg)

	tool := reg.Get("set_next_sleep")

	_, err := tool.Handler(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("handler should fail when duration is missing")
	}
}
