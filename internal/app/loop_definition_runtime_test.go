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
		now:         time.Now,
		scheduleCh:  make(chan struct{}, 1),
	}

	result, err := runtime.StartEnabledServices(context.Background())
	if err != nil {
		t.Fatalf("StartEnabledServices: %v", err)
	}
	if result.Started != 1 || result.SkippedInactive != 1 || result.SkippedPaused != 0 || result.SkippedNonService != 1 || result.SkippedExisting != 0 {
		t.Fatalf("result = %+v, want started=1 inactive=1 paused=0 non_service=1 existing=0", result)
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
		now:         time.Now,
		scheduleCh:  make(chan struct{}, 1),
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
		now:         time.Now,
		scheduleCh:  make(chan struct{}, 1),
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

func TestLoopDefinitionRuntimeReconcileDefinitionServiceSurvivesRequestContext(t *testing.T) {
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
		now:         time.Now,
		scheduleCh:  make(chan struct{}, 1),
	}

	requestCtx, cancel := context.WithCancel(context.Background())
	if err := runtime.ReconcileDefinition(requestCtx, "office_watch"); err != nil {
		t.Fatalf("ReconcileDefinition(active): %v", err)
	}
	cancel()
	time.Sleep(10 * time.Millisecond)

	if loops.GetByName("office_watch") == nil {
		t.Fatal("expected office_watch to keep running after request context cancellation")
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
			Enabled:    true,
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
		now:         time.Now,
		scheduleCh:  make(chan struct{}, 1),
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

	if err := registry.ApplyPolicy("paused_task", looppkg.DefinitionPolicy{
		State: looppkg.DefinitionPolicyStatePaused,
	}, time.Now()); err != nil {
		t.Fatalf("ApplyPolicy(paused): %v", err)
	}

	_, err = runtime.LaunchDefinition(context.Background(), "paused_task", looppkg.Launch{
		CompletionConversationID: "conv-1",
	})
	var paused *looppkg.PausedDefinitionError
	if err == nil || !errors.As(err, &paused) {
		t.Fatalf("paused LaunchDefinition error = %v, want *PausedDefinitionError", err)
	}
}

func TestLoopDefinitionRuntimeSnapshotIncludesRunningLoop(t *testing.T) {
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
		now:         time.Now,
		scheduleCh:  make(chan struct{}, 1),
	}
	if err := runtime.ReconcileDefinition(context.Background(), "office_watch"); err != nil {
		t.Fatalf("ReconcileDefinition: %v", err)
	}

	view := runtime.Snapshot()
	if view == nil {
		t.Fatal("Snapshot returned nil")
	}
	if view.RunningDefinitions != 1 {
		t.Fatalf("RunningDefinitions = %d, want 1", view.RunningDefinitions)
	}
	if len(view.Definitions) != 1 || !view.Definitions[0].Runtime.Running {
		t.Fatalf("Definitions = %+v, want one running definition", view.Definitions)
	}
}

func TestLoopDefinitionRuntimeStartEnabledServicesSkipsIneligibleDefinition(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 7, 2, 0, 0, 0, time.UTC) // Monday 21:00 CDT
	registry, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{
		{
			Name:       "night_watch",
			Enabled:    true,
			Task:       "Watch overnight.",
			Operation:  looppkg.OperationService,
			Completion: looppkg.CompletionNone,
			Conditions: looppkg.Conditions{
				Schedule: &looppkg.ScheduleCondition{
					Timezone: "America/Chicago",
					Windows: []looppkg.ScheduleWindow{{
						Days:  []string{"mon"},
						Start: "09:00",
						End:   "17:00",
					}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	runtime := &loopDefinitionRuntime{
		definitions: registry,
		loops:       looppkg.NewRegistry(),
		runner:      testLoopRunner{},
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:         func() time.Time { return now },
		scheduleCh:  make(chan struct{}, 1),
	}

	result, err := runtime.StartEnabledServices(context.Background())
	if err != nil {
		t.Fatalf("StartEnabledServices: %v", err)
	}
	if result.Started != 0 || result.SkippedIneligible != 1 {
		t.Fatalf("result = %+v, want started=0 skipped_ineligible=1", result)
	}
}

func TestLoopDefinitionRuntimeReconcileDefinitionStopsWhenConditionsNoLongerMatch(t *testing.T) {
	t.Parallel()

	current := time.Date(2026, 4, 6, 15, 0, 0, 0, time.UTC) // Monday 10:00 CDT
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
			Conditions: looppkg.Conditions{
				Schedule: &looppkg.ScheduleCondition{
					Timezone: "America/Chicago",
					Windows: []looppkg.ScheduleWindow{{
						Days:  []string{"mon"},
						Start: "09:00",
						End:   "17:00",
					}},
				},
			},
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
		now:         func() time.Time { return current },
		scheduleCh:  make(chan struct{}, 1),
	}

	if err := runtime.ReconcileDefinition(context.Background(), "office_watch"); err != nil {
		t.Fatalf("ReconcileDefinition(active): %v", err)
	}
	if loops.GetByName("office_watch") == nil {
		t.Fatal("expected office_watch to be running while eligible")
	}

	current = time.Date(2026, 4, 6, 23, 30, 0, 0, time.UTC) // Monday 18:30 CDT
	if err := runtime.ReconcileDefinition(context.Background(), "office_watch"); err != nil {
		t.Fatalf("ReconcileDefinition(ineligible): %v", err)
	}
	if loops.GetByName("office_watch") != nil {
		t.Fatal("expected office_watch to stop after leaving its schedule window")
	}
}

func TestLoopDefinitionRuntimeLaunchDefinitionRejectsIneligibleDefinition(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 6, 23, 30, 0, 0, time.UTC) // Monday 18:30 CDT
	registry, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{
		{
			Name:       "day_shift",
			Enabled:    true,
			Task:       "Handle one request.",
			Operation:  looppkg.OperationRequestReply,
			Completion: looppkg.CompletionReturn,
			Conditions: looppkg.Conditions{
				Schedule: &looppkg.ScheduleCondition{
					Timezone: "America/Chicago",
					Windows: []looppkg.ScheduleWindow{{
						Days:  []string{"mon"},
						Start: "09:00",
						End:   "17:00",
					}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}

	runtime := &loopDefinitionRuntime{
		definitions: registry,
		loops:       looppkg.NewRegistry(),
		runner:      testLoopRunner{},
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:         func() time.Time { return now },
		scheduleCh:  make(chan struct{}, 1),
	}

	_, err = runtime.LaunchDefinition(context.Background(), "day_shift", looppkg.Launch{})
	var ineligible *looppkg.IneligibleDefinitionError
	if err == nil || !errors.As(err, &ineligible) {
		t.Fatalf("LaunchDefinition error = %v, want *IneligibleDefinitionError", err)
	}
}
