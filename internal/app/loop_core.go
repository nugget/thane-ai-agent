package app

import (
	"context"
	"errors"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// ensureCoreLoop spawns the singleton core loop — a container with
// the well-known name [looppkg.CoreLoopName] — into the live
// registry when one isn't already present. Idempotent: runs once
// per startup and again is a no-op as long as the existing core
// stays registered.
//
// The core is implicit — it does not live in the definition
// registry. The bootstrap owns its lifecycle entirely. That keeps
// the persisted spec surface free of a "what's my core's spec"
// question that has no satisfying answer (operators don't curate
// the root) and avoids the "core got out of sync with code
// expectations" failure mode a persisted spec would introduce.
//
// Core is functionally a container with a few extras (singleton
// enforcement, default-parent target for orphans, refused for
// delete). The "magic container" mental model lives in code as
// `Operation == OperationContainer && Name == CoreLoopName`,
// captured by [looppkg.Loop.IsCore].
func (a *App) ensureCoreLoop(ctx context.Context) error {
	if a == nil || a.loopRegistry == nil {
		return nil
	}
	if existing := a.loopRegistry.Core(); existing != nil {
		// Steady-state path: core is already up. Nothing to do.
		return nil
	}

	spec := looppkg.Spec{
		Name:      looppkg.CoreLoopName,
		Enabled:   true,
		Operation: looppkg.OperationContainer,
	}
	if _, err := a.loopRegistry.SpawnSpec(ctx, spec, looppkg.Deps{
		Logger: a.logger,
	}); err != nil {
		// MultipleCoreError shouldn't happen — we just checked
		// Core() == nil above — but if a race did slip through,
		// treat it as the "already exists" no-op shape.
		var dupe *looppkg.MultipleCoreError
		if errors.As(err, &dupe) {
			return nil
		}
		return err
	}
	a.logger.Info("core loop auto-created", "name", looppkg.CoreLoopName)
	return nil
}

// ensureChannelsContainer spawns the channels grouping container
// ([looppkg.ChannelsContainerName]) as an inert implicit container under core,
// mirroring [App.ensureCoreLoop]. It must run before the channel integrations
// spawn their dynamically-created channel loops: there is no late-reattach
// path, so a child that registers before its parent is live stays parentless.
// Idempotent — a no-op once it exists.
func (a *App) ensureChannelsContainer(ctx context.Context) error {
	if a == nil || a.loopRegistry == nil {
		return nil
	}
	if a.loopRegistry.GetByName(looppkg.ChannelsContainerName) != nil {
		return nil
	}
	spec := looppkg.Spec{
		Name:      looppkg.ChannelsContainerName,
		Enabled:   true,
		Operation: looppkg.OperationContainer,
		Metadata:  map[string]string{"intent": "Interactive counterparty channels (Signal, OWU, and others)."},
	}
	if _, err := a.loopRegistry.SpawnSpec(ctx, spec, looppkg.Deps{Logger: a.logger}); err != nil {
		return err
	}
	a.logger.Info("channels container auto-created", "name", looppkg.ChannelsContainerName)
	return nil
}
