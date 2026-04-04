package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/events"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
)

type testLoopRunner struct{}

func (testLoopRunner) Run(context.Context, looppkg.Request, looppkg.StreamCallback) (*looppkg.Response, error) {
	return &looppkg.Response{Content: "ok", Model: "test/model"}, nil
}

func TestLoopDefinitionRuntimeStartEnabledServices(t *testing.T) {
	t.Parallel()

	registry, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{
		{
			Name:         "office_watch",
			Enabled:      true,
			Task:         "Watch the office.",
			Operation:    looppkg.OperationService,
			Completion:   looppkg.CompletionNone,
			SleepMin:     time.Minute,
			SleepMax:     time.Minute,
			SleepDefault: time.Minute,
			Jitter:       looppkg.Float64Ptr(0),
		},
		{
			Name:       "paused_watch",
			Enabled:    false,
			Task:       "Watch the garage.",
			Operation:  looppkg.OperationService,
			Completion: looppkg.CompletionNone,
		},
		{
			Name:       "delegate_like",
			Enabled:    true,
			Task:       "Handle one request.",
			Operation:  looppkg.OperationRequestReply,
			Completion: looppkg.CompletionReturn,
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	loops := looppkg.NewRegistry()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		loops.ShutdownAll(shutdownCtx)
	})

	runtime := &loopDefinitionRuntime{
		definitions: registry,
		loops:       loops,
		runner:      testLoopRunner{},
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		eventBus:    events.New(),
	}

	result, err := runtime.StartEnabledServices(context.Background())
	if err != nil {
		t.Fatalf("StartEnabledServices: %v", err)
	}
	if result.Started != 1 || result.SkippedDisabled != 1 || result.SkippedNonService != 1 || result.SkippedExisting != 0 {
		t.Fatalf("result = %+v, want started=1 disabled=1 non_service=1 existing=0", result)
	}
	if got := loops.GetByName("office_watch"); got == nil {
		t.Fatal("enabled service definition was not started")
	}
}

func TestLoopDefinitionRuntimeSkipsExistingLoopName(t *testing.T) {
	t.Parallel()

	registry, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{
		{
			Name:         "office_watch",
			Enabled:      true,
			Task:         "Watch the office.",
			Operation:    looppkg.OperationService,
			Completion:   looppkg.CompletionNone,
			SleepMin:     time.Minute,
			SleepMax:     time.Minute,
			SleepDefault: time.Minute,
			Jitter:       looppkg.Float64Ptr(0),
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	loops := looppkg.NewRegistry()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		loops.ShutdownAll(shutdownCtx)
	})
	if _, err := loops.SpawnLoop(context.Background(), looppkg.Config{
		Name:         "office_watch",
		Handler:      func(context.Context, any) error { return nil },
		SleepMin:     time.Minute,
		SleepMax:     time.Minute,
		SleepDefault: time.Minute,
		Jitter:       looppkg.Float64Ptr(0),
	}, looppkg.Deps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}); err != nil {
		t.Fatalf("SpawnLoop: %v", err)
	}

	runtime := &loopDefinitionRuntime{
		definitions: registry,
		loops:       loops,
		runner:      testLoopRunner{},
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	result, err := runtime.StartEnabledServices(context.Background())
	if err != nil {
		t.Fatalf("StartEnabledServices: %v", err)
	}
	if result.Started != 0 || result.SkippedExisting != 1 {
		t.Fatalf("result = %+v, want started=0 existing=1", result)
	}
}

func TestLoopDefinitionRuntimeReconcileDefinitionStopsInactiveService(t *testing.T) {
	t.Parallel()

	registry, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{
		{
			Name:         "office_watch",
			Enabled:      true,
			Task:         "Watch the office.",
			Operation:    looppkg.OperationService,
			Completion:   looppkg.CompletionNone,
			SleepMin:     time.Minute,
			SleepMax:     time.Minute,
			SleepDefault: time.Minute,
			Jitter:       looppkg.Float64Ptr(0),
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	loops := looppkg.NewRegistry()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		loops.ShutdownAll(shutdownCtx)
	})

	runtime := &loopDefinitionRuntime{
		definitions: registry,
		loops:       loops,
		runner:      testLoopRunner{},
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	if err := runtime.ReconcileDefinition(context.Background(), "office_watch"); err != nil {
		t.Fatalf("ReconcileDefinition(active): %v", err)
	}
	if loops.GetByName("office_watch") == nil {
		t.Fatal("expected office_watch to be running after reconcile")
	}

	if err := registry.ApplyPolicy("office_watch", looppkg.DefinitionPolicy{
		State: looppkg.DefinitionPolicyStateInactive,
	}, time.Now()); err != nil {
		t.Fatalf("ApplyPolicy: %v", err)
	}
	if err := runtime.ReconcileDefinition(context.Background(), "office_watch"); err != nil {
		t.Fatalf("ReconcileDefinition(inactive): %v", err)
	}
	if loops.GetByName("office_watch") != nil {
		t.Fatal("expected office_watch to stop after inactive policy")
	}
}

func TestLoopDefinitionRuntimeLaunchDefinition(t *testing.T) {
	t.Parallel()

	registry, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{
		{
			Name:       "delegate_like",
			Enabled:    true,
			Task:       "Handle one request.",
			Operation:  looppkg.OperationRequestReply,
			Completion: looppkg.CompletionReturn,
		},
		{
			Name:       "paused_task",
			Enabled:    false,
			Task:       "Do not run right now.",
			Operation:  looppkg.OperationBackgroundTask,
			Completion: looppkg.CompletionConversation,
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	loops := looppkg.NewRegistry()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		loops.ShutdownAll(shutdownCtx)
	})

	runtime := &loopDefinitionRuntime{
		definitions: registry,
		loops:       loops,
		runner:      testLoopRunner{},
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	result, err := runtime.LaunchDefinition(context.Background(), "delegate_like", looppkg.Launch{
		Task: "Use this request instead.",
	})
	if err != nil {
		t.Fatalf("LaunchDefinition: %v", err)
	}
	if result.Operation != looppkg.OperationRequestReply || result.Response == nil || result.Response.Content != "ok" {
		t.Fatalf("result = %+v, want request_reply response ok", result)
	}

	_, err = runtime.LaunchDefinition(context.Background(), "paused_task", looppkg.Launch{
		CompletionConversationID: "conv-1",
	})
	var inactive *looppkg.InactiveDefinitionError
	if err == nil || !errors.As(err, &inactive) {
		t.Fatalf("inactive LaunchDefinition error = %v, want *InactiveDefinitionError", err)
	}
}
