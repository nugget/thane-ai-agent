package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func (a *App) buildLoopDefinitionBaseSpecs() ([]looppkg.Spec, error) {
	// Core-defined loops come first because first wins: everything below
	// appends only what is not already declared, so a definition living
	// in the signed core root takes precedence over the same-named
	// built-in without needing a rule of its own.
	// Wrapped as authored content: a definition that does not parse is
	// fixed by editing the document, never by starting again, so the CLI
	// exits terminally instead of leaving a supervisor to retry the same
	// bytes forever.
	// The core path resolves with the same fallback the validate
	// pre-flight uses: Paths["core"] when App wiring has populated it,
	// the workspace derivation otherwise. The fallback is load-bearing
	// at boot, not just in pre-flight — the definition registry is
	// built before the document roots populate Paths, so reading the
	// map alone means reading an empty string: the loader silently
	// no-ops, and the shipped documents never take effect. Production
	// ran that way undetected for as long as the legacy config enabled
	// flags existed to seed the embedded defaults instead; the day the
	// last flag was removed, all three reflective loops and their
	// container vanished from a healthy-looking boot.
	corePath := strings.TrimSpace(a.cfg.Paths["core"])
	if corePath == "" {
		corePath = strings.TrimSpace(a.cfg.CoreRoot())
	}
	coreDefinitions, err := loadCoreLoopDefinitions(corePath)
	if err != nil {
		return nil, coreAuthoring(fmt.Errorf("core loop definitions: %w", err))
	}
	// Count and source are forensics the next silent absence deserves:
	// a boot that loaded zero core documents should say so where the
	// registry-initialized event is read.
	if a.logger != nil {
		a.logger.Info("core loop definitions loaded",
			"dir", corePath,
			"count", len(coreDefinitions),
		)
	}
	baseDefinitions := append(coreDefinitions, a.cfg.Loops.Definitions...)
	seen := make(map[string]struct{}, len(baseDefinitions))
	for _, def := range baseDefinitions {
		seen[strings.TrimSpace(def.Name)] = struct{}{}
	}
	// Grouping containers first, so member loops resolve their ParentName to
	// a container that's already registered.
	for _, spec := range builtInContainerDefinitionSpecs(a.cfg, seen) {
		baseDefinitions = appendMissingDefinition(baseDefinitions, seen, spec)
	}
	for _, spec := range builtInServiceDefinitionSpecs(a.cfg) {
		baseDefinitions = appendMissingDefinition(baseDefinitions, seen, spec)
	}
	// Core model-facing service loops (ego, metacognitive, archivist)
	// share a parse-cache-emit shape captured by [coreServiceLoops].
	// Parse+cache whenever the loop is enabled OR an operator declared
	// its definition (so an override still gets a config to hydrate
	// against); only auto-append the built-in definition when enabled
	// and not already declared.
	for _, reg := range coreServiceLoops {
		_, hasDefinition := seen[reg.Name]
		enabled := reg.ConfigEnabled(a.cfg)
		if !enabled && !hasDefinition {
			continue
		}
		if err := reg.ParseAndCache(a, a.cfg); err != nil {
			return nil, fmt.Errorf("%s config: %w", reg.Name, err)
		}
		if enabled && !hasDefinition {
			spec, err := reg.DefinitionSpec(a)
			if err != nil {
				return nil, fmt.Errorf("%s definition: %w", reg.Name, err)
			}
			// Parent comes from the document, like everything else about
			// these loops. It used to be assigned here, which meant a
			// definition read from core skipped this line and silently
			// landed at the graph root — the document owned every field
			// but the one saying where the loop belongs.
			baseDefinitions = appendMissingDefinition(baseDefinitions, seen, spec)
		}
	}
	return baseDefinitions, nil
}

func (a *App) hydrateLoopDefinitionSpec(spec looppkg.Spec) (looppkg.Spec, error) {
	if a == nil {
		return spec, nil
	}
	if err := a.validateLoopBindings(spec); err != nil {
		return looppkg.Spec{}, err
	}
	name := strings.TrimSpace(spec.Name)
	// Core model-facing service loops dispatch through their shared
	// registration descriptor (see [coreServiceLoops]); each one's
	// Hydrate closure absorbs its specifics (e.g. metacognitive's
	// resolved state-file Opts) so this site stays uniform.
	if reg, ok := coreServiceLoopByName[name]; ok {
		runtimeSpec, err := reg.Hydrate(a, spec)
		if err != nil {
			return looppkg.Spec{}, err
		}
		return a.hydrateLoopOutputs(runtimeSpec)
	}
	switch name {
	case unifiPollerDefinitionName:
		if a.unifiPoller == nil {
			return looppkg.Spec{}, fmt.Errorf("%s definition requires UniFi poller runtime", unifiPollerDefinitionName)
		}
		spec.Handler = func(ctx context.Context, _ any) error {
			return a.unifiPoller.Poll(ctx)
		}
		return a.hydrateLoopOutputs(spec)
	case haStateWatcherDefinitionName:
		if a.haStateWatcher == nil {
			return looppkg.Spec{}, fmt.Errorf("%s definition requires Home Assistant state watcher runtime", haStateWatcherDefinitionName)
		}
		return a.hydrateLoopOutputs(hydrateHAStateWatcherSpec(spec, a.haStateWatcher))
	case emailPollerDefinitionName:
		if a.emailPoller == nil {
			return looppkg.Spec{}, fmt.Errorf("%s definition requires email poller runtime", emailPollerDefinitionName)
		}
		spec.Handler = func(ctx context.Context, _ any) error {
			wakes, err := a.emailPoller.CheckNewMessages(ctx)
			if err != nil {
				return err
			}
			if wakes == 0 {
				return looppkg.ErrNoOp
			}
			return nil
		}
		return a.hydrateLoopOutputs(spec)
	case forgeSubPollerDefinitionName:
		if a.forgeService == nil || !a.forgeService.SubscriptionPollingEnabled() {
			return looppkg.Spec{}, fmt.Errorf("%s definition requires forge subscription poller runtime", forgeSubPollerDefinitionName)
		}
		spec.Handler = func(ctx context.Context, _ any) error {
			wakes, err := a.forgeService.CheckSubscriptions(ctx)
			if err != nil {
				return err
			}
			if wakes == 0 {
				return looppkg.ErrNoOp
			}
			return nil
		}
		return a.hydrateLoopOutputs(spec)
	case mediaFeedPollerDefinitionName:
		if a.mediaFeedPoller == nil {
			return looppkg.Spec{}, fmt.Errorf("%s definition requires media feed poller runtime", mediaFeedPollerDefinitionName)
		}
		spec.Handler = func(ctx context.Context, _ any) error {
			wakes, err := a.mediaFeedPoller.CheckFeeds(ctx)
			if err != nil {
				return err
			}
			if wakes == 0 {
				return looppkg.ErrNoOp
			}
			return nil
		}
		return a.hydrateLoopOutputs(spec)
	case mqttPublisherDefinitionName:
		if a.mqttPub == nil {
			return looppkg.Spec{}, fmt.Errorf("%s definition requires MQTT publisher runtime", mqttPublisherDefinitionName)
		}
		spec.Handler = func(ctx context.Context, _ any) error {
			a.mqttPub.PublishStates(ctx)
			return nil
		}
		return a.hydrateLoopOutputs(spec)
	case telemetryDefinitionName:
		if a.telemetryPublisher == nil {
			return looppkg.Spec{}, fmt.Errorf("%s definition requires telemetry publisher runtime", telemetryDefinitionName)
		}
		spec.Handler = func(ctx context.Context, _ any) error {
			return a.telemetryPublisher.Publish(ctx)
		}
		return a.hydrateLoopOutputs(spec)
	default:
		return a.hydrateLoopOutputs(spec)
	}
}

func hydrateHAStateWatcherSpec(spec looppkg.Spec, watcher *homeassistant.StateWatcher) looppkg.Spec {
	const haCleanupInterval = 5 * time.Minute
	const haBatchWindow = 1 * time.Second
	const haBatchMax = 100

	haEvents := watcher.Events()
	lastCleanup := time.Now()
	spec.WaitFunc = func(wCtx context.Context) (any, error) {
		cleanupTimer := time.NewTimer(haCleanupInterval)
		defer cleanupTimer.Stop()

		var batch []homeassistant.Event

		select {
		case <-wCtx.Done():
			return nil, wCtx.Err()
		case ev, ok := <-haEvents:
			if !ok {
				return nil, context.Canceled
			}
			batch = append(batch, ev)
		case <-cleanupTimer.C:
			watcher.CleanupRateLimiter()
			lastCleanup = time.Now()
			return nil, nil
		}

		drainTimer := time.NewTimer(haBatchWindow)
		defer drainTimer.Stop()
	drain:
		for len(batch) < haBatchMax {
			select {
			case <-wCtx.Done():
				break drain
			case ev, ok := <-haEvents:
				if !ok {
					break drain
				}
				batch = append(batch, ev)
			case <-drainTimer.C:
				break drain
			}
		}

		return batch, nil
	}
	spec.Handler = func(ctx context.Context, payload any) error {
		var processed int
		if batch, ok := payload.([]homeassistant.Event); ok {
			for _, ev := range batch {
				if watcher.HandleEvent(ev) {
					processed++
				}
			}
		}
		if time.Since(lastCleanup) > haCleanupInterval {
			watcher.CleanupRateLimiter()
			lastCleanup = time.Now()
		}
		if processed == 0 {
			return looppkg.ErrNoOp
		}
		if summary := looppkg.IterationSummary(ctx); summary != nil {
			summary["events_processed"] = processed
		}
		return nil
	}
	return spec
}

// validateLoopBindings resolves a spec's bindings against live
// configuration. [looppkg.ValidateBindings] has already checked that
// every key is registered and every value non-empty; what it cannot
// know is whether the value names something this site actually has.
//
// The check belongs at hydration because that is the last moment
// before a loop starts running with a boundary that does not resolve.
// A misspelled account or repository root would otherwise surface on the
// first unattended tool call, days after the definition was accepted.
func (a *App) validateLoopBindings(spec looppkg.Spec) error {
	if account, ok := spec.Bindings[looppkg.BindingForgeAccount]; ok {
		if a.forgeService == nil {
			return fmt.Errorf("loop %q binds %s=%q but no forge accounts are configured at this site",
				spec.Name, looppkg.BindingForgeAccount, account)
		}
		// Deliberately unbound. ResolveAccount honors a binding carried by
		// ctx, so threading the caller's context here would redirect the
		// lookup to the caller's account and report a binding refusal for
		// a spec that merely names an account this site does not have —
		// turning a plain configuration error into a confusing one. The
		// question being asked is "does this account exist", not "may this
		// caller use it".
		if _, err := a.forgeService.ResolveAccount(context.Background(), account); err != nil {
			return fmt.Errorf("loop %q binds %s=%q: %w",
				spec.Name, looppkg.BindingForgeAccount, account, err)
		}
	}

	if rootName, ok := spec.Bindings[looppkg.BindingRepositoryRoot]; ok {
		if a.forgeService == nil {
			return fmt.Errorf("loop %q binds %s=%q but no forge repository roots are configured at this site",
				spec.Name, looppkg.BindingRepositoryRoot, rootName)
		}
		if _, exists := a.forgeService.RepositoryRoot(rootName); !exists {
			return fmt.Errorf("loop %q binds %s=%q but no repository subscription exposes that named root",
				spec.Name, looppkg.BindingRepositoryRoot, rootName)
		}
	}
	return nil
}
