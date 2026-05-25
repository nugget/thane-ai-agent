package app

import (
	"context"
	"errors"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// defaultCoreLoopName is the well-known name the bootstrap uses for
// the auto-created core. Operators can read it back through the live
// registry; the UI will hang system-level affordances off this node
// in PR-D2. Kept as a constant so log lines, tests, and any future
// "core by name" lookups agree.
const defaultCoreLoopName = "core"

// ensureCoreLoop spawns the singleton [looppkg.OperationCore] loop
// into the live registry when one isn't already present. Idempotent:
// runs once per startup and again is a no-op as long as the existing
// core stays registered.
//
// The core is implicit — it does not live in the definition
// registry. The bootstrap owns its lifecycle entirely. That keeps
// the persisted spec surface free of a "what's my core's spec"
// question that has no satisfying answer (operators don't curate
// the root) and avoids the "core got out of sync with code
// expectations" failure mode a persisted spec would introduce.
func (a *App) ensureCoreLoop(ctx context.Context) error {
	if a == nil || a.loopRegistry == nil {
		return nil
	}
	if existing := a.loopRegistry.Core(); existing != nil {
		// Steady-state path: core is already up. Nothing to do.
		return nil
	}

	spec := looppkg.Spec{
		Name:      defaultCoreLoopName,
		Enabled:   true,
		Operation: looppkg.OperationCore,
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
	a.logger.Info("core loop auto-created", "name", defaultCoreLoopName)
	return nil
}
