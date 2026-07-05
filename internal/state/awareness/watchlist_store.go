package awareness

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// OwnerSystem is the reserved owner for rows the runtime seeds and
// maintains itself — today the person-entity ingestion floor, re-seeded
// from config at every boot. System rows are visible in
// list_entity_subscriptions like any other row but refuse tool-driven
// mutation; the configuration is their source of truth.
const OwnerSystem = "system"

// SubscriptionRow is one persisted registry entry: an owner plus the
// unified subscription declaration. Owner ” means always-visible (the
// global tier every turn renders), [OwnerSystem] marks runtime-seeded
// rows, and any other value is the owning loop's definition name —
// loop rows are compiled from Spec.Subscriptions and replaced whenever
// the spec persists.
type SubscriptionRow struct {
	Owner string
	looppkg.EntitySubscription
}

// WatchlistStore persists the entity-subscription registry in SQLite.
type WatchlistStore struct {
	db *sql.DB
}

// NewWatchlistStore creates a watchlist store, running migrations on first use.
func NewWatchlistStore(db *sql.DB, logger *slog.Logger) (*WatchlistStore, error) {
	if err := database.Migrate(db, watchlistSchema, logger); err != nil {
		return nil, err
	}
	return &WatchlistStore{db: db}, nil
}

// Upsert inserts or replaces the subscription for (owner, entity_id).
// A zero AddedAt is stamped with the current time so TTL countdown is
// always anchored; re-upserting an entity restarts its TTL window.
func (s *WatchlistStore) Upsert(owner string, sub looppkg.EntitySubscription) error {
	if strings.TrimSpace(sub.EntityID) == "" {
		return fmt.Errorf("entity_id is required")
	}
	if sub.AddedAt.IsZero() {
		sub.AddedAt = time.Now().UTC()
	}
	optsJSON, err := marshalSubscriptionOptions(sub)
	if err != nil {
		return fmt.Errorf("marshal options: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT INTO watched_entity_subscriptions (entity_id, owner, added_at, options)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(owner, entity_id) DO UPDATE SET
			options = excluded.options,
			added_at = excluded.added_at
	`, sub.EntityID, strings.TrimSpace(owner), sub.AddedAt.UTC(), string(optsJSON))
	if err != nil {
		return fmt.Errorf("upsert subscription %s/%s: %w", owner, sub.EntityID, err)
	}
	return nil
}

// Remove deletes the subscription for (owner, entity_id). A missing
// row is a no-op.
func (s *WatchlistStore) Remove(owner, entityID string) error {
	_, err := s.db.Exec(
		`DELETE FROM watched_entity_subscriptions WHERE owner = ? AND entity_id = ?`,
		strings.TrimSpace(owner), entityID,
	)
	return err
}

// RemoveAllForOwner deletes every subscription row belonging to the
// given owner. Used when the owning loop definition is deleted and by
// [WatchlistStore.ReplaceOwner]. The global tier (owner ”) is never
// bulk-deleted; owner is required.
func (s *WatchlistStore) RemoveAllForOwner(owner string) error {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return fmt.Errorf("owner is required")
	}
	_, err := s.db.Exec(
		`DELETE FROM watched_entity_subscriptions WHERE owner = ?`,
		owner,
	)
	return err
}

// ReplaceOwner atomically replaces the owner's rows with the given
// subscription set — the compilation step that mirrors a loop spec's
// Subscriptions into the registry (replace-on-persist, the same
// lifecycle spec seeding implies). Entries that are already expired
// are skipped rather than written and immediately reaped.
func (s *WatchlistStore) ReplaceOwner(owner string, subs []looppkg.EntitySubscription) error {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return fmt.Errorf("owner is required")
	}
	now := time.Now().UTC()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin replace tx for %s: %w", owner, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`DELETE FROM watched_entity_subscriptions WHERE owner = ?`, owner,
	); err != nil {
		return fmt.Errorf("clear rows for %s: %w", owner, err)
	}
	for _, sub := range subs {
		if strings.TrimSpace(sub.EntityID) == "" {
			continue
		}
		if sub.AddedAt.IsZero() {
			sub.AddedAt = now
		}
		if sub.IsExpired(now) {
			continue
		}
		optsJSON, err := marshalSubscriptionOptions(sub)
		if err != nil {
			return fmt.Errorf("marshal options for %s/%s: %w", owner, sub.EntityID, err)
		}
		if _, err := tx.Exec(`
			INSERT INTO watched_entity_subscriptions (entity_id, owner, added_at, options)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(owner, entity_id) DO UPDATE SET
				options = excluded.options,
				added_at = excluded.added_at
		`, sub.EntityID, owner, sub.AddedAt.UTC(), string(optsJSON)); err != nil {
			return fmt.Errorf("insert row for %s/%s: %w", owner, sub.EntityID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace tx for %s: %w", owner, err)
	}
	return nil
}

// Owners returns the distinct owners present in the registry,
// excluding the global tier (”). Consumed by the startup orphan sweep
// to find rows whose owning definition no longer exists.
func (s *WatchlistStore) Owners() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT owner FROM watched_entity_subscriptions WHERE owner != '' ORDER BY owner ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var owners []string
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			return nil, err
		}
		owners = append(owners, owner)
	}
	return owners, rows.Err()
}

// ListAll returns every active subscription row across all owners in
// insertion order. Expired rows are reaped as a side effect.
func (s *WatchlistStore) ListAll() ([]SubscriptionRow, error) {
	return s.scanActiveSubscriptions(
		`SELECT entity_id, owner, added_at, options FROM watched_entity_subscriptions
		 ORDER BY added_at ASC`)
}

// ListOwner returns the active subscription rows for one owner in
// insertion order; ” selects the always-visible global tier. Expired
// rows are reaped as a side effect.
func (s *WatchlistStore) ListOwner(owner string) ([]SubscriptionRow, error) {
	return s.scanActiveSubscriptions(
		`SELECT entity_id, owner, added_at, options FROM watched_entity_subscriptions
		 WHERE owner = ? ORDER BY added_at ASC`,
		strings.TrimSpace(owner),
	)
}

// GlobalEntityGates returns, for each candidate whose always-visible
// row (owner=”) can render at all — state-rendering mode, not expired
// — that row's RequiresTag gate ("" for an ungated row). Rows that
// never render (ingest-only, elapsed TTL) are omitted entirely, so
// they cannot suppress a loop-scoped render of the same entity;
// callers combine the returned gate with their own active-tag set for
// the same reason — a gated-off global row renders nothing this turn
// either (#1213). It performs a single bounded IN-clause query and
// skips the TTL cleanup writes that [WatchlistStore.ListOwner] does —
// this method is meant to be called by sibling providers (e.g.
// [LoopSubscriptionProvider]) that only need a dedup check against
// the always-visible set on every iteration; the cleanup is left to
// the always-visible [WatchlistProvider]'s own pass so we don't
// double-write deletes. Returns an empty (non-nil) map when
// candidates is empty.
func (s *WatchlistStore) GlobalEntityGates(candidates []string) (map[string]string, error) {
	out := make(map[string]string, len(candidates))
	if len(candidates) == 0 {
		return out, nil
	}
	// Deduplicate candidates so the IN list and the param vector
	// don't bloat for a loop that lists the same entity twice
	// across cascade levels.
	seen := make(map[string]struct{}, len(candidates))
	args := make([]any, 0, len(candidates))
	for _, id := range candidates {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		args = append(args, id)
	}
	if len(args) == 0 {
		return out, nil
	}
	placeholders := strings.Repeat("?,", len(args))
	placeholders = placeholders[:len(placeholders)-1]
	query := `SELECT entity_id, added_at, options FROM watched_entity_subscriptions
		 WHERE owner = '' AND entity_id IN (` + placeholders + `)`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now().UTC()
	for rows.Next() {
		var (
			id, optsJSON string
			addedAt      time.Time
		)
		if err := rows.Scan(&id, &addedAt, &optsJSON); err != nil {
			return nil, err
		}
		sub := parseSubscriptionOptions(optsJSON, addedAt.UTC())
		// A row that never renders must not dedup anything: an
		// ingest-only row feeds capture without a context block, and
		// an expired row is gone at the next cleanup pass.
		if !sub.RendersState() || sub.IsExpired(now) {
			continue
		}
		out[id] = sub.RequiresTag
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *WatchlistStore) scanActiveSubscriptions(query string, args ...any) ([]SubscriptionRow, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now().UTC()
	var (
		subs    []SubscriptionRow
		expired []subscriptionKey
	)
	for rows.Next() {
		var (
			entityID, owner, optsJSON string
			addedAt                   time.Time
		)
		if err := rows.Scan(&entityID, &owner, &addedAt, &optsJSON); err != nil {
			return nil, err
		}
		sub := parseSubscriptionOptions(optsJSON, addedAt.UTC())
		sub.EntityID = entityID
		if sub.IsExpired(now) {
			expired = append(expired, subscriptionKey{EntityID: entityID, Owner: owner})
			continue
		}
		subs = append(subs, SubscriptionRow{Owner: owner, EntitySubscription: sub})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.removeExpiredSubscriptions(expired); err != nil {
		return nil, err
	}
	return subs, nil
}

func (s *WatchlistStore) removeExpiredSubscriptions(keys []subscriptionKey) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin expired watchlist cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, key := range keys {
		if _, err := tx.Exec(
			`DELETE FROM watched_entity_subscriptions WHERE entity_id = ? AND owner = ?`,
			key.EntityID, key.Owner,
		); err != nil {
			return fmt.Errorf("delete expired watchlist subscription %s/%s: %w", key.Owner, key.EntityID, err)
		}
	}
	return tx.Commit()
}

type subscriptionKey struct {
	EntityID string
	Owner    string
}

func (s *WatchlistStore) subscriptionCount() (int, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM watched_entity_subscriptions`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// subscriptionOptionsWire is the options-blob JSON shape. It carries
// the declaration fields of [looppkg.EntitySubscription] that don't
// have their own column; entity_id, owner, and added_at live on the
// row. ExpiresAt is decode-only: pre-#1209 rows persisted an absolute
// expiry instead of ttl_seconds, and the shim in
// parseSubscriptionOptions converts it back against the row's
// added_at so old TTLs keep counting down unchanged.
type subscriptionOptionsWire struct {
	History                  []int                                 `json:"history,omitempty"`
	Forecast                 string                                `json:"forecast,omitempty"`
	Mode                     string                                `json:"mode,omitempty"`
	Include                  *homeassistant.EntityMetadataIncludes `json:"include,omitempty"`
	TTLSeconds               int                                   `json:"ttl_seconds,omitempty"`
	SelfOnly                 bool                                  `json:"self_only,omitempty"`
	RequiresTag              string                                `json:"requires_tag,omitempty"`
	Transitions              int                                   `json:"transitions,omitempty"`
	TransitionsWindowSeconds int                                   `json:"transitions_window_seconds,omitempty"`
	Wake                     bool                                  `json:"wake,omitempty"`
	WakeDebounceSeconds      int                                   `json:"wake_debounce_seconds,omitempty"`
	ExpiresAt                string                                `json:"expires_at,omitempty"`
}

func marshalSubscriptionOptions(sub looppkg.EntitySubscription) ([]byte, error) {
	forecast, err := looppkg.NormalizeSubscriptionForecast(sub.Forecast)
	if err != nil {
		return nil, err
	}
	mode, err := looppkg.NormalizeSubscriptionMode(sub.Mode)
	if err != nil {
		return nil, err
	}
	if sub.TTLSeconds < 0 {
		return nil, fmt.Errorf("ttl_seconds must be >= 0, got %d", sub.TTLSeconds)
	}
	if sub.Transitions < 0 || sub.TransitionsWindowSeconds < 0 {
		return nil, fmt.Errorf("transitions and transitions_window_seconds must be >= 0, got %d/%d", sub.Transitions, sub.TransitionsWindowSeconds)
	}
	if sub.WakeDebounceSeconds < 0 {
		return nil, fmt.Errorf("wake_debounce_seconds must be >= 0, got %d", sub.WakeDebounceSeconds)
	}
	wire := subscriptionOptionsWire{
		History:                  append([]int(nil), sub.History...),
		Forecast:                 forecast,
		Mode:                     mode,
		Include:                  sub.Include.Clone(),
		TTLSeconds:               sub.TTLSeconds,
		SelfOnly:                 sub.SelfOnly,
		RequiresTag:              strings.TrimSpace(sub.RequiresTag),
		Transitions:              sub.Transitions,
		TransitionsWindowSeconds: sub.TransitionsWindowSeconds,
		Wake:                     sub.Wake,
		WakeDebounceSeconds:      sub.WakeDebounceSeconds,
	}
	if wire.Include != nil && !wire.Include.Any() {
		wire.Include = nil
	}
	return json.Marshal(wire)
}

// parseSubscriptionOptions decodes an options blob into the unified
// declaration, anchored at the row's added_at. Unrecognized forecast
// or mode values degrade to their defaults (no forecast / render)
// rather than erroring — the legacy-tolerant read posture the store
// has always had.
func parseSubscriptionOptions(optsJSON string, addedAt time.Time) looppkg.EntitySubscription {
	sub := looppkg.EntitySubscription{AddedAt: addedAt}
	if optsJSON == "" || optsJSON == "{}" {
		return sub
	}
	var wire subscriptionOptionsWire
	if err := json.Unmarshal([]byte(optsJSON), &wire); err != nil {
		return sub
	}
	sub.History = append([]int(nil), wire.History...)
	sub.Include = wire.Include.Clone()
	sub.SelfOnly = wire.SelfOnly
	sub.RequiresTag = strings.TrimSpace(wire.RequiresTag)
	sub.Wake = wire.Wake
	if wire.WakeDebounceSeconds > 0 {
		sub.WakeDebounceSeconds = wire.WakeDebounceSeconds
	}
	if wire.Transitions > 0 {
		sub.Transitions = wire.Transitions
	}
	if wire.TransitionsWindowSeconds > 0 {
		sub.TransitionsWindowSeconds = wire.TransitionsWindowSeconds
	}
	if forecast, err := looppkg.NormalizeSubscriptionForecast(wire.Forecast); err == nil {
		sub.Forecast = forecast
	}
	if mode, err := looppkg.NormalizeSubscriptionMode(wire.Mode); err == nil {
		sub.Mode = mode
	}
	if wire.TTLSeconds > 0 {
		sub.TTLSeconds = wire.TTLSeconds
	} else if wire.ExpiresAt != "" {
		// Legacy absolute-expiry row: recover the TTL against the
		// row's added_at anchor. An expiry at or before added_at
		// (clock skew, an already-elapsed row) maps to a 1-second
		// TTL so the expiry sweep reaps it on this pass instead of
		// resurrecting it as immortal.
		if ts, err := time.Parse(time.RFC3339, wire.ExpiresAt); err == nil {
			ttl := int(ts.UTC().Sub(addedAt).Seconds())
			if ttl <= 0 {
				ttl = 1
			}
			sub.TTLSeconds = ttl
		}
	}
	return sub
}
