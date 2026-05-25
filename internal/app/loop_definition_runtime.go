package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/events"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// loopDefinitionBootstrapResult summarizes one startup reconciliation
// pass from durable loop definitions into live loop instances.
type loopDefinitionBootstrapResult struct {
	Started           int `json:"started"`
	SkippedInactive   int `json:"skipped_inactive"`
	SkippedPaused     int `json:"skipped_paused"`
	SkippedIneligible int `json:"skipped_ineligible"`
	SkippedExisting   int `json:"skipped_existing"`
	SkippedNonService int `json:"skipped_non_service"`
}

// loopDefinitionRuntime bridges durable loop definitions into the live
// loop registry. It intentionally owns only startup/runtime plumbing;
// the definition registry remains the source of truth for stored specs.
type loopDefinitionRuntime struct {
	definitions  *looppkg.DefinitionRegistry
	loops        *looppkg.Registry
	runner       looppkg.Runner
	completion   looppkg.CompletionSink
	hydrate      func(looppkg.Spec) (looppkg.Spec, error)
	logger       *slog.Logger
	eventBus     *events.Bus
	lifecycleCtx context.Context
	now          func() time.Time
	scheduleCh   chan struct{}
}

func newAppLoopDefinitionRuntime(a *App) *loopDefinitionRuntime {
	if a == nil || a.loopDefinitionRegistry == nil || a.loopRegistry == nil || a.loop == nil {
		return nil
	}
	dispatcher := a.ensureLoopCompletionDispatcher()
	return &loopDefinitionRuntime{
		definitions:  a.loopDefinitionRegistry,
		loops:        a.loopRegistry,
		runner:       &loopAdapter{agentLoop: a.loop, router: a.rtr, capSurface: a.capSurfaceGetter()},
		completion:   dispatcher.Deliver,
		hydrate:      a.hydrateLoopDefinitionSpec,
		logger:       a.logger,
		eventBus:     a.eventBus,
		lifecycleCtx: context.Background(),
		now:          time.Now,
		scheduleCh:   make(chan struct{}, 1),
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

func (r *loopDefinitionRuntime) definitionLogger(name string) *slog.Logger {
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	return logger.With("definition_name", name)
}

func (r *loopDefinitionRuntime) runtimeSpec(spec looppkg.Spec) (looppkg.Spec, error) {
	if r == nil {
		return spec, nil
	}
	if r.hydrate != nil {
		var err error
		spec, err = r.hydrate(spec)
		if err != nil {
			return spec, err
		}
	}
	// Resolve parent_name → live ParentID late, after every other
	// hydration has had its say. ParentID survives only as long as the
	// parent's current launch, so the runtime — not the stored spec —
	// is the source of truth here. If the named parent isn't yet
	// registered, ParentID stays empty and the loop lands at the root;
	// container tag inheritance walks the live registry, so the link
	// re-establishes naturally if the parent comes up later in the
	// same startup pass.
	if r.loops != nil && spec.ParentName != "" && spec.ParentID == "" {
		if parent := r.loops.GetByName(spec.ParentName); parent != nil {
			spec.ParentID = parent.ID()
		}
	}
	// Orphan loops attach to the core at registration time —
	// [Registry.Register] owns that default-parenting now so every
	// spawn path (definition hydration, mqtt wake, delegate
	// launches, direct SpawnLoop callers) gets uniform behavior.
	// Doing it here would have left non-definition spawns as
	// additional roots.
	return spec, nil
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
	return looppkg.BuildDefinitionRegistryView(
		r.definitions.Snapshot(),
		r.runtimeStatusByName(),
		looppkg.WithLiveRegistry(r.loops),
	)
}

func (r *loopDefinitionRuntime) nowTime() time.Time {
	if r == nil || r.now == nil {
		return time.Now()
	}
	return r.now()
}

func (r *loopDefinitionRuntime) signalScheduleChange() {
	if r == nil || r.scheduleCh == nil {
		return
	}
	select {
	case r.scheduleCh <- struct{}{}:
	default:
	}
}

func (r *loopDefinitionRuntime) serviceContext() context.Context {
	if r != nil && r.lifecycleCtx != nil {
		return r.lifecycleCtx
	}
	return context.Background()
}

// evaluateConditions runs the cascade-aware eligibility check for
// loopName at the runtime's current clock. Falls back to a
// permanently-eligible status when the definitions registry is
// unwired (test paths) so the check sites that gate on Eligible
// don't accidentally block when the registry is mocked away.
func (r *loopDefinitionRuntime) evaluateConditions(loopName string) looppkg.DefinitionEligibilityStatus {
	if r == nil || r.definitions == nil {
		return looppkg.DefinitionEligibilityStatus{Eligible: true}
	}
	status, _ := r.definitions.EvaluateConditions(loopName, r.nowTime())
	return status
}

func (r *loopDefinitionRuntime) nextScheduleTransition(now time.Time) time.Time {
	if r == nil || r.definitions == nil {
		return time.Time{}
	}
	snap := r.definitions.Snapshot()
	if snap == nil {
		return time.Time{}
	}
	next := time.Time{}
	for _, def := range snap.Definitions {
		if def.Spec.Operation != looppkg.OperationService || def.PolicyState != looppkg.DefinitionPolicyStateActive {
			continue
		}
		eligibility, _ := r.definitions.EvaluateConditions(def.Name, now)
		if eligibility.NextTransitionAt.IsZero() {
			continue
		}
		if next.IsZero() || eligibility.NextTransitionAt.Before(next) {
			next = eligibility.NextTransitionAt
		}
	}
	return next
}

func (r *loopDefinitionRuntime) ReconcileAllDefinitions(ctx context.Context) error {
	if r == nil || r.definitions == nil {
		return nil
	}
	snap := r.definitions.Snapshot()
	if snap == nil {
		return nil
	}
	for _, def := range snap.Definitions {
		if err := r.ReconcileDefinition(ctx, def.Name); err != nil {
			return fmt.Errorf("reconcile %q: %w", def.Name, err)
		}
	}
	return nil
}

func (r *loopDefinitionRuntime) StartScheduleWatcher(ctx context.Context) error {
	if r == nil || r.definitions == nil {
		return nil
	}
	if ctx != nil {
		r.lifecycleCtx = ctx
	}
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}
	if r.scheduleCh == nil {
		r.scheduleCh = make(chan struct{}, 1)
	}
	go func() {
		for {
			next := r.nextScheduleTransition(r.nowTime())
			if next.IsZero() {
				select {
				case <-ctx.Done():
					return
				case <-r.scheduleCh:
					continue
				}
			}

			wait := time.Until(next)
			if wait < time.Second {
				wait = time.Second
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-r.scheduleCh:
				if !timer.Stop() {
					<-timer.C
				}
				continue
			case <-timer.C:
				reconcileCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := r.ReconcileAllDefinitions(reconcileCtx); err != nil {
					logger.Warn("loop definition schedule reconcile failed", "error", err)
				}
				cancel()
			}
		}
	}()
	return nil
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
	r.lifecycleCtx = ctx

	snap := r.definitions.Snapshot()
	if snap == nil || len(snap.Definitions) == 0 {
		return loopDefinitionBootstrapResult{}, nil
	}

	result := loopDefinitionBootstrapResult{}
	logger := r.logger
	if logger == nil {
		logger = slog.Default()
	}

	// Containers must come up before any descendant that references
	// them by parent_name, otherwise the descendant lands at the root
	// with no inheritance. Order them by ancestry depth (roots first,
	// nested second) before spawning. Services come last because they
	// can sit under containers but never the other way around.
	containerSpecs, serviceSpecs := splitContainerSpecs(snap.Definitions, r.nowTime)
	// Count non-durable definitions (request_reply, background_task)
	// up front so the accounting still matches the pre-refactor
	// behavior. Partitioning silently drops them, so we surface them
	// here as SkippedNonService for parity with existing callers and
	// dashboards.
	for _, def := range snap.Definitions {
		if def.Spec.Operation != looppkg.OperationService && def.Spec.Operation != looppkg.OperationContainer {
			result.SkippedNonService++
		}
	}
	for _, spec := range containerSpecs {
		if err := r.bootstrapDefinitionSpawn(ctx, spec, logger, &result); err != nil {
			return result, err
		}
	}
	for _, spec := range serviceSpecs {
		if err := r.bootstrapDefinitionSpawn(ctx, spec, logger, &result); err != nil {
			return result, err
		}
	}

	return result, nil
}

// bootstrapDefinitionSpawn runs the eligibility/policy gating and spawn
// for one definition during startup hydration. Extracted from
// [StartEnabledServices] so the container-first / service-last passes
// share identical decision logic.
func (r *loopDefinitionRuntime) bootstrapDefinitionSpawn(ctx context.Context, def looppkg.DefinitionSnapshot, logger *slog.Logger, result *loopDefinitionBootstrapResult) error {
	spec := def.Spec
	switch {
	case def.PolicyState == looppkg.DefinitionPolicyStateInactive:
		result.SkippedInactive++
		return nil
	case def.PolicyState == looppkg.DefinitionPolicyStatePaused:
		result.SkippedPaused++
		return nil
	case !r.evaluateConditions(def.Name).Eligible:
		result.SkippedIneligible++
		return nil
	case r.loops.GetByName(spec.Name) != nil:
		result.SkippedExisting++
		logger.Debug("skipping loop definition bootstrap for existing loop", "name", spec.Name)
		return nil
	}

	runtimeSpec, err := r.runtimeSpec(spec)
	if err != nil {
		return fmt.Errorf("hydrate loop definition %q: %w", spec.Name, err)
	}
	if _, err := r.loops.SpawnSpec(ctx, runtimeSpec, r.deps()); err != nil {
		return fmt.Errorf("spawn loop definition %q: %w", spec.Name, err)
	}
	result.Started++
	return nil
}

// splitContainerSpecs partitions definitions into container and service
// hydration order. Containers come first, sorted root-first so a
// parent_name reference resolves to a live loop by the time the child
// hydrates. Non-container, non-service operations (request_reply,
// background_task) are dropped — they're transient and shouldn't be
// hydrated at startup at all. nowTime is unused today but threaded
// through so future condition-driven ordering doesn't reshape the API.
func splitContainerSpecs(defs []looppkg.DefinitionSnapshot, _ func() time.Time) (containers, services []looppkg.DefinitionSnapshot) {
	byName := make(map[string]looppkg.DefinitionSnapshot, len(defs))
	for _, def := range defs {
		byName[def.Spec.Name] = def
	}
	for _, def := range defs {
		switch def.Spec.Operation {
		case looppkg.OperationContainer:
			containers = append(containers, def)
		case looppkg.OperationService:
			services = append(services, def)
		}
	}
	// Stable topo sort: containers with no parent first, then those
	// whose parent has already been emitted. Anything left over (orphan
	// parent_name) falls back to the original definition order so we
	// still spawn the container — its tag inheritance will simply
	// resolve to nil until the operator fixes the missing parent.
	emitted := make(map[string]bool, len(containers))
	ordered := make([]looppkg.DefinitionSnapshot, 0, len(containers))
	progressed := true
	remaining := containers
	for progressed && len(remaining) > 0 {
		progressed = false
		next := remaining[:0]
		for _, def := range remaining {
			pname := def.Spec.ParentName
			if pname == "" || emitted[pname] || byName[pname].Spec.Operation != looppkg.OperationContainer {
				ordered = append(ordered, def)
				emitted[def.Spec.Name] = true
				progressed = true
				continue
			}
			next = append(next, def)
		}
		remaining = next
	}
	ordered = append(ordered, remaining...) // orphans (cycle or missing parent)
	return ordered, services
}

// ReconcileDefinition applies the current effective definition state to
// the live loop registry. Active service definitions are started when
// absent; inactive or non-service definitions stop any same-named live
// loop so runtime state follows the stored contract.
func (r *loopDefinitionRuntime) ReconcileDefinition(ctx context.Context, name string) error {
	if r == nil || r.loops == nil || r.definitions == nil {
		return nil
	}
	defer r.signalScheduleChange()
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	log := r.definitionLogger(name)
	def, found := r.definition(name)
	existing := r.loops.GetByName(name)
	if !found {
		if existing != nil {
			log.Info("stopping loop definition service",
				"reason", "definition_removed",
				"loop_id", existing.ID(),
			)
			return r.loops.StopLoop(existing.ID())
		}
		return nil
	}
	eligibility := r.evaluateConditions(def.Name)
	durable := def.Spec.Operation == looppkg.OperationService || def.Spec.Operation == looppkg.OperationContainer
	if !durable || def.PolicyState != looppkg.DefinitionPolicyStateActive || !eligibility.Eligible {
		if existing != nil {
			reason := "not_durable"
			switch {
			case !durable:
				reason = "non_durable_definition"
			case def.PolicyState == looppkg.DefinitionPolicyStateInactive:
				reason = "policy_inactive"
			case def.PolicyState == looppkg.DefinitionPolicyStatePaused:
				reason = "policy_paused"
			case !eligibility.Eligible:
				reason = "condition_ineligible"
			}
			log.Info("stopping loop definition service",
				"reason", reason,
				"source", def.Source,
				"policy_state", def.PolicyState,
				"eligibility_reason", eligibility.Reason,
				"loop_id", existing.ID(),
			)
			return r.loops.StopLoop(existing.ID())
		}
		return nil
	}
	if existing != nil {
		return nil
	}
	runtimeSpec, err := r.runtimeSpec(def.Spec)
	if err != nil {
		return fmt.Errorf("hydrate loop definition %q: %w", name, err)
	}
	log.Info("starting loop definition service",
		"source", def.Source,
		"policy_state", def.PolicyState,
		"completion", def.Spec.Completion,
	)
	_, err = r.loops.SpawnSpec(r.serviceContext(), runtimeSpec, r.deps())
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
	log := r.definitionLogger(name)
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
	if eligibility := r.evaluateConditions(name); !eligibility.Eligible {
		return looppkg.LaunchResult{}, &looppkg.IneligibleDefinitionError{Name: name, Reason: eligibility.Reason}
	}
	if def.Spec.Operation == looppkg.OperationService || def.Spec.Operation == looppkg.OperationContainer {
		if existing := r.loops.GetByName(name); existing != nil {
			// Loud-fail on caller payload for already-running durable
			// loops (services and containers). The runtime captures
			// requestOverride at launch time and never re-applies it,
			// and the spec-overwrite further down doesn't run on this
			// early-return path — silently returning the existing loop
			// ID would hide both drops from the caller. HasOverrides
			// covers per-launch override fields and inline launch.spec
			// alike.
			if launch.HasOverrides() {
				log.Warn("rejecting launch overrides for running durable definition",
					"loop_id", existing.ID(),
					"operation", def.Spec.Operation,
				)
				return looppkg.LaunchResult{}, &looppkg.RunningDurableLoopOverridesError{Name: name}
			}
			log.Info("using existing running loop definition",
				"loop_id", existing.ID(),
				"operation", def.Spec.Operation,
			)
			return looppkg.LaunchResult{
				LoopID:    existing.ID(),
				Operation: def.Spec.Operation,
				Detached:  true,
			}, nil
		}
	}

	runtimeSpec, err := r.runtimeSpec(def.Spec)
	if err != nil {
		return looppkg.LaunchResult{}, fmt.Errorf("hydrate loop definition %q: %w", name, err)
	}
	launch.Spec = runtimeSpec
	log.Info("launching loop definition",
		"source", def.Source,
		"operation", runtimeSpec.Operation,
		"completion", runtimeSpec.Completion,
		"policy_state", def.PolicyState,
		"conversation_id", launch.ConversationID,
		"completion_conversation_id", launch.CompletionConversationID,
		"completion_channel", looppkg.CloneCompletionChannelTarget(launch.CompletionChannel),
	)
	result, err := r.loops.Launch(ctx, launch, r.deps())
	if err != nil {
		return looppkg.LaunchResult{}, err
	}
	log.Info("loop definition launched",
		"loop_id", result.LoopID,
		"operation", result.Operation,
		"detached", result.Detached,
	)
	return result, nil
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
	return looppkg.BuildDefinitionRegistryView(
		a.loopDefinitionRegistry.Snapshot(),
		nil,
		looppkg.WithLiveRegistry(a.loopRegistry),
	)
}
