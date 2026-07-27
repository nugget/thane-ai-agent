package tools

import (
	"context"
	"fmt"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// ensureDefinitionMutable refuses a mutation that would overwrite a
// config-owned loop definition, and returns the existing definition when
// one is present so callers can apply their own additional policy.
//
// Config ownership is an invariant of the definition registry rather than
// of any one tool, so it gets one implementation. It previously had three
// — once in loop_definition_set and once in each of the guided create
// paths — which is the arrangement where one copy eventually stops
// matching the others without anything failing.
//
// A nil snapshot means the registry has nothing to compare against, which
// is not an error here: the caller is creating something new.
func ensureDefinitionMutable(snap *looppkg.DefinitionRegistrySnapshot, name string) (looppkg.DefinitionSnapshot, bool, error) {
	if snap == nil {
		return looppkg.DefinitionSnapshot{}, false, nil
	}
	existing, ok := looppkg.FindDefinition(snap, name)
	if !ok {
		return looppkg.DefinitionSnapshot{}, false, nil
	}
	if existing.Source == looppkg.DefinitionSourceConfig {
		return existing, true, (&looppkg.ImmutableDefinitionError{Name: name})
	}
	return existing, true, nil
}

// requireMutableDefinition is [ensureDefinitionMutable] for callers that
// act on a definition that must already exist — update, reparent, delete,
// launch — where absence is an error rather than the start of a creation.
func requireMutableDefinition(snap *looppkg.DefinitionRegistrySnapshot, name string) (looppkg.DefinitionSnapshot, error) {
	existing, found, err := ensureDefinitionMutable(snap, name)
	if err != nil {
		return looppkg.DefinitionSnapshot{}, err
	}
	if !found {
		return looppkg.DefinitionSnapshot{}, (&looppkg.UnknownDefinitionError{Name: name})
	}
	return existing, nil
}

// commitSpecThroughChokepoint persists a spec through the durable commit
// hook — persist, upsert, reconcile as one step — falling back to a bare
// overlay upsert when no hook is wired, so a registry-only configuration
// still works.
//
// The two callers, loop_definition_set and the guided create path, reach
// the same underlying hook and registry through separate struct fields
// (r.commitLoopDefinitionSpec and loopIntentDeps.CommitSpec are both
// app.commitLoopDefinition). Passing the dependencies in keeps that
// wiring untouched while leaving one implementation of what committing
// means, so the fallback and the ordering cannot diverge between the
// tool a model reaches first and the tool it reaches later.
func commitSpecThroughChokepoint(
	ctx context.Context,
	commit func(context.Context, looppkg.Spec, time.Time) error,
	registry *looppkg.DefinitionRegistry,
	spec looppkg.Spec,
	updatedAt time.Time,
) error {
	if commit != nil {
		return commit(ctx, spec, updatedAt)
	}
	if registry == nil {
		return fmt.Errorf("loop definition registry not configured")
	}
	return registry.Upsert(spec, updatedAt)
}
