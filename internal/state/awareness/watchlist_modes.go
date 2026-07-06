package awareness

import (
	"fmt"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// IngestGlobs returns the entity ids/globs of rows — any owner — whose
// mode feeds the state-change window (ingest or both), excluding
// expired entries. This is the runtime source of the StateWatcher's
// ingestion filter: the registry replacement for the retired
// homeassistant.subscribe config globs (#1192), widened in #1209 from
// the always-visible tier to every owner so a loop-owned ingest
// subscription feeds capture the same way a global one does. Registry
// targets (area:/label:/floor:) are skipped — the EntityFilter speaks
// ids and globs only, and the tool boundary rejects them for ingest
// modes. Expired TTL rows drop out at the next rebuild rather than
// instantly.
func (s *WatchlistStore) IngestGlobs(now time.Time) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT entity_id, added_at, options FROM watched_entity_subscriptions`)
	if err != nil {
		return nil, fmt.Errorf("query ingest globs: %w", err)
	}
	defer rows.Close()

	var globs []string
	seen := make(map[string]struct{})
	for rows.Next() {
		var (
			entityID, optsJSON string
			addedAt            time.Time
		)
		if err := rows.Scan(&entityID, &addedAt, &optsJSON); err != nil {
			return nil, err
		}
		sub := parseSubscriptionOptions(optsJSON, addedAt.UTC())
		sub.EntityID = entityID
		if sub.IsExpired(now) {
			continue
		}
		// Capture is fed two ways: an explicit ingest-feeding mode, or
		// derivation — a declared transition log needs the stream, so
		// its target joins the filter automatically (#1210). For the
		// mode path, tag-gated rows never feed capture (the gate is
		// render-only, #1213; tool boundaries reject the combination
		// and this skip is the backstop). Derived capture is the
		// deliberate exception: a gated transition log captures
		// unconditionally so the log is warm when the tag activates —
		// only its rendering follows the gate.
		modeFeeds := sub.FeedsIngest() && sub.RequiresTag == ""
		if !modeFeeds && !sub.WantsTransitions() && !sub.Wake {
			continue
		}
		if ParseSubscriptionTarget(entityID).IsRegistryTarget() {
			continue
		}
		if _, dup := seen[entityID]; dup {
			continue
		}
		seen[entityID] = struct{}{}
		globs = append(globs, entityID)
	}
	return globs, rows.Err()
}

// CheckIngestCapacity enforces the ingestion-registry cap (#1192) for
// a subscription about to be written that will feed the filter —
// through an ingest-feeding mode or through transition-log derivation
// (#1210). The cap gates only genuinely new filter entries: re-adding
// an id already in the filter updates it in place and always passes.
// Call before persisting; the returned error teaches the recovery
// move.
func CheckIngestCapacity(store *WatchlistStore, sub looppkg.EntitySubscription) error {
	if store == nil || (!sub.FeedsIngest() && !sub.WantsTransitions() && !sub.Wake) {
		return nil
	}
	globs, err := store.IngestGlobs(time.Now())
	if err != nil {
		return fmt.Errorf("count ingest entries: %w", err)
	}
	for _, g := range globs {
		if g == sub.EntityID {
			return nil
		}
	}
	if len(globs) >= maxIngestEntries {
		return fmt.Errorf("ingest registry is at its cap (%d entries) — remove entries before adding more; a broad glob covers more for less", maxIngestEntries)
	}
	return nil
}
