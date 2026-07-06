package app

import (
	"errors"
	"fmt"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/awareness"
)

// compileLoopSubscriptions mirrors every loop definition's
// Spec.Subscriptions into the awareness registry as rows owned by the
// loop, then reaps rows whose owner no longer exists. Runs once at
// startup, after the definition registry is hydrated; from then on
// [App.mirrorLoopSubscriptions] keeps the registry current on every
// spec persist.
//
// The orphan sweep is also the conscious burial of the retired
// tag-scoped tier (#1209): legacy scope-tagged rows have no owning
// definition, so they are logged entity-by-entity and deleted rather
// than silently ignored forever.
func (a *App) compileLoopSubscriptions() error {
	if a == nil || a.loopDefinitionRegistry == nil || a.watchlistStore == nil {
		return nil
	}

	known := make(map[string]struct{})
	var errs []error
	if snap := a.loopDefinitionRegistry.Snapshot(); snap != nil {
		for _, def := range snap.Definitions {
			name := strings.TrimSpace(def.Name)
			if name == "" {
				continue
			}
			if name == awareness.OwnerSystem || name == awareness.OwnerCore {
				// Reserved owners: system holds runtime-seeded rows,
				// and core's rows are the always-visible tier whose
				// source of truth is the registry itself (core has no
				// persisted definition by design, #1208). A definition
				// squatting on either name must not clobber them.
				a.logger.Warn("loop definition name collides with a reserved subscription owner — its subscriptions are not mirrored into the registry",
					"name", name)
				continue
			}
			known[name] = struct{}{}
			if err := a.watchlistStore.ReplaceOwner(name, def.Spec.Subscriptions); err != nil {
				errs = append(errs, fmt.Errorf("mirror subscriptions for %q: %w", name, err))
			}
		}
	}

	owners, err := a.watchlistStore.Owners()
	if err != nil {
		errs = append(errs, fmt.Errorf("enumerate subscription owners: %w", err))
		return errors.Join(errs...)
	}
	for _, owner := range owners {
		if owner == awareness.OwnerSystem || owner == awareness.OwnerCore {
			// Reserved owners have no definition behind them by
			// design; their rows are never orphans.
			continue
		}
		if _, ok := known[owner]; ok {
			continue
		}
		rows, err := a.watchlistStore.ListOwner(owner)
		if err != nil {
			errs = append(errs, fmt.Errorf("list orphaned rows for %q: %w", owner, err))
			continue
		}
		entities := make([]string, 0, len(rows))
		for _, row := range rows {
			entities = append(entities, row.EntityID)
		}
		a.logger.Warn("dropping orphaned subscription rows — no loop definition owns them",
			"owner", owner, "entities", entities)
		if err := a.watchlistStore.RemoveAllForOwner(owner); err != nil {
			errs = append(errs, fmt.Errorf("drop orphaned rows for %q: %w", owner, err))
		}
	}
	// The pass may have added or dropped ingest-feeding rows after the
	// state watcher built its initial filter; refresh it once at the end
	// rather than per-definition.
	if a.ingestFilterRebuild != nil {
		a.ingestFilterRebuild()
	}
	return errors.Join(errs...)
}

// mirrorLoopSubscriptions projects one spec's Subscriptions into the
// awareness registry and refreshes the ingestion filter. Best-effort
// by design: the registry rows are a projection of the spec (the spec
// stays the source of truth), and the startup compile pass self-heals
// any miss on the next boot — so a projection failure warns rather
// than failing the spec persist that triggered it.
func (a *App) mirrorLoopSubscriptions(spec looppkg.Spec) {
	if a == nil || a.watchlistStore == nil {
		return
	}
	name := strings.TrimSpace(spec.Name)
	if name == "" || name == awareness.OwnerSystem || name == awareness.OwnerCore {
		return
	}
	if err := a.watchlistStore.ReplaceOwner(name, spec.Subscriptions); err != nil {
		a.logger.Warn("failed to mirror loop subscriptions into the registry",
			"name", name, "error", err)
		return
	}
	if a.ingestFilterRebuild != nil {
		a.ingestFilterRebuild()
	}
}
