package loop

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/router"
)

// mustNew is a test helper that calls New and fails on error.
func mustNew(t *testing.T, cfg Config, deps Deps) *Loop {
	t.Helper()
	l, err := New(cfg, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

func TestRegistryRegisterDeregister(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	l := mustNew(t, Config{Name: "test-loop", Task: "test"}, Deps{Runner: &noopRunner{}})

	if err := r.Register(l); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if r.ActiveCount() != 1 {
		t.Errorf("ActiveCount = %d, want 1", r.ActiveCount())
	}

	// Duplicate registration fails.
	if err := r.Register(l); err == nil {
		t.Error("duplicate Register should fail")
	}

	r.Deregister(l.ID())
	if r.ActiveCount() != 0 {
		t.Errorf("ActiveCount after deregister = %d, want 0", r.ActiveCount())
	}

	// Deregister of unknown ID is a no-op.
	r.Deregister("nonexistent")
}

func TestRegistryConcurrencyLimit(t *testing.T) {
	t.Parallel()

	r := NewRegistry(WithMaxLoops(2))
	runner := &noopRunner{}

	l1 := mustNew(t, Config{Name: "loop-1", Task: "test"}, Deps{Runner: runner})
	l2 := mustNew(t, Config{Name: "loop-2", Task: "test"}, Deps{Runner: runner})
	l3 := mustNew(t, Config{Name: "loop-3", Task: "test"}, Deps{Runner: runner})

	if err := r.Register(l1); err != nil {
		t.Fatalf("Register l1: %v", err)
	}
	if err := r.Register(l2); err != nil {
		t.Fatalf("Register l2: %v", err)
	}
	if err := r.Register(l3); err == nil {
		t.Error("Register l3 should fail at concurrency limit")
	}

	// After deregistering one, we can add another.
	r.Deregister(l1.ID())
	if err := r.Register(l3); err != nil {
		t.Fatalf("Register l3 after deregister: %v", err)
	}
}

func TestRegistryGetAndGetByName(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	l := mustNew(t, Config{Name: "named-loop", Task: "test"}, Deps{Runner: &noopRunner{}})

	if err := r.Register(l); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if got := r.Get(l.ID()); got != l {
		t.Errorf("Get(%q) returned wrong loop", l.ID())
	}
	if got := r.Get("nonexistent"); got != nil {
		t.Error("Get(nonexistent) should return nil")
	}

	if got := r.GetByName("named-loop"); got != l {
		t.Error("GetByName(named-loop) returned wrong loop")
	}
	if got := r.GetByName("missing"); got != nil {
		t.Error("GetByName(missing) should return nil")
	}
}

func TestRegistryList(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	runner := &noopRunner{}
	l1 := mustNew(t, Config{Name: "bravo", Task: "test"}, Deps{Runner: runner})
	l2 := mustNew(t, Config{Name: "alpha", Task: "test"}, Deps{Runner: runner})

	_ = r.Register(l1)
	_ = r.Register(l2)

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	// Should be sorted by name.
	if list[0].Name() != "alpha" {
		t.Errorf("List[0].Name = %q, want alpha", list[0].Name())
	}
	if list[1].Name() != "bravo" {
		t.Errorf("List[1].Name = %q, want bravo", list[1].Name())
	}
}

func TestRegistryStatuses(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	l := mustNew(t, Config{Name: "status-test", Task: "test"}, Deps{Runner: &noopRunner{}})
	_ = r.Register(l)

	statuses := r.Statuses()
	if len(statuses) != 1 {
		t.Fatalf("Statuses len = %d, want 1", len(statuses))
	}
	if statuses[0].Name != "status-test" {
		t.Errorf("Status.Name = %q, want status-test", statuses[0].Name)
	}
	if statuses[0].State != StatePending {
		t.Errorf("Status.State = %q, want %q", statuses[0].State, StatePending)
	}
}

func TestNewFromSpec(t *testing.T) {
	t.Parallel()

	l, err := NewFromSpec(Spec{
		Name:       "from-spec",
		Task:       "hello",
		Operation:  OperationRequestReply,
		Completion: CompletionReturn,
		SleepMin:   1 * time.Millisecond,
		SleepMax:   2 * time.Millisecond,
		Jitter:     Float64Ptr(0),
		MaxIter:    1,
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}
	if l.Name() != "from-spec" {
		t.Fatalf("Name = %q, want from-spec", l.Name())
	}
}

func TestRegistrySpawnSpec(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	id, err := r.SpawnSpec(context.Background(), Spec{
		Name:       "spawn-spec",
		Task:       "test",
		Operation:  OperationRequestReply,
		Completion: CompletionReturn,
		SleepMin:   1 * time.Millisecond,
		SleepMax:   2 * time.Millisecond,
		Jitter:     Float64Ptr(0),
		MaxIter:    1,
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	l := r.Get(id)
	if l == nil {
		t.Fatalf("Get(%q) = nil, want loop", id)
	}
	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not finish")
	}
}

func TestRegistryLaunchRequestReplyWaitsForCompletion(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	result, err := r.Launch(context.Background(), Launch{
		Spec: Spec{
			Name:       "launch-request-reply",
			Task:       "test",
			Operation:  OperationRequestReply,
			Completion: CompletionReturn,
		},
	}, Deps{Runner: &noopRunner{}})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if result.Detached {
		t.Fatal("Detached = true, want false")
	}
	if result.FinalStatus == nil {
		t.Fatal("FinalStatus = nil, want status")
	}
	if result.FinalStatus.Iterations != 1 {
		t.Fatalf("Iterations = %d, want 1", result.FinalStatus.Iterations)
	}
	if got := r.Get(result.LoopID); got != nil {
		t.Fatalf("Get(%q) = %v, want nil after joined completion", result.LoopID, got)
	}
}

func TestRegistryLaunchBackgroundTaskDetaches(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	gate := make(chan struct{})
	result, err := r.Launch(context.Background(), Launch{
		Spec: Spec{
			Name:       "launch-background-task",
			Task:       "test",
			Operation:  OperationBackgroundTask,
			Completion: CompletionConversation,
		},
	}, Deps{Runner: &blockingRunner{gate: gate}})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if !result.Detached {
		t.Fatal("Detached = false, want true")
	}
	if result.FinalStatus != nil {
		t.Fatalf("FinalStatus = %#v, want nil", result.FinalStatus)
	}
	l := r.Get(result.LoopID)
	if l == nil {
		t.Fatalf("Get(%q) = nil, want running loop", result.LoopID)
	}

	close(gate)

	select {
	case <-l.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("detached loop did not finish")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.Get(result.LoopID) == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("loop %q still registered after completion", result.LoopID)
}

func TestRegistryLaunchAppliesRequestOverrides(t *testing.T) {
	t.Parallel()

	var captured Request
	result, err := NewRegistry().Launch(context.Background(), Launch{
		Spec: Spec{
			Name:       "launch-overrides",
			Task:       "base task",
			Operation:  OperationRequestReply,
			Completion: CompletionReturn,
			Profile: router.LoopProfile{
				Model:        "spark/base",
				Mission:      "automation",
				ExcludeTools: []string{"profile_block"},
				InitialTags:  []string{"profile_tag"},
				Instructions: "profile guidance",
			},
			Hints: map[string]string{
				"source": "spec",
			},
			ExcludeTools: []string{"config_block"},
		},
		Task:            "launch task",
		ParentID:        "parent-loop",
		Metadata:        map[string]string{"origin": "launch"},
		ConversationID:  "conv-123",
		Model:           "deepslate/google/gemma-3-4b",
		Hints:           map[string]string{"source": "launch", "custom": "1"},
		AllowedTools:    []string{"ha_get_state"},
		ExcludeTools:    []string{"launch_block"},
		InitialTags:     []string{"launch_tag"},
		SkipContext:     true,
		SkipTagFilter:   true,
		SystemPrompt:    "system prompt",
		MaxIterations:   7,
		MaxOutputTokens: 321,
		ToolTimeout:     2 * time.Second,
		UsageRole:       "delegate",
		UsageTaskName:   "general",
	}, Deps{Runner: &inspectingRunner{
		onRun: func(req RunRequest) {
			captured = Request{
				Model:           req.Model,
				ConversationID:  req.ConversationID,
				Messages:        append([]Message(nil), req.Messages...),
				SkipContext:     req.SkipContext,
				AllowedTools:    append([]string(nil), req.AllowedTools...),
				ExcludeTools:    append([]string(nil), req.ExcludeTools...),
				SkipTagFilter:   req.SkipTagFilter,
				Hints:           cloneStringMap(req.Hints),
				InitialTags:     append([]string(nil), req.InitialTags...),
				MaxIterations:   req.MaxIterations,
				MaxOutputTokens: req.MaxOutputTokens,
				ToolTimeout:     req.ToolTimeout,
				UsageRole:       req.UsageRole,
				UsageTaskName:   req.UsageTaskName,
				SystemPrompt:    req.SystemPrompt,
			}
		},
	}})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	if result.FinalStatus == nil {
		t.Fatal("FinalStatus = nil, want status")
	}
	if result.FinalStatus.ParentID != "parent-loop" {
		t.Fatalf("ParentID = %q, want parent-loop", result.FinalStatus.ParentID)
	}
	if result.FinalStatus.Config.Metadata["origin"] != "launch" {
		t.Fatalf("Metadata origin = %q, want launch", result.FinalStatus.Config.Metadata["origin"])
	}
	if captured.Model != "deepslate/google/gemma-3-4b" {
		t.Fatalf("Model = %q, want deepslate/google/gemma-3-4b", captured.Model)
	}
	if captured.ConversationID != "conv-123" {
		t.Fatalf("ConversationID = %q, want conv-123", captured.ConversationID)
	}
	if got := captured.Messages[0].Content; got != "Instructions: profile guidance\n\nlaunch task" {
		t.Fatalf("Message content = %q", got)
	}
	if !captured.SkipContext || !captured.SkipTagFilter {
		t.Fatalf("Skip flags = %#v", captured)
	}
	if !slices.Equal(captured.AllowedTools, []string{"ha_get_state"}) {
		t.Fatalf("AllowedTools = %#v", captured.AllowedTools)
	}
	for _, want := range []string{"profile_block", "config_block", "launch_block"} {
		if !slices.Contains(captured.ExcludeTools, want) {
			t.Fatalf("ExcludeTools = %#v, missing %q", captured.ExcludeTools, want)
		}
	}
	for _, want := range []string{"profile_tag", "launch_tag"} {
		if !slices.Contains(captured.InitialTags, want) {
			t.Fatalf("InitialTags = %#v, missing %q", captured.InitialTags, want)
		}
	}
	if captured.Hints["mission"] != "automation" {
		t.Fatalf("mission hint = %q, want automation", captured.Hints["mission"])
	}
	if captured.Hints["source"] != "launch" {
		t.Fatalf("source hint = %q, want launch", captured.Hints["source"])
	}
	if captured.Hints["custom"] != "1" {
		t.Fatalf("custom hint = %q, want 1", captured.Hints["custom"])
	}
	if captured.MaxIterations != 7 || captured.MaxOutputTokens != 321 {
		t.Fatalf("limits = iterations %d output %d", captured.MaxIterations, captured.MaxOutputTokens)
	}
	if captured.ToolTimeout != 2*time.Second {
		t.Fatalf("ToolTimeout = %v, want 2s", captured.ToolTimeout)
	}
	if captured.SystemPrompt != "system prompt" {
		t.Fatalf("SystemPrompt = %q, want system prompt", captured.SystemPrompt)
	}
	if captured.UsageRole != "delegate" || captured.UsageTaskName != "general" {
		t.Fatalf("Usage = role %q task %q", captured.UsageRole, captured.UsageTaskName)
	}
}

func TestRegistryShutdownAll(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	runner := &noopRunner{}

	l1 := mustNew(t, Config{
		Name:     "shutdown-1",
		Task:     "test",
		SleepMin: time.Hour,
		SleepMax: time.Hour,
	}, Deps{Runner: runner})
	l2 := mustNew(t, Config{
		Name:     "shutdown-2",
		Task:     "test",
		SleepMin: time.Hour,
		SleepMax: time.Hour,
	}, Deps{Runner: runner})

	_ = r.Register(l1)
	_ = r.Register(l2)

	ctx := context.Background()
	_ = l1.Start(ctx)
	_ = l2.Start(ctx)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	stopped := r.ShutdownAll(shutdownCtx)
	if stopped != 2 {
		t.Errorf("ShutdownAll stopped %d loops, want 2", stopped)
	}
	if r.ActiveCount() != 0 {
		t.Errorf("ActiveCount after shutdown = %d, want 0", r.ActiveCount())
	}
}

func TestRegistryShutdownAllWithUnstartedLoops(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	runner := &noopRunner{}

	// Register but don't start.
	l := mustNew(t, Config{Name: "unstarted", Task: "test"}, Deps{Runner: runner})
	_ = r.Register(l)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stopped := r.ShutdownAll(shutdownCtx)
	if stopped != 1 {
		t.Errorf("ShutdownAll stopped %d, want 1 (unstarted loops should be drained immediately)", stopped)
	}
	if r.ActiveCount() != 0 {
		t.Errorf("ActiveCount = %d, want 0", r.ActiveCount())
	}
}

// noopRunner is a mock Runner that returns immediately with minimal
// data. Used for lifecycle tests where the LLM response doesn't matter.
type noopRunner struct{}

func (r *noopRunner) Run(_ context.Context, _ RunRequest, _ StreamCallback) (*RunResponse, error) {
	return &RunResponse{
		Content:      "ok",
		Model:        "test-model",
		InputTokens:  10,
		OutputTokens: 5,
	}, nil
}

type blockingRunner struct {
	gate chan struct{}
}

func (r *blockingRunner) Run(_ context.Context, _ RunRequest, _ StreamCallback) (*RunResponse, error) {
	<-r.gate
	return &RunResponse{
		Content:      "ok",
		Model:        "test-model",
		InputTokens:  10,
		OutputTokens: 5,
	}, nil
}
