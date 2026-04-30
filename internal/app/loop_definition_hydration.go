package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/runtime/ego"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/runtime/metacognitive"
)

func (a *App) buildLoopDefinitionBaseSpecs() ([]looppkg.Spec, error) {
	baseDefinitions := append([]looppkg.Spec(nil), a.cfg.Loops.Definitions...)
	seen := make(map[string]struct{}, len(baseDefinitions))
	for _, def := range baseDefinitions {
		seen[strings.TrimSpace(def.Name)] = struct{}{}
	}
	for _, spec := range builtInServiceDefinitionSpecs(a.cfg) {
		baseDefinitions = appendMissingDefinition(baseDefinitions, seen, spec)
	}
	_, hasMetacogDefinition := seen[metacognitive.DefinitionName]
	if a.cfg.Metacognitive.Enabled || hasMetacogDefinition {
		metacogCfg, err := metacognitive.ParseConfig(a.cfg.Metacognitive)
		if err != nil {
			return nil, fmt.Errorf("metacognitive config: %w", err)
		}
		a.metacogCfg = &metacogCfg
		if a.cfg.Metacognitive.Enabled && !hasMetacogDefinition {
			baseDefinitions = appendMissingDefinition(baseDefinitions, seen, metacognitive.DefinitionSpec(metacogCfg))
		}
	}
	_, hasEgoDefinition := seen[ego.DefinitionName]
	if a.cfg.Ego.Enabled || hasEgoDefinition {
		egoCfg, err := ego.ParseConfig(a.cfg.Ego)
		if err != nil {
			return nil, fmt.Errorf("ego config: %w", err)
		}
		a.egoCfg = &egoCfg
		if a.cfg.Ego.Enabled && !hasEgoDefinition {
			baseDefinitions = appendMissingDefinition(baseDefinitions, seen, ego.DefinitionSpec(egoCfg))
		}
	}
	return baseDefinitions, nil
}

func (a *App) hydrateLoopDefinitionSpec(spec looppkg.Spec) (looppkg.Spec, error) {
	if a == nil {
		return spec, nil
	}
	switch strings.TrimSpace(spec.Name) {
	case metacognitive.DefinitionName:
		if a.metacogCfg == nil {
			return looppkg.Spec{}, fmt.Errorf("metacognitive definition requires metacognitive config")
		}
		stateFileName := filepath.Base(a.metacogCfg.StateFile)
		stateFilePath := coreFilePath(a.cfg.Workspace.Path, stateFileName)
		runtimeSpec := metacognitive.HydrateSpec(spec, *a.metacogCfg, metacognitive.Opts{
			StateFilePath: stateFilePath,
			StateFileName: stateFileName,
		})
		return a.hydrateLoopOutputs(runtimeSpec)
	case ego.DefinitionName:
		if a.egoCfg == nil {
			return looppkg.Spec{}, fmt.Errorf("ego definition requires ego config")
		}
		runtimeSpec := ego.HydrateSpec(spec, *a.egoCfg)
		return a.hydrateLoopOutputs(runtimeSpec)
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
		spec.Handler = emailPollHandler(a.emailPoller, a.loop, a.logger)
		return a.hydrateLoopOutputs(spec)
	case mediaFeedPollerDefinitionName:
		if a.mediaFeedPoller == nil {
			return looppkg.Spec{}, fmt.Errorf("%s definition requires media feed poller runtime", mediaFeedPollerDefinitionName)
		}
		spec.Handler = mediaFeedHandler(a.mediaFeedPoller, a.loop, a.logger)
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
