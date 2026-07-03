package awareness

import (
	"fmt"
	"time"
)

// Subscription modes: what a watched entry feeds. Render is the
// classic watchlist behavior (per-turn context injection); ingest feeds
// the state-change window's push pipeline without rendering every turn;
// both does both. Stored in the options blob — absent means render, so
// existing rows keep their behavior unchanged.
const (
	SubscriptionModeRender = "render"
	SubscriptionModeIngest = "ingest"
	SubscriptionModeBoth   = "both"
)

// normalizeSubscriptionMode maps stored/user mode strings to a
// canonical value, treating empty as render. Returns "" for anything
// unrecognized so callers can reject it.
func normalizeSubscriptionMode(mode string) string {
	switch mode {
	case "", SubscriptionModeRender:
		return SubscriptionModeRender
	case SubscriptionModeIngest:
		return SubscriptionModeIngest
	case SubscriptionModeBoth:
		return SubscriptionModeBoth
	default:
		return ""
	}
}

// IngestGlobs returns the entity ids/globs of always-visible rows whose
// mode feeds the state-change window (ingest or both), excluding
// expired entries. This is the runtime source of the StateWatcher's
// ingestion filter — the registry replacement for the retired
// homeassistant.subscribe config globs (#1192). Expired TTL rows drop
// out at the next rebuild rather than instantly.
func (s *WatchlistStore) IngestGlobs(now time.Time) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT entity_id, options FROM watched_entity_subscriptions WHERE scope = ''`)
	if err != nil {
		return nil, fmt.Errorf("query ingest globs: %w", err)
	}
	defer rows.Close()

	var globs []string
	for rows.Next() {
		var entityID, optsJSON string
		if err := rows.Scan(&entityID, &optsJSON); err != nil {
			return nil, err
		}
		opts := parseWatchlistOptions(optsJSON)
		if opts.expired(now) {
			continue
		}
		if opts.Mode == SubscriptionModeIngest || opts.Mode == SubscriptionModeBoth {
			globs = append(globs, entityID)
		}
	}
	return globs, rows.Err()
}
