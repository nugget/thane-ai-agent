package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/events"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
)

// loopDefinitionBootstrapResult summarizes one startup reconciliation
// pass from durable loop definitions into live loop instances.
type loopDefinitionBootstrapResult struct {
	Started           int `json:"started"`
	SkippedDisabled   int `json:"skipped_disabled"`
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
	logger      *slog.Logger
	eventBus    *events.Bus
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

	deps := looppkg.Deps{
		Runner:   r.runner,
		Logger:   logger,
		EventBus: r.eventBus,
	}

	for _, def := range snap.Definitions {
		spec := def.Spec
		switch {
		case spec.Operation != looppkg.OperationService:
			result.SkippedNonService++
			continue
		case !spec.Enabled:
			result.SkippedDisabled++
			continue
		case r.loops.GetByName(spec.Name) != nil:
			result.SkippedExisting++
			logger.Debug("skipping loop definition bootstrap for existing loop", "name", spec.Name)
			continue
		}

		if _, err := r.loops.SpawnSpec(ctx, spec, deps); err != nil {
			return result, fmt.Errorf("spawn loop definition %q: %w", spec.Name, err)
		}
		result.Started++
	}

	return result, nil
}
