package app

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/awareness"
)

// migrateLegacyScopeTagSubscriptions moves loop-scoped subscription
// rows from the watchlist store onto Spec.Subscriptions, in place,
// once at startup. Specs persisted before the subscriptions-as-
// attribute migration recorded their owning loop via
// Metadata["scope_tag"] (or the older "focus_tag"), and the
// awareness watchlist table held the entity rows under that scope.
// After the migration the store holds only always-visible rows.
//
// The migration is idempotent: a second run finds no scope_tag
// metadata and no scoped rows, so it is a no-op. We accept partial
// failure — a problematic row is logged but does not abort the
// startup-time migration of every other loop.
func (a *App) migrateLegacyScopeTagSubscriptions() error {
	if a == nil || a.loopDefinitionRegistry == nil || a.watchlistStore == nil {
		return nil
	}
	snap := a.loopDefinitionRegistry.Snapshot()
	if snap == nil || len(snap.Definitions) == 0 {
		return nil
	}

	var errs []error
	migrated := 0
	for _, def := range snap.Definitions {
		legacyTag := legacyScopeTag(def.Spec)
		if legacyTag == "" {
			continue
		}
		if def.Source == looppkg.DefinitionSourceConfig {
			// Config-source specs are immutable from the runtime side;
			// they should not be carrying scope_tag metadata anyway, so
			// log and skip.
			a.logger.Warn("legacy scope_tag on immutable config spec — skipping migration",
				"name", def.Name, "scope_tag", legacyTag)
			continue
		}
		rows, err := a.watchlistStore.ListByTag(legacyTag)
		if err != nil {
			errs = append(errs, fmt.Errorf("read legacy subscriptions for %q (scope=%q): %w", def.Name, legacyTag, err))
			continue
		}

		// Two-step persist so a wipe failure is retryable.
		//
		// Step 1 writes spec.Subscriptions but KEEPS the legacy
		// scope_tag/focus_tag metadata in place. If the wipe fails on
		// step 3, the next startup still sees the legacy tag, reads
		// the same rows again, and re-attempts the wipe — the
		// in-memory subscriptions already line up so the redundant
		// write is idempotent. Stripping the metadata before the
		// wipe would orphan rows on failure with no retry path.
		stagingSpec := def.Spec
		stagingSpec.Subscriptions = mergeLegacySubscriptions(def.Spec.Subscriptions, rows, time.Now().UTC())
		stagingAt := time.Now().UTC()
		if err := a.persistLoopDefinition(stagingSpec, stagingAt); err != nil {
			errs = append(errs, fmt.Errorf("persist migrated spec %q (step 1): %w", def.Name, err))
			continue
		}
		if err := a.loopDefinitionRegistry.Upsert(stagingSpec, stagingAt); err != nil {
			errs = append(errs, fmt.Errorf("upsert migrated spec %q (step 1): %w", def.Name, err))
			continue
		}

		// Step 2: wipe the watchlist rows. On failure leave the
		// metadata in place so the next run resumes from step 1.
		if err := a.watchlistStore.RemoveAllForScope(legacyTag); err != nil {
			errs = append(errs, fmt.Errorf("wipe legacy rows for %q (scope=%q): %w", def.Name, legacyTag, err))
			continue
		}

		// Step 3: now safe to strip the legacy metadata; the rows
		// it pointed at are gone.
		finalSpec := stagingSpec
		finalSpec.Metadata = stripLegacyScopeKeys(finalSpec.Metadata)
		finalSpec.Tags = stripLegacyScopeTagValue(finalSpec.Tags, legacyTag)
		finalAt := time.Now().UTC()
		if err := a.persistLoopDefinition(finalSpec, finalAt); err != nil {
			errs = append(errs, fmt.Errorf("persist migrated spec %q (step 3): %w", def.Name, err))
			continue
		}
		if err := a.loopDefinitionRegistry.Upsert(finalSpec, finalAt); err != nil {
			errs = append(errs, fmt.Errorf("upsert migrated spec %q (step 3): %w", def.Name, err))
			continue
		}

		migrated++
		a.logger.Info("migrated legacy scope_tag subscriptions to spec.Subscriptions",
			"name", def.Name, "scope_tag", legacyTag, "row_count", len(rows))
	}
	if migrated > 0 {
		a.logger.Info("legacy scope_tag subscription migration complete", "migrated", migrated)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// legacyScopeTag returns the scope tag previously recorded on
// spec.Metadata under one of the legacy keys, or empty when none
// remain. Kept in this file so the rest of the codebase can drop
// the constants once startup has rewritten every spec.
func legacyScopeTag(spec looppkg.Spec) string {
	if tag := strings.TrimSpace(spec.Metadata["scope_tag"]); tag != "" {
		return tag
	}
	return strings.TrimSpace(spec.Metadata["focus_tag"])
}

func stripLegacyScopeKeys(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return metadata
	}
	out := make(map[string]string, len(metadata))
	for k, v := range metadata {
		if k == "scope_tag" || k == "focus_tag" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func stripLegacyScopeTagValue(tags []string, legacyTag string) []string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if t == legacyTag {
			continue
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeLegacySubscriptions converts watchlist rows into
// [looppkg.EntitySubscription] and unions them with any subscriptions
// already present on the spec, first-wins on entity_id. The "already
// present" branch matters when an operator partially upgraded a spec
// by hand before the migration ran.
func mergeLegacySubscriptions(existing []looppkg.EntitySubscription, rows []awareness.WatchedSubscription, addedAt time.Time) []looppkg.EntitySubscription {
	seen := make(map[string]struct{}, len(existing)+len(rows))
	out := make([]looppkg.EntitySubscription, 0, len(existing)+len(rows))
	for _, sub := range existing {
		if sub.EntityID == "" {
			continue
		}
		if _, dup := seen[sub.EntityID]; dup {
			continue
		}
		seen[sub.EntityID] = struct{}{}
		// If a partially-upgraded spec carried entries with TTL>0
		// but missing AddedAt (the documented footgun on the spec
		// shape), stamp them so the migration doesn't preserve a
		// permanent watcher. Same boundary invariant the
		// UnmarshalJSON sweep enforces.
		if sub.TTLSeconds > 0 && sub.AddedAt.IsZero() {
			sub.AddedAt = addedAt
		}
		out = append(out, sub)
	}
	for _, row := range rows {
		if row.EntityID == "" {
			continue
		}
		if _, dup := seen[row.EntityID]; dup {
			continue
		}
		seen[row.EntityID] = struct{}{}
		converted := looppkg.EntitySubscription{
			EntityID: row.EntityID,
			History:  append([]int(nil), row.History...),
			Forecast: row.Forecast,
			Include:  cloneEntityMetadataIncludesPtr(row.Include),
			AddedAt:  addedAt,
		}
		if row.ExpiresAt != nil {
			ttl := int(row.ExpiresAt.Sub(addedAt).Seconds())
			if ttl > 0 {
				converted.TTLSeconds = ttl
			}
		}
		out = append(out, converted)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneEntityMetadataIncludesPtr(src *homeassistant.EntityMetadataIncludes) *homeassistant.EntityMetadataIncludes {
	if src == nil || !src.Any() {
		return nil
	}
	cp := *src
	return &cp
}
