package awareness

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// WatchedSubscription represents one entity subscription scope with its stored
// options. Empty Scope means the entity is always visible.
type WatchedSubscription struct {
	EntityID  string
	Scope     string
	History   []int
	Forecast  string
	ExpiresAt *time.Time
}

// WatchlistStore persists the set of watched entity subscriptions in SQLite.
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

// Add inserts an entity into the watchlist with no scope or options.
// Duplicates are silently ignored. Use [AddWithOptions] for richer
// subscriptions.
func (s *WatchlistStore) Add(entityID string) error {
	_, err := s.db.Exec(
		`INSERT INTO watched_entity_subscriptions (entity_id, scope, options)
		 VALUES (?, '', '{}')
		 ON CONFLICT(scope, entity_id) DO NOTHING`,
		entityID,
	)
	return err
}

// AddWithOptions inserts or updates entity subscriptions with tag scopes,
// historical offsets, optional weather forecast type, and an optional TTL.
// Empty tags means the entity is always visible in context.
func (s *WatchlistStore) AddWithOptions(entityID string, tags []string, history []int, ttlSeconds int, forecast string) error {
	scopes := normalizeScopes(tags)

	optsJSON, err := marshalWatchlistOptions(history, ttlSeconds, forecast)
	if err != nil {
		return fmt.Errorf("marshal options: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin watchlist tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, scope := range scopes {
		if _, err := tx.Exec(`
			INSERT INTO watched_entity_subscriptions (entity_id, scope, options)
			VALUES (?, ?, ?)
			ON CONFLICT(scope, entity_id) DO UPDATE SET options = excluded.options
		`, entityID, scope, string(optsJSON)); err != nil {
			return fmt.Errorf("upsert watchlist subscription for %s/%s: %w", entityID, scope, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit watchlist tx: %w", err)
	}
	return nil
}

// Remove deletes all subscriptions for an entity. Non-existent IDs are a no-op.
func (s *WatchlistStore) Remove(entityID string) error {
	return s.RemoveWithScopes(entityID, nil)
}

// RemoveWithScopes deletes subscriptions for an entity. When scopes is empty,
// all subscriptions for the entity are removed.
func (s *WatchlistStore) RemoveWithScopes(entityID string, scopes []string) error {
	if len(scopes) == 0 {
		_, err := s.db.Exec(
			`DELETE FROM watched_entity_subscriptions WHERE entity_id = ?`,
			entityID,
		)
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin watchlist delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, scope := range normalizeScopes(scopes) {
		if _, err := tx.Exec(
			`DELETE FROM watched_entity_subscriptions WHERE entity_id = ? AND scope = ?`,
			entityID, scope,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// List returns all watched entity IDs in insertion order, deduplicated across
// scoped subscriptions.
func (s *WatchlistStore) List() ([]string, error) {
	subs, err := s.ListSubscriptions("")
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(subs))
	seen := make(map[string]bool, len(subs))
	for _, sub := range subs {
		if seen[sub.EntityID] {
			continue
		}
		seen[sub.EntityID] = true
		ids = append(ids, sub.EntityID)
	}
	return ids, nil
}

// ListUntagged returns watched entities that have no capability scope
// and are always visible in context regardless of active tags.
func (s *WatchlistStore) ListUntagged() ([]string, error) {
	subs, err := s.ListUntaggedSubscriptions()
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(subs))
	for _, sub := range subs {
		ids = append(ids, sub.EntityID)
	}
	return ids, nil
}

// ListUntaggedSubscriptions returns all always-visible subscriptions with their
// stored options intact.
func (s *WatchlistStore) ListUntaggedSubscriptions() ([]WatchedSubscription, error) {
	return s.listScopedSubscriptions("")
}

// ListByTag returns watched entity subscriptions for the given capability
// tag. Used by the tag context assembler to inject entity context only when a
// tag is active.
func (s *WatchlistStore) ListByTag(tag string) ([]WatchedSubscription, error) {
	return s.listScopedSubscriptions(tag)
}

// ListSubscriptions returns active subscriptions. When scope is empty, all
// scopes are returned.
func (s *WatchlistStore) ListSubscriptions(scope string) ([]WatchedSubscription, error) {
	query := `SELECT entity_id, scope, options FROM watched_entity_subscriptions`
	var args []any
	if scope != "" {
		query += ` WHERE scope = ?`
		args = append(args, scope)
	}
	query += ` ORDER BY added_at ASC`
	return s.scanActiveSubscriptions(query, args...)
}

// DistinctTags returns all unique active scopes across watched entities.
// Used at startup to register tag-scoped watchlist providers.
func (s *WatchlistStore) DistinctTags() ([]string, error) {
	subs, err := s.ListSubscriptions("")
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	for _, sub := range subs {
		if sub.Scope == "" {
			continue
		}
		seen[sub.Scope] = true
	}

	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags, nil
}

func (s *WatchlistStore) listScopedSubscriptions(scope string) ([]WatchedSubscription, error) {
	return s.scanActiveSubscriptions(
		`SELECT entity_id, scope, options FROM watched_entity_subscriptions
		 WHERE scope = ? ORDER BY added_at ASC`,
		scope,
	)
}

func (s *WatchlistStore) scanActiveSubscriptions(query string, args ...any) ([]WatchedSubscription, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	now := time.Now().UTC()
	var (
		subs    []WatchedSubscription
		expired []subscriptionKey
	)
	for rows.Next() {
		var entityID, scope, optsJSON string
		if err := rows.Scan(&entityID, &scope, &optsJSON); err != nil {
			return nil, err
		}
		opts := parseWatchlistOptions(optsJSON)
		if opts.expired(now) {
			expired = append(expired, subscriptionKey{EntityID: entityID, Scope: scope})
			continue
		}
		subs = append(subs, WatchedSubscription{
			EntityID:  entityID,
			Scope:     scope,
			History:   append([]int(nil), opts.History...),
			Forecast:  opts.Forecast,
			ExpiresAt: cloneTimePtr(opts.ExpiresAt),
		})
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
			`DELETE FROM watched_entity_subscriptions WHERE entity_id = ? AND scope = ?`,
			key.EntityID, key.Scope,
		); err != nil {
			return fmt.Errorf("delete expired watchlist subscription %s/%s: %w", key.EntityID, key.Scope, err)
		}
	}
	return tx.Commit()
}

type subscriptionKey struct {
	EntityID string
	Scope    string
}

func (s *WatchlistStore) subscriptionCount() (int, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM watched_entity_subscriptions`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

type watchlistOptions struct {
	History   []int
	Forecast  string
	ExpiresAt *time.Time
}

type watchlistOptionsWire struct {
	History   []int  `json:"history,omitempty"`
	Forecast  string `json:"forecast,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

func marshalWatchlistOptions(history []int, ttlSeconds int, forecast string) ([]byte, error) {
	forecast, err := normalizeForecastType(forecast)
	if err != nil {
		return nil, err
	}
	wire := watchlistOptionsWire{
		History:  append([]int(nil), history...),
		Forecast: forecast,
	}
	if ttlSeconds > 0 {
		wire.ExpiresAt = time.Now().UTC().Add(time.Duration(ttlSeconds) * time.Second).Format(time.RFC3339)
	}
	return json.Marshal(wire)
}

func parseWatchlistOptions(optsJSON string) watchlistOptions {
	if optsJSON == "" || optsJSON == "{}" {
		return watchlistOptions{}
	}
	var wire watchlistOptionsWire
	if err := json.Unmarshal([]byte(optsJSON), &wire); err != nil {
		return watchlistOptions{}
	}
	out := watchlistOptions{
		History: append([]int(nil), wire.History...),
	}
	if forecast, err := normalizeForecastType(wire.Forecast); err == nil {
		out.Forecast = forecast
	}
	if wire.ExpiresAt != "" {
		if ts, err := time.Parse(time.RFC3339, wire.ExpiresAt); err == nil {
			ts = ts.UTC()
			out.ExpiresAt = &ts
		}
	}
	return out
}

func (o watchlistOptions) expired(now time.Time) bool {
	return o.ExpiresAt != nil && !o.ExpiresAt.After(now)
}

func normalizeScopes(tags []string) []string {
	if len(tags) == 0 {
		return []string{""}
	}
	var scopes []string
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		scopes = append(scopes, tag)
	}
	if len(scopes) == 0 {
		return []string{""}
	}
	return scopes
}

func normalizeForecastType(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	switch value {
	case "", "none":
		return "", nil
	case "daily", "hourly", "twice_daily":
		return value, nil
	default:
		return "", fmt.Errorf("forecast must be one of daily, hourly, twice_daily, or none")
	}
}

func cloneTimePtr(src *time.Time) *time.Time {
	if src == nil {
		return nil
	}
	cp := *src
	return &cp
}
