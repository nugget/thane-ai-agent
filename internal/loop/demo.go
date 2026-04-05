package loop

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/nugget/thane-ai-agent/internal/events"
)

// SpawnDemoLoops creates simulated loops covering all visual categories,
// parent/child hierarchies, error states, and node churn. Intended for
// dashboard layout iteration without real service dependencies.
func SpawnDemoLoops(ctx context.Context, registry *Registry, eventBus *events.Bus, logger *slog.Logger) error {
	lg := logger.With("subsystem", "demo")
	deps := Deps{Logger: lg, EventBus: eventBus}

	// --- metacognitive (circle) ---
	if _, err := registry.SpawnLoop(ctx, Config{
		Name:         "metacognitive",
		SleepMin:     8 * time.Second,
		SleepMax:     15 * time.Second,
		SleepDefault: 10 * time.Second,
		Handler:      demoHandler(1*time.Second, 3*time.Second, 0),
		Metadata:     map[string]string{"category": "metacognitive"},
	}, deps); err != nil {
		return fmt.Errorf("demo metacognitive: %w", err)
	}

	// --- signal (channel, parent + children) ---
	signalID, err := registry.SpawnLoop(ctx, Config{
		Name:    "signal",
		Handler: func(context.Context, any) error { return nil },
		Metadata: map[string]string{
			"subsystem": "signal",
			"category":  "channel",
		},
	}, deps)
	if err != nil {
		return fmt.Errorf("demo signal parent: %w", err)
	}

	for _, child := range []struct {
		name    string
		minWait time.Duration
		maxWait time.Duration
	}{
		{"signal/Alice", 4 * time.Second, 12 * time.Second},
		{"signal/Bob", 6 * time.Second, 20 * time.Second},
	} {
		ch := make(chan struct{}, 1)
		name := child.name
		minW, maxW := child.minWait, child.maxWait
		if _, err := registry.SpawnLoop(ctx, Config{
			Name:     name,
			ParentID: signalID,
			WaitFunc: func(wCtx context.Context) (any, error) {
				// Simulate waiting for an inbound message.
				delay := minW + time.Duration(rand.Int64N(int64(maxW-minW)))
				select {
				case <-wCtx.Done():
					return nil, wCtx.Err()
				case <-time.After(delay):
					return struct{}{}, nil
				case <-ch:
					return struct{}{}, nil
				}
			},
			Handler:  demoHandler(800*time.Millisecond, 2*time.Second, 0),
			Metadata: map[string]string{"subsystem": "signal", "category": "channel"},
		}, deps); err != nil {
			return fmt.Errorf("demo %s: %w", name, err)
		}
	}

	// --- owu (channel, parent + child) ---
	owuID, err := registry.SpawnLoop(ctx, Config{
		Name:    "owu",
		Handler: func(context.Context, any) error { return nil },
		Metadata: map[string]string{
			"subsystem": "owu",
			"category":  "channel",
		},
	}, deps)
	if err != nil {
		return fmt.Errorf("demo owu parent: %w", err)
	}

	owuCh := make(chan struct{}, 1)
	if _, err := registry.SpawnLoop(ctx, Config{
		Name:     "owu/What is the weather t…",
		ParentID: owuID,
		WaitFunc: func(wCtx context.Context) (any, error) {
			delay := 5*time.Second + time.Duration(rand.Int64N(int64(15*time.Second)))
			select {
			case <-wCtx.Done():
				return nil, wCtx.Err()
			case <-time.After(delay):
				return struct{}{}, nil
			case <-owuCh:
				return struct{}{}, nil
			}
		},
		Handler:  demoHandler(1*time.Second, 3*time.Second, 0),
		Metadata: map[string]string{"subsystem": "owu", "category": "channel"},
	}, deps); err != nil {
		return fmt.Errorf("demo owu child: %w", err)
	}

	// --- email-poller (generic, octagon) ---
	if _, err := registry.SpawnLoop(ctx, Config{
		Name:         "email-poller",
		SleepMin:     5 * time.Second,
		SleepMax:     8 * time.Second,
		SleepDefault: 6 * time.Second,
		Handler:      demoHandler(300*time.Millisecond, 800*time.Millisecond, 0),
		Metadata:     map[string]string{"category": "generic"},
	}, deps); err != nil {
		return fmt.Errorf("demo email-poller: %w", err)
	}

	// --- media-feed-poller (generic, octagon) ---
	if _, err := registry.SpawnLoop(ctx, Config{
		Name:         "media-feed-poller",
		SleepMin:     10 * time.Second,
		SleepMax:     20 * time.Second,
		SleepDefault: 15 * time.Second,
		Handler:      demoHandler(500*time.Millisecond, 1500*time.Millisecond, 0),
		Metadata:     map[string]string{"category": "generic"},
	}, deps); err != nil {
		return fmt.Errorf("demo media-feed-poller: %w", err)
	}

	// --- scheduled-digest (scheduled, hexagon, occasional errors) ---
	if _, err := registry.SpawnLoop(ctx, Config{
		Name:         "scheduled-digest",
		SleepMin:     12 * time.Second,
		SleepMax:     25 * time.Second,
		SleepDefault: 18 * time.Second,
		Handler:      demoHandler(1*time.Second, 2*time.Second, 0.2),
		Metadata:     map[string]string{"category": "scheduled"},
	}, deps); err != nil {
		return fmt.Errorf("demo scheduled-digest: %w", err)
	}

	// --- delegate-research (delegate, diamond, finite + respawn) ---
	spawnDelegate := func() {
		if _, err := registry.SpawnLoop(ctx, Config{
			Name:         "delegate-research",
			SleepMin:     6 * time.Second,
			SleepMax:     12 * time.Second,
			SleepDefault: 8 * time.Second,
			MaxIter:      3,
			Handler:      demoHandler(1*time.Second, 2500*time.Millisecond, 0),
			Metadata:     map[string]string{"category": "delegate"},
		}, deps); err != nil {
			lg.Warn("demo delegate-research respawn failed", "error", err)
		}
	}
	spawnDelegate()

	// Respawn delegate after it completes and a cooldown.
	go func() {
		for {
			l := registry.GetByName("delegate-research")
			if l != nil {
				<-l.Done()
			}
			// Cooldown before respawn.
			delay := 10*time.Second + time.Duration(rand.Int64N(int64(20*time.Second)))
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
				spawnDelegate()
			}
		}
	}()

	lg.Info("demo loops spawned", "count", len(registry.List()))
	return nil
}

// demoHandler returns a loop handler that simulates work by sleeping a
// random duration, emits fake progress events for dashboard live cards,
// and returns an error with the given probability.
func demoHandler(minWork, maxWork time.Duration, errRate float64) func(context.Context, any) error {
	return func(ctx context.Context, _ any) error {
		progressFn := ProgressFunc(ctx)

		// Fake LLM start.
		if progressFn != nil {
			progressFn(events.KindLoopLLMStart, map[string]any{
				"model": "demo-model",
			})
		}

		// Simulate LLM thinking.
		workDur := minWork + time.Duration(rand.Int64N(int64(maxWork-minWork)))
		half := workDur / 2

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(half):
		}

		// Fake tool call mid-iteration.
		if progressFn != nil {
			tools := []string{"web_search", "shell_exec", "get_state", "send_message", "knowledge_search"}
			tool := tools[rand.IntN(len(tools))]
			progressFn(events.KindLoopToolStart, map[string]any{
				"tool": tool,
			})
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(workDur - half):
		}

		// Fake tool done + LLM response.
		if progressFn != nil {
			progressFn(events.KindLoopToolDone, map[string]any{
				"tool": "web_search",
			})
			progressFn(events.KindLoopLLMResponse, map[string]any{
				"model":         "demo-model",
				"input_tokens":  rand.IntN(4000) + 500,
				"output_tokens": rand.IntN(800) + 100,
			})
		}

		// Occasional error.
		if errRate > 0 && rand.Float64() < errRate {
			return fmt.Errorf("simulated transient error")
		}

		return nil
	}
}
