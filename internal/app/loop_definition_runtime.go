package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/events"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
)

// loopDefinitionBootstrapResult summarizes one startup reconciliation
// pass from durable loop definitions into live loop instances.
type loopDefinitionBootstrapResult struct {
	Started           int `json:"started"`
	SkippedInactive   int `json:"skipped_inactive"`
	SkippedPaused     int `json:"skipped_paused"`
	SkippedExisting   int `json:"skipped_existing"`
	SkippedNonService int `json:"skipped_non_service"`
}

// loopDefinitionRuntime bridges durable loop definitions into the live
// loop registry. It intentionally owns only startup/runtime plumbing;
// the definition registry remains the source of truth for stored specs.
type loopDefinitionRuntime struct {
	definitions *looppkg.DefinitionRegistry
	loops       *looppkg.Registry
	runner      looppkg.Runner
	completion  looppkg.CompletionSink
	logger      *slog.Logger
	eventBus    *events.Bus
}

func newAppLoopDefinitionRuntime(a *App) *loopDefinitionRuntime {
	if a == nil || a.loopDefinitionRegistry == nil || a.loopRegistry == nil || a.loop == nil {
		return nil
	}
	injector := &conversationSystemInjector{mem: a.mem, archiver: a.archiveAdapter}
	dispatcher := newDetachedLoopCompletionDispatcher(injector)
	return &loopDefinitionRuntime{
		definitions: a.loopDefinitionRegistry,
		loops:       a.loopRegistry,
		runner:      &loopAdapter{agentLoop: a.loop, router: a.rtr},
		completion:  dispatcher.Deliver,
		logger:      a.logger,
		eventBus:    a.eventBus,
	}
}

func (r *loopDefinitionRuntime) deps() looppkg.Deps {
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	return looppkg.Deps{
		Runner:         r.runner,
		CompletionSink: r.completion,
		Logger:         logger,
		EventBus:       r.eventBus,
	}
}

func (r *loopDefinitionRuntime) definition(name string) (looppkg.DefinitionSnapshot, bool) {
	snap := r.definitions.Snapshot()
	if snap == nil {
		return looppkg.DefinitionSnapshot{}, false
	}
	return findLoopDefinitionByName(snap, name)
}

func (r *loopDefinitionRuntime) runtimeStatusByName() map[string]looppkg.DefinitionRuntimeStatus {
	if r == nil || r.loops == nil {
		return nil
	}
	statuses := make(map[string]looppkg.DefinitionRuntimeStatus)
	for _, l := range r.loops.List() {
		st := l.Status()
		statuses[st.Name] = looppkg.DefinitionRuntimeStatus{
			Running:    true,
			LoopID:     st.ID,
			State:      st.State,
			StartedAt:  st.StartedAt,
			LastWakeAt: st.LastWakeAt,
			Iterations: st.Iterations,
			Attempts:   st.Attempts,
			LastError:  st.LastError,
		}
	}
	return statuses
}

func (r *loopDefinitionRuntime) Snapshot() *looppkg.DefinitionRegistryView {
	if r == nil || r.definitions == nil {
		return nil
	}
	return looppkg.BuildDefinitionRegistryView(r.definitions.Snapshot(), r.runtimeStatusByName())
}

// StartEnabledServices starts durable service definitions that are
// currently enabled and not already present in the live loop registry.
// It relies on the loop engine's own initial jittered sleep to stagger
// first iterations after restart rather than introducing a second
// bootstrap delay layer here.
func (r *loopDefinitionRuntime) StartEnabledServices(ctx context.Context) (loopDefinitionBootstrapResult, error) {
	if r == nil || r.definitions == nil || r.loops == nil {
		return loopDefinitionBootstrapResult{}, nil
	}
	if r.runner == nil {
		return loopDefinitionBootstrapResult{}, fmt.Errorf("loop definition runtime requires a runner")
	}

	snap := r.definitions.Snapshot()
	if snap == nil || len(snap.Definitions) == 0 {
		return loopDefinitionBootstrapResult{}, nil
	}

	result := loopDefinitionBootstrapResult{}
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	for _, def := range snap.Definitions {
		spec := def.Spec
		switch {
		case spec.Operation != looppkg.OperationService:
			result.SkippedNonService++
			continue
		case def.PolicyState == looppkg.DefinitionPolicyStateInactive:
			result.SkippedInactive++
			continue
		case def.PolicyState == looppkg.DefinitionPolicyStatePaused:
			result.SkippedPaused++
			continue
		case r.loops.GetByName(spec.Name) != nil:
			result.SkippedExisting++
			logger.Debug("skipping loop definition bootstrap for existing loop", "name", spec.Name)
			continue
		}

		if _, err := r.loops.SpawnSpec(ctx, spec, r.deps()); err != nil {
			return result, fmt.Errorf("spawn loop definition %q: %w", spec.Name, err)
		}
		result.Started++
	}

	return result, nil
}

// ReconcileDefinition applies the current effective definition state to
// the live loop registry. Active service definitions are started when
// absent; inactive or non-service definitions stop any same-named live
// loop so runtime state follows the stored contract.
func (r *loopDefinitionRuntime) ReconcileDefinition(ctx context.Context, name string) error {
	if r == nil || r.loops == nil || r.definitions == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	def, found := r.definition(name)
	existing := r.loops.GetByName(name)
	if !found {
		if existing != nil {
			existing.Stop()
		}
		return nil
	}
	if def.Spec.Operation != looppkg.OperationService || def.PolicyState != looppkg.DefinitionPolicyStateActive {
		if existing != nil {
			existing.Stop()
		}
		return nil
	}
	if existing != nil {
		return nil
	}
	_, err := r.loops.SpawnSpec(ctx, def.Spec, r.deps())
	return err
}

// LaunchDefinition launches one stored loop definition using the
// current effective snapshot and optional launch overrides.
func (r *loopDefinitionRuntime) LaunchDefinition(ctx context.Context, name string, launch looppkg.Launch) (looppkg.LaunchResult, error) {
	if r == nil || r.definitions == nil || r.loops == nil {
		return looppkg.LaunchResult{}, fmt.Errorf("loop definition runtime is not configured")
	}
	if r.runner == nil {
		return looppkg.LaunchResult{}, fmt.Errorf("loop definition runtime requires a runner")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return looppkg.LaunchResult{}, fmt.Errorf("definition name is required")
	}
	def, found := r.definition(name)
	if !found {
		return looppkg.LaunchResult{}, &looppkg.UnknownDefinitionError{Name: name}
	}
	switch def.PolicyState {
	case looppkg.DefinitionPolicyStateInactive:
		return looppkg.LaunchResult{}, &looppkg.InactiveDefinitionError{Name: name}
	case looppkg.DefinitionPolicyStatePaused:
		return looppkg.LaunchResult{}, &looppkg.PausedDefinitionError{Name: name}
	}
	if def.Spec.Operation == looppkg.OperationService {
		if existing := r.loops.GetByName(name); existing != nil {
			return looppkg.LaunchResult{
				LoopID:    existing.ID(),
				Operation: looppkg.OperationService,
				Detached:  true,
			}, nil
		}
	}

	launch.Spec = def.Spec
	return r.loops.Launch(ctx, launch, r.deps())
}

func findLoopDefinitionByName(snapshot *looppkg.DefinitionRegistrySnapshot, name string) (looppkg.DefinitionSnapshot, bool) {
	if snapshot == nil {
		return looppkg.DefinitionSnapshot{}, false
	}
	for _, def := range snapshot.Definitions {
		if def.Name == name {
			return def, true
		}
	}
	return looppkg.DefinitionSnapshot{}, false
}

func (a *App) reconcileLoopDefinition(ctx context.Context, name string) error {
	if a == nil || a.loopDefinitionRuntime == nil {
		return nil
	}
	return a.loopDefinitionRuntime.ReconcileDefinition(ctx, name)
}

func (a *App) launchLoopDefinition(ctx context.Context, name string, launch looppkg.Launch) (looppkg.LaunchResult, error) {
	if a == nil || a.loopDefinitionRuntime == nil {
		return looppkg.LaunchResult{}, fmt.Errorf("loop definition runtime is not configured")
	}
	return a.loopDefinitionRuntime.LaunchDefinition(ctx, name, launch)
}

func (a *App) loopDefinitionView() *looppkg.DefinitionRegistryView {
	if a == nil {
		return nil
	}
	if a.loopDefinitionRuntime != nil {
		return a.loopDefinitionRuntime.Snapshot()
	}
	if a.loopDefinitionRegistry == nil {
		return nil
	}
	return looppkg.BuildDefinitionRegistryView(a.loopDefinitionRegistry.Snapshot(), nil)
}
