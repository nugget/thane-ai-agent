package mqtt

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/router"
)

// WakeSubscription pairs an MQTT topic filter with the LoopSeed
// configuration used to wake the agent when a message arrives.
type WakeSubscription struct {
	ID        string          `json:"id"`
	Topic     string          `json:"topic"`
	Seed      router.LoopSeed `json:"seed"`
	Source    string          `json:"source"` // "config" or "runtime"
	CreatedAt time.Time       `json:"created_at"`
}

// SubscriptionStore manages wake-enabled MQTT subscriptions with
// persistent storage in SQLite for runtime-added subscriptions. Config-
// defined subscriptions are held in memory only and reloaded on restart.
type SubscriptionStore struct {
	db            *sql.DB
	mu            sync.RWMutex
	subs          []WakeSubscription
	logger        *slog.Logger
	subscribeHook func(topics []string) // called after Add with new topics
}

const createSubscriptionsTable = `
CREATE TABLE IF NOT EXISTS mqtt_wake_subscriptions (
	id         TEXT PRIMARY KEY,
	topic      TEXT NOT NULL,
	seed_json  TEXT NOT NULL,
	source     TEXT NOT NULL DEFAULT 'runtime',
	created_at TEXT NOT NULL
)`

// NewSubscriptionStore creates a SubscriptionStore backed by the given
// database. It creates the schema if needed and loads any previously
// persisted runtime subscriptions.
func NewSubscriptionStore(db *sql.DB, logger *slog.Logger) (*SubscriptionStore, error) {
	if logger == nil {
		logger = slog.Default()
	}

	if _, err := db.Exec(createSubscriptionsTable); err != nil {
		return nil, fmt.Errorf("create mqtt_wake_subscriptions table: %w", err)
	}

	s := &SubscriptionStore{
		db:     db,
		logger: logger,
	}

	if err := s.loadRuntime(); err != nil {
		return nil, fmt.Errorf("load runtime subscriptions: %w", err)
	}

	return s, nil
}

// loadRuntime reads persisted runtime subscriptions from SQLite.
func (s *SubscriptionStore) loadRuntime() error {
	rows, err := s.db.Query(`SELECT id, topic, seed_json, source, created_at FROM mqtt_wake_subscriptions`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var ws WakeSubscription
		var seedJSON, createdAt string
		if err := rows.Scan(&ws.ID, &ws.Topic, &seedJSON, &ws.Source, &createdAt); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(seedJSON), &ws.Seed); err != nil {
			s.logger.Warn("skipping subscription with invalid seed JSON",
				"id", ws.ID, "error", err)
			continue
		}
		ts, err := database.ParseTimestamp(createdAt)
		if err != nil {
			s.logger.Warn("skipping subscription with invalid timestamp",
				"id", ws.ID, "error", err)
			continue
		}
		ws.CreatedAt = ts
		s.subs = append(s.subs, ws)
	}

	return rows.Err()
}

// LoadConfig loads config-defined wake subscriptions. Only entries with
// a non-nil Wake field are loaded. Config subscriptions are not persisted
// to SQLite and cannot be removed via [SubscriptionStore.Remove].
// Subscriptions with invalid topic filters or seeds are logged and
// skipped rather than causing a startup failure.
func (s *SubscriptionStore) LoadConfig(subs []config.SubscriptionConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove any previous config-sourced entries so LoadConfig is
	// idempotent when called on restart.
	filtered := s.subs[:0]
	for _, ws := range s.subs {
		if ws.Source != "config" {
			filtered = append(filtered, ws)
		}
	}
	s.subs = filtered

	for i, sc := range subs {
		if sc.Wake == nil {
			continue
		}
		if err := router.ValidateTopicFilter(sc.Topic); err != nil {
			s.logger.Warn("skipping config subscription with invalid topic",
				"index", i, "topic", sc.Topic, "error", err)
			continue
		}
		if err := sc.Wake.Validate(); err != nil {
			s.logger.Warn("skipping config subscription with invalid seed",
				"index", i, "topic", sc.Topic, "error", err)
			continue
		}
		s.subs = append(s.subs, WakeSubscription{
			ID:        fmt.Sprintf("cfg-%s-%d", topicHash(sc.Topic), i),
			Topic:     sc.Topic,
			Seed:      *sc.Wake,
			Source:    "config",
			CreatedAt: time.Now(),
		})
	}
}

// SetSubscribeHook registers a callback that is invoked after a runtime
// subscription is added. The callback receives the new topic filter(s)
// so the caller can send a live SUBSCRIBE to the broker. Must be called
// before any concurrent Add calls (typically during init).
func (s *SubscriptionStore) SetSubscribeHook(fn func(topics []string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribeHook = fn
}

// Add creates a runtime wake subscription, persists it to SQLite, and
// returns the new subscription. The topic filter and seed are validated
// before persistence — invalid values are rejected early rather than
// stored and retried forever.
func (s *SubscriptionStore) Add(topic string, seed router.LoopSeed) (WakeSubscription, error) {
	if err := router.ValidateTopicFilter(topic); err != nil {
		return WakeSubscription{}, fmt.Errorf("invalid topic filter: %w", err)
	}
	if err := seed.Validate(); err != nil {
		return WakeSubscription{}, fmt.Errorf("invalid loop seed: %w", err)
	}

	ws := WakeSubscription{
		ID:        fmt.Sprintf("rt-%s-%d", topicHash(topic), time.Now().UnixMilli()),
		Topic:     topic,
		Seed:      seed,
		Source:    "runtime",
		CreatedAt: time.Now(),
	}

	seedJSON, err := json.Marshal(ws.Seed)
	if err != nil {
		return WakeSubscription{}, fmt.Errorf("marshal seed: %w", err)
	}

	_, err = s.db.Exec(
		`INSERT INTO mqtt_wake_subscriptions (id, topic, seed_json, source, created_at) VALUES (?, ?, ?, ?, ?)`,
		ws.ID, ws.Topic, string(seedJSON), ws.Source, ws.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return WakeSubscription{}, fmt.Errorf("insert subscription: %w", err)
	}

	s.mu.Lock()
	s.subs = append(s.subs, ws)
	s.mu.Unlock()

	s.logger.Info("mqtt wake subscription added",
		"id", ws.ID, "topic", ws.Topic)

	// Copy under lock to avoid racing with SetSubscribeHook.
	s.mu.RLock()
	hook := s.subscribeHook
	s.mu.RUnlock()

	if hook != nil {
		hook([]string{ws.Topic})
	}

	return ws, nil
}

// Remove deletes a runtime subscription by ID. Config-sourced
// subscriptions cannot be removed and return an error.
func (s *SubscriptionStore) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, ws := range s.subs {
		if ws.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("subscription %q not found", id)
	}
	if s.subs[idx].Source == "config" {
		return fmt.Errorf("cannot remove config-defined subscription %q", id)
	}

	if _, err := s.db.Exec(`DELETE FROM mqtt_wake_subscriptions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete subscription: %w", err)
	}

	s.subs = append(s.subs[:idx], s.subs[idx+1:]...)
	s.logger.Info("mqtt wake subscription removed", "id", id)
	return nil
}

// List returns all wake subscriptions (config + runtime).
func (s *SubscriptionStore) List() []WakeSubscription {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]WakeSubscription, len(s.subs))
	copy(result, s.subs)
	return result
}

// Matches returns all wake subscriptions whose topic filter matches
// the given concrete MQTT topic. Multiple subscriptions on the same
// topic (with different LoopSeed configurations) are all returned,
// enabling fan-out dispatch.
func (s *SubscriptionStore) Matches(topic string) []WakeSubscription {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var matches []WakeSubscription
	for _, ws := range s.subs {
		if matchTopicFilter(ws.Topic, topic) {
			matches = append(matches, ws)
		}
	}
	return matches
}

// Topics returns all unique topic filters across config and runtime
// subscriptions. Useful for building the MQTT SUBSCRIBE packet.
func (s *SubscriptionStore) Topics() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seen := make(map[string]struct{}, len(s.subs))
	var topics []string
	for _, ws := range s.subs {
		if _, ok := seen[ws.Topic]; !ok {
			seen[ws.Topic] = struct{}{}
			topics = append(topics, ws.Topic)
		}
	}
	return topics
}

// matchTopicFilter returns true if the concrete topic matches the MQTT
// topic filter. Supports + (single-level wildcard) and # (multi-level
// wildcard, must be last segment) per MQTT v5 specification.
func matchTopicFilter(filter, topic string) bool {
	if filter == "#" {
		return true
	}

	filterParts := strings.Split(filter, "/")
	topicParts := strings.Split(topic, "/")

	for i, fp := range filterParts {
		if fp == "#" {
			// # must be the last segment and matches everything remaining.
			return i == len(filterParts)-1
		}
		if i >= len(topicParts) {
			return false
		}
		if fp == "+" {
			continue
		}
		if fp != topicParts[i] {
			return false
		}
	}

	return len(filterParts) == len(topicParts)
}

// topicHash returns a short, collision-resistant hex hash of a topic
// string for use in subscription IDs. Uses the first 8 bytes (16 hex
// chars) of a SHA-256 digest.
func topicHash(topic string) string {
	h := sha256.Sum256([]byte(topic))
	return hex.EncodeToString(h[:8])
}
