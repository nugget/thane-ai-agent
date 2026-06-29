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
	baseDefinitions := append([]looppkg.Spec(nil), a.cfg.Loops.Definitions...)
	seen := make(map[string]struct{}, len(baseDefinitions))
	for _, def := range baseDefinitions {
		seen[strings.TrimSpace(def.Name)] = struct{}{}
	}
	// Grouping containers first, so member loops resolve their ParentName to
	// a container that's already registered.
	for _, spec := range builtInContainerDefinitionSpecs(a.cfg) {
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
			spec := reg.DefinitionSpec(a)
			// The core cognition loops (ego, metacognitive, archivist) live
			// under the cognition container.
			spec.ParentName = cognitionContainerName
			baseDefinitions = appendMissingDefinition(baseDefinitions, seen, spec)
		}
	}
	return baseDefinitions, nil
}

func (a *App) hydrateLoopDefinitionSpec(spec looppkg.Spec) (looppkg.Spec, error) {
	if a == nil {
		return spec, nil
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
		if a.forgeSubPoller == nil {
			return looppkg.Spec{}, fmt.Errorf("%s definition requires forge subscription poller runtime", forgeSubPollerDefinitionName)
		}
		spec.Handler = func(ctx context.Context, _ any) error {
			wakes, err := a.forgeSubPoller.CheckSubscriptions(ctx)
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
