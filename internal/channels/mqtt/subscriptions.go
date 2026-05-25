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

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// WakeSubscription pairs an MQTT topic filter with one of two dispatch
// shapes used to react to messages on the topic:
//
//   - **Wake-target dispatch (preferred):** WakeTarget points at an
//     existing loop. Matching messages produce a
//     [messages.LoopEventPayload] delivered to the target loop's
//     pending notifications via [messages.NewEventSourceEnvelope].
//     The target loop's next iteration sees the MQTT payload as an
//     event-source wake and runs under its own Spec.Profile /
//     SupervisorProfile / container cascade. No new loop is
//     spawned; the registry stays clean.
//
//   - **Spawn dispatch (legacy):** Profile + InitialTags shape a
//     fresh one-shot loop run when no WakeTarget is configured.
//     Kept for backwards compatibility with subscriptions written
//     before the trigger-unification work; new subscriptions
//     should declare WakeTarget instead.
//
// HasWakeTarget reports which path a given subscription uses.
type WakeSubscription struct {
	ID    string `json:"id"`
	Topic string `json:"topic"`

	// WakeTarget identifies an existing loop to receive matching
	// MQTT messages as event-source notifications. When non-empty
	// this is the preferred dispatch route — Profile and InitialTags
	// are unused on this path because the target loop owns its own
	// routing via Spec.Profile.
	WakeTarget messages.LoopWakeTarget `json:"wake_loop,omitempty"`

	// Profile shapes the spawn-dispatch path (legacy). Used only
	// when WakeTarget is empty.
	Profile router.LoopProfile `json:"profile"`

	// InitialTags lists capability tags activated at the start of
	// the spawn-dispatch agent run (legacy path). Only used when
	// WakeTarget is empty. Modern subscriptions delegate tag
	// activation to the target loop's Spec.Tags.
	InitialTags []string `json:"initial_tags,omitempty"`

	Source    string    `json:"source"` // "config" or "runtime"
	CreatedAt time.Time `json:"created_at"`
}

// HasWakeTarget reports whether this subscription routes matching
// messages to an existing loop via the event-source envelope path
// (the modern dispatch shape), versus the legacy
// spawn-a-one-shot-loop path that consumes Profile + InitialTags.
func (ws WakeSubscription) HasWakeTarget() bool {
	return !ws.WakeTarget.Empty()
}

// AddRequest is the parameter struct for [SubscriptionStore.Add]. At
// least one of WakeTarget or Profile must carry meaningful
// configuration — a subscription that points at neither would match
// MQTT messages and then do nothing.
type AddRequest struct {
	Topic       string
	WakeTarget  messages.LoopWakeTarget
	Profile     router.LoopProfile
	InitialTags []string
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

	// Capability tags moved off the embedded LoopProfile onto a
	// dedicated WakeSubscription field; persist them in their own
	// column so the seed_json blob stays a pure routing profile.
	if err := database.AddColumn(db, "mqtt_wake_subscriptions", "initial_tags_json", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return nil, fmt.Errorf("migrate mqtt_wake_subscriptions schema: %w", err)
	}
	// Wake-target dispatch added in the trigger-unification work:
	// subscriptions can declare an existing loop to receive matching
	// MQTT messages instead of spawning a fresh one-shot loop.
	// Default '{}' lets older rows hydrate cleanly with an empty
	// target (falling through to the legacy spawn path).
	if err := database.AddColumn(db, "mqtt_wake_subscriptions", "wake_target_json", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return nil, fmt.Errorf("migrate mqtt_wake_subscriptions schema: %w", err)
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
	rows, err := s.db.Query(`SELECT id, topic, seed_json, initial_tags_json, wake_target_json, source, created_at FROM mqtt_wake_subscriptions`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var ws WakeSubscription
		var profileJSON, initialTagsJSON, wakeTargetJSON, createdAt string
		if err := rows.Scan(&ws.ID, &ws.Topic, &profileJSON, &initialTagsJSON, &wakeTargetJSON, &ws.Source, &createdAt); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(profileJSON), &ws.Profile); err != nil {
			s.logger.Warn("skipping subscription with invalid profile JSON",
				"id", ws.ID, "error", err)
			continue
		}
		if initialTagsJSON != "" {
			var tags []string
			if err := json.Unmarshal([]byte(initialTagsJSON), &tags); err != nil {
				s.logger.Warn("subscription initial_tags_json invalid, ignoring",
					"id", ws.ID, "error", err)
			} else if len(tags) > 0 {
				ws.InitialTags = tags
			}
		}
		// wake_target_json is "{}" for pre-trigger-unification rows
		// (column default). Unmarshal then Empty() detects "no
		// target configured" and dispatch falls back to the legacy
		// Profile-driven spawn path.
		if wakeTargetJSON != "" && wakeTargetJSON != "{}" {
			if err := json.Unmarshal([]byte(wakeTargetJSON), &ws.WakeTarget); err != nil {
				s.logger.Warn("subscription wake_target_json invalid, ignoring",
					"id", ws.ID, "error", err)
				ws.WakeTarget = messages.LoopWakeTarget{}
			}
		}
		ts, err := database.ParseTimestamp(createdAt)
		if err != nil {
			s.logger.Warn("skipping subscription with invalid timestamp",
				"id", ws.ID, "error", err)
			continue
		}
		ws.CreatedAt = ts

		// Validate persisted rows against current rules — older rows
		// may predate the validation added in Add(). Skip invalid
		// entries rather than letting them cause SUBSCRIBE failures
		// or unexpected matching at runtime.
		if err := router.ValidateTopicFilter(ws.Topic); err != nil {
			s.logger.Warn("skipping persisted subscription with invalid topic",
				"id", ws.ID, "topic", ws.Topic, "error", err)
			continue
		}
		if err := ws.Profile.Validate(); err != nil {
			s.logger.Warn("skipping persisted subscription with invalid profile",
				"id", ws.ID, "topic", ws.Topic, "error", err)
			continue
		}

		s.subs = append(s.subs, ws)
	}

	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	return nil
}

// LoadConfig loads config-defined wake subscriptions. Only entries with
// a non-nil Wake field are loaded. Config subscriptions are not persisted
// to SQLite and cannot be removed via [SubscriptionStore.Remove].
// Returns an error if any wake subscription has an invalid topic filter
// or profile — config-backed triggers should fail at startup rather than
// silently dropping messages at runtime.
func (s *SubscriptionStore) LoadConfig(subs []config.SubscriptionConfig) error {
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
		// A config entry with neither WakeLoop nor Wake is
		// ambient-awareness only; skip silently like before.
		if sc.WakeLoop == nil && sc.Wake == nil {
			continue
		}
		if err := router.ValidateTopicFilter(sc.Topic); err != nil {
			return fmt.Errorf("mqtt.subscriptions[%d]: invalid topic %q: %w", i, sc.Topic, err)
		}
		ws := WakeSubscription{
			ID:          fmt.Sprintf("cfg-%s-%d", topicHash(sc.Topic), i),
			Topic:       sc.Topic,
			InitialTags: append([]string{}, sc.InitialTags...),
			Source:      "config",
			CreatedAt:   time.Now(),
		}
		if sc.WakeLoop != nil {
			if sc.WakeLoop.Empty() {
				return fmt.Errorf("mqtt.subscriptions[%d] (topic %q): wake_loop requires loop_id or name", i, sc.Topic)
			}
			ws.WakeTarget = *sc.WakeLoop
		}
		if sc.Wake != nil {
			if err := sc.Wake.Validate(); err != nil {
				return fmt.Errorf("mqtt.subscriptions[%d] (topic %q): invalid wake profile: %w", i, sc.Topic, err)
			}
			ws.Profile = *sc.Wake
		}
		s.subs = append(s.subs, ws)
	}
	return nil
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
// returns the new subscription. The topic filter and dispatch shape
// are validated before persistence — invalid values are rejected
// early rather than stored and retried forever. A subscription that
// declares neither a WakeTarget nor a meaningful Profile is rejected
// because it would match messages and do nothing on dispatch.
func (s *SubscriptionStore) Add(req AddRequest) (WakeSubscription, error) {
	topic := strings.TrimSpace(req.Topic)
	if err := router.ValidateTopicFilter(topic); err != nil {
		return WakeSubscription{}, fmt.Errorf("invalid topic filter: %w", err)
	}
	if err := req.Profile.Validate(); err != nil {
		return WakeSubscription{}, fmt.Errorf("invalid loop profile: %w", err)
	}
	// A subscription that points at nothing is almost certainly an
	// operator/model mistake: matching messages would dispatch
	// against a zero-value Profile (no model preference, no
	// instructions) and would still spawn a one-shot loop. Reject
	// with an actionable error. LoopProfile contains slice/map
	// fields so we can't `==`-compare against a zero literal;
	// inspect each field instead.
	if req.WakeTarget.Empty() && isLoopProfileEmpty(req.Profile) {
		return WakeSubscription{}, fmt.Errorf("subscription must declare either wake_loop (preferred) or a non-empty profile")
	}

	ws := WakeSubscription{
		ID:          fmt.Sprintf("rt-%s-%d", topicHash(topic), time.Now().UnixMilli()),
		Topic:       topic,
		WakeTarget:  req.WakeTarget,
		Profile:     req.Profile,
		InitialTags: append([]string{}, req.InitialTags...),
		Source:      "runtime",
		CreatedAt:   time.Now(),
	}

	profileJSON, err := json.Marshal(ws.Profile)
	if err != nil {
		return WakeSubscription{}, fmt.Errorf("marshal profile: %w", err)
	}
	// Marshal from a non-nil slice so the column always stores a JSON
	// array (matching the column's '[]' default). json.Marshal(nil)
	// produces "null", which would be inconsistent.
	initialTagsJSON, err := json.Marshal(ws.InitialTags)
	if err != nil {
		return WakeSubscription{}, fmt.Errorf("marshal initial_tags: %w", err)
	}
	wakeTargetJSON, err := json.Marshal(ws.WakeTarget)
	if err != nil {
		return WakeSubscription{}, fmt.Errorf("marshal wake_target: %w", err)
	}

	_, err = s.db.Exec(
		`INSERT INTO mqtt_wake_subscriptions (id, topic, seed_json, initial_tags_json, wake_target_json, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ws.ID, ws.Topic, string(profileJSON), string(initialTagsJSON), string(wakeTargetJSON), ws.Source, ws.CreatedAt.Format(time.RFC3339),
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
// topic (with different LoopProfile configurations) are all returned,
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

// isLoopProfileEmpty reports whether every field on the profile is
// zero-valued. Used to detect subscriptions that declare neither a
// wake target nor a useful spawn-time routing shape (an operator
// mistake we want to fail loud on, since the subscription would
// otherwise match and silently produce a no-op spawn).
func isLoopProfileEmpty(p router.LoopProfile) bool {
	return p.Model == "" &&
		p.QualityFloor == 0 &&
		p.Mission == "" &&
		p.LocalOnly == "" &&
		p.DelegationGating == "" &&
		p.PreferSpeed == "" &&
		p.Instructions == "" &&
		len(p.ExcludeTools) == 0 &&
		len(p.ExtraHints) == 0
}
