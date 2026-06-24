package mqtt

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// DefaultHandlerLoopName is the name of the built-in event-driven loop
// that receives MQTT wake events when a subscription does not declare
// a custom wake_loop target. It is auto-started at boot via the loop
// definition runtime whenever MQTT is configured, and is the migration
// landing zone for legacy inline-Profile subscriptions.
const DefaultHandlerLoopName = "mqtt-default-handler"

// WakeSubscription pairs an MQTT topic filter with a target loop
// that receives matching messages as event-source notifications.
// Matching messages produce a [messages.LoopEventPayload] delivered
// to the target loop's pending notifications via
// [messages.NewEventSourceEnvelope]; the target loop's next iteration
// sees the MQTT payload as an event-source wake and runs under its
// own Spec.Profile / SupervisorProfile / container cascade. No new
// loop is spawned; the registry stays clean.
//
// Operators that don't declare a custom wake_loop are migrated onto
// the built-in [DefaultHandlerLoopName] event-driven loop at config
// load and DB hydrate time.
type WakeSubscription struct {
	ID    string `json:"id"`
	Topic string `json:"topic"`

	// WakeTarget identifies an existing loop to receive matching
	// MQTT messages as event-source notifications. Never empty after
	// validation: subscriptions without a wake_loop are rejected at
	// the tool surface and auto-migrated onto the default handler at
	// load time.
	WakeTarget messages.LoopWakeTarget `json:"wake_loop"`

	Source    string    `json:"source"` // "config" or "runtime"
	CreatedAt time.Time `json:"created_at"`
}

// AddRequest is the parameter struct for [SubscriptionStore.Add]. A
// non-empty WakeTarget is required; the inline-Profile spawn path was
// retired in the trigger-unification work — operators who want bespoke
// handling create their own event-driven loop and point wake_loop at it.
type AddRequest struct {
	Topic      string
	WakeTarget messages.LoopWakeTarget
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

// loadRuntime reads persisted runtime subscriptions from SQLite and
// auto-migrates legacy inline-Profile rows onto [DefaultHandlerLoopName].
// Rows that pre-date PR-T1 (no wake_target_json) get rewritten to point
// at the default handler, preserving any initial_tags as
// [messages.LoopWakeTarget.Tags] and the profile's Instructions field.
// Migrated rows are persisted back to SQLite so the migration is a
// one-shot upgrade rather than a per-boot rewrite.
func (s *SubscriptionStore) loadRuntime() error {
	rows, err := s.db.Query(`SELECT id, topic, seed_json, initial_tags_json, wake_target_json, source, created_at FROM mqtt_wake_subscriptions`)
	if err != nil {
		return err
	}
	// rows.Close is idempotent so deferring is safe even though the
	// happy path closes the cursor explicitly below (some drivers
	// block UPDATEs while a SELECT cursor is open, hence the early
	// close). The defer is the safety net for the rows.Scan/Err
	// early-return paths.
	defer rows.Close()

	type loadedRow struct {
		ws         WakeSubscription
		needsWrite bool
		newTarget  messages.LoopWakeTarget
	}
	var loaded []loadedRow

	for rows.Next() {
		var ws WakeSubscription
		var seedJSON, initialTagsJSON, wakeTargetJSON, createdAt string
		if err := rows.Scan(&ws.ID, &ws.Topic, &seedJSON, &initialTagsJSON, &wakeTargetJSON, &ws.Source, &createdAt); err != nil {
			return err
		}
		ts, err := database.ParseTimestamp(createdAt)
		if err != nil {
			s.logger.Warn("skipping persisted subscription with invalid timestamp",
				"id", ws.ID, "error", err)
			continue
		}
		ws.CreatedAt = ts
		if err := router.ValidateTopicFilter(ws.Topic); err != nil {
			s.logger.Warn("skipping persisted subscription with invalid topic",
				"id", ws.ID, "topic", ws.Topic, "error", err)
			continue
		}

		row := loadedRow{ws: ws}
		// wake_target_json is "{}" for pre-trigger-unification rows
		// (the column default added by the PR-T1 migration). Detect
		// either form and rewrite onto the default handler.
		hasStoredTarget := wakeTargetJSON != "" && wakeTargetJSON != "{}"
		if hasStoredTarget {
			if err := json.Unmarshal([]byte(wakeTargetJSON), &row.ws.WakeTarget); err != nil {
				s.logger.Warn("subscription wake_target_json invalid, migrating onto default handler",
					"id", ws.ID, "topic", ws.Topic, "error", err)
				hasStoredTarget = false
			}
		}
		if !hasStoredTarget || row.ws.WakeTarget.Empty() {
			row.newTarget = migrateLegacyMQTTSubscription(seedJSON, initialTagsJSON)
			row.ws.WakeTarget = row.newTarget
			row.needsWrite = true
			s.logger.Warn("migrating legacy mqtt wake subscription onto default handler",
				"id", row.ws.ID,
				"topic", row.ws.Topic,
				"default_handler", DefaultHandlerLoopName,
				"migrated_tags", row.ws.WakeTarget.Tags,
			)
		}
		loaded = append(loaded, row)
	}

	if err := rows.Err(); err != nil {
		return err
	}
	// Release the cursor before running UPDATE statements — some
	// drivers reject DML while a SELECT cursor is still open. The
	// deferred Close above runs again on return; sql.Rows.Close is
	// idempotent so the second call is a no-op.
	if err := rows.Close(); err != nil {
		return err
	}

	// Persist migrated targets after the query is fully drained.
	// Migration failures here are not fatal; the next boot will retry
	// the same rewrite.
	for _, row := range loaded {
		if row.needsWrite {
			blob, err := json.Marshal(row.ws.WakeTarget)
			if err != nil {
				s.logger.Warn("failed to marshal migrated wake target",
					"id", row.ws.ID, "error", err)
				continue
			}
			if _, err := s.db.Exec(
				`UPDATE mqtt_wake_subscriptions SET wake_target_json = ?, seed_json = '{}', initial_tags_json = '[]' WHERE id = ?`,
				string(blob), row.ws.ID,
			); err != nil {
				s.logger.Warn("failed to persist migrated wake target — will retry on next boot",
					"id", row.ws.ID, "error", err)
			}
		}
		s.subs = append(s.subs, row.ws)
	}

	return nil
}

// migrateLegacyMQTTSubscription constructs a default-handler wake
// target from a legacy row's stored profile JSON and initial tags JSON.
// Both blobs are tolerated being invalid or empty — the migration's job
// is to keep the subscription firing into *something* rather than
// silently drop it, so we extract whatever signal is salvageable (the
// operator's Instructions text, their iteration-scoped tags) and
// otherwise hand the model the bare topic + payload to triage.
func migrateLegacyMQTTSubscription(seedJSON, initialTagsJSON string) messages.LoopWakeTarget {
	target := messages.LoopWakeTarget{Name: DefaultHandlerLoopName}
	if seedJSON != "" {
		var legacy router.LoopProfile
		if err := json.Unmarshal([]byte(seedJSON), &legacy); err == nil {
			if trimmed := strings.TrimSpace(legacy.Instructions); trimmed != "" {
				target.Instructions = trimmed
			}
		}
	}
	if initialTagsJSON != "" {
		var tags []string
		if err := json.Unmarshal([]byte(initialTagsJSON), &tags); err == nil {
			cleaned := make([]string, 0, len(tags))
			for _, tag := range tags {
				if t := strings.TrimSpace(tag); t != "" {
					cleaned = append(cleaned, t)
				}
			}
			if len(cleaned) > 0 {
				target.Tags = cleaned
			}
		}
	}
	return target
}

// LoadConfig loads config-defined wake subscriptions. Only entries
// with a non-nil WakeLoop or legacy Wake field are loaded; entries
// with neither are ambient-awareness only and skipped. Legacy Wake +
// InitialTags entries are auto-migrated onto [DefaultHandlerLoopName]
// with a WARN log so operators see the deprecation. Config
// subscriptions are not persisted to SQLite and cannot be removed via
// [SubscriptionStore.Remove]. Returns an error if any wake
// subscription has an invalid topic filter or wake_loop target —
// config-backed triggers should fail at startup rather than silently
// dropping messages at runtime.
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
		if sc.WakeLoop == nil && sc.Wake == nil {
			continue
		}
		if err := router.ValidateTopicFilter(sc.Topic); err != nil {
			return fmt.Errorf("mqtt.subscriptions[%d]: invalid topic %q: %w", i, sc.Topic, err)
		}
		ws := WakeSubscription{
			ID:        fmt.Sprintf("cfg-%s-%d", topicHash(sc.Topic), i),
			Topic:     sc.Topic,
			Source:    "config",
			CreatedAt: time.Now(),
		}
		switch {
		case sc.WakeLoop != nil:
			if sc.WakeLoop.Empty() {
				return fmt.Errorf("mqtt.subscriptions[%d] (topic %q): wake_loop requires loop_id or name", i, sc.Topic)
			}
			ws.WakeTarget = *sc.WakeLoop
			// Config-level initial_tags merge into the iteration-
			// scoped tag set carried on the wake envelope.
			if len(sc.InitialTags) > 0 {
				ws.WakeTarget.Tags = mergeUniqueTags(ws.WakeTarget.Tags, sc.InitialTags)
			}
		default:
			// Legacy inline-Profile entry. Migrate onto the default
			// handler so operators upgrade in place without losing
			// their subscription firing.
			ws.WakeTarget = messages.LoopWakeTarget{Name: DefaultHandlerLoopName}
			if sc.Wake != nil {
				if instructions := strings.TrimSpace(sc.Wake.Instructions); instructions != "" {
					ws.WakeTarget.Instructions = instructions
				}
			}
			if len(sc.InitialTags) > 0 {
				ws.WakeTarget.Tags = mergeUniqueTags(nil, sc.InitialTags)
			}
			s.logger.Warn("migrating legacy mqtt subscription config entry onto default handler",
				"index", i,
				"topic", sc.Topic,
				"default_handler", DefaultHandlerLoopName,
				"migrated_tags", ws.WakeTarget.Tags,
			)
		}
		s.subs = append(s.subs, ws)
	}
	return nil
}

// mergeUniqueTags returns the deduplicated union of two tag slices,
// preserving the order of the first slice and appending unique tags
// from the second. Empty strings are dropped.
func mergeUniqueTags(base, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, tag := range base {
		t := strings.TrimSpace(tag)
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	for _, tag := range extra {
		t := strings.TrimSpace(tag)
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

// VerifyTargets checks that every loaded subscription's wake target
// resolves against the supplied loop resolver. Intended to run after
// the live loop registry has finished hydrating (typically right after
// [loopDefinitionRuntime.StartEnabledServices]) so config-declared
// targets fail startup loud instead of silently dropping the first
// matching message at delivery time.
//
// Runtime tool adds already verify at the moment of Add — this method
// closes the gap on config-defined entries, which load before any loop
// is registered. A nil resolver is a no-op so test wiring without a
// registry stays simple.
//
// Only config-declared targets are fatal. A runtime subscription whose
// target loop has since been deleted or disabled is an orphaned-data
// condition: it is warned and skipped, never fatal, so one stale row
// can't keep the agent from booting.
func (s *SubscriptionStore) VerifyTargets(resolver messages.LoopResolver) error {
	if resolver == nil {
		return nil
	}
	s.mu.RLock()
	subs := make([]WakeSubscription, len(s.subs))
	copy(subs, s.subs)
	s.mu.RUnlock()

	for _, ws := range subs {
		err := messages.VerifyLoopWakeTarget(ws.WakeTarget, resolver)
		if err == nil {
			continue
		}
		// Config-declared targets are operator-authored, so an unresolved
		// one is a config error worth failing startup loudly — that's the
		// gap this pass exists to close.
		if ws.Source == "config" {
			return fmt.Errorf("mqtt subscription %q (topic %q, source=%s) wake_loop unresolved: %w", ws.ID, ws.Topic, ws.Source, err)
		}
		// A runtime subscription can outlive its target loop: the loop was
		// deleted or disabled after the subscription was persisted. That is
		// an orphaned-data condition, not a config error, and must not crash
		// the whole agent at startup. Warn and skip — it simply won't
		// dispatch, and self-heals if the loop comes back.
		s.logger.Warn("mqtt wake subscription targets a loop that is not running; skipping",
			"id", ws.ID, "topic", ws.Topic, "source", ws.Source, "error", err.Error())
	}
	return nil
}

// Add creates a runtime wake subscription, persists it to SQLite, and
// returns the new subscription. A non-empty WakeTarget is required —
// the inline-Profile spawn path was retired in the trigger-unification
// work, so a runtime subscription that points at nothing has no
// dispatch route at all.
func (s *SubscriptionStore) Add(req AddRequest) (WakeSubscription, error) {
	topic := strings.TrimSpace(req.Topic)
	if err := router.ValidateTopicFilter(topic); err != nil {
		return WakeSubscription{}, fmt.Errorf("invalid topic filter: %w", err)
	}
	if req.WakeTarget.Empty() {
		return WakeSubscription{}, fmt.Errorf("subscription requires a wake_loop target (loop_id or name)")
	}

	ws := WakeSubscription{
		ID:         fmt.Sprintf("rt-%s-%d", topicHash(topic), time.Now().UnixMilli()),
		Topic:      topic,
		WakeTarget: req.WakeTarget,
		Source:     "runtime",
		CreatedAt:  time.Now(),
	}

	wakeTargetJSON, err := json.Marshal(ws.WakeTarget)
	if err != nil {
		return WakeSubscription{}, fmt.Errorf("marshal wake_target: %w", err)
	}

	// seed_json and initial_tags_json columns are no-op vestigial
	// after the legacy spawn path was retired. Insert deterministic
	// empty-JSON values rather than dropping the columns to keep the
	// schema compatible with operators who downgrade after upgrading
	// — they'd still see migrated rows with hollow legacy fields.
	_, err = s.db.Exec(
		`INSERT INTO mqtt_wake_subscriptions (id, topic, seed_json, initial_tags_json, wake_target_json, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ws.ID, ws.Topic, "{}", "[]", string(wakeTargetJSON), ws.Source, ws.CreatedAt.Format(time.RFC3339),
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

// RemoveByWakeLoop deletes every runtime subscription whose wake target
// names the given loop, so deleting a loop doesn't leave orphaned wake
// rows that would later fail (or, before that resilience landed, crash)
// startup verification. It returns the removed runtime subscriptions and,
// separately, any config-sourced subscriptions that also target the loop —
// those are NOT deleted (config is the source of truth), but the caller can
// warn that config still references a now-deleted loop.
//
// Matching is by wake-target name, the durable key a subscription uses to
// reach a loop definition; loop_id targets an ephemeral instance and is not
// matched here.
func (s *SubscriptionStore) RemoveByWakeLoop(loopName string) (removed, configRefs []WakeSubscription, err error) {
	loopName = strings.TrimSpace(loopName)
	if loopName == "" {
		return nil, nil, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	kept := make([]WakeSubscription, 0, len(s.subs))
	for _, ws := range s.subs {
		if strings.TrimSpace(ws.WakeTarget.Name) != loopName {
			kept = append(kept, ws)
			continue
		}
		if ws.Source == "config" {
			// Config owns this row; surface it but leave it in place.
			configRefs = append(configRefs, ws)
			kept = append(kept, ws)
			continue
		}
		if _, derr := s.db.Exec(`DELETE FROM mqtt_wake_subscriptions WHERE id = ?`, ws.ID); derr != nil {
			err = errors.Join(err, fmt.Errorf("delete subscription %q: %w", ws.ID, derr))
			kept = append(kept, ws)
			continue
		}
		removed = append(removed, ws)
		s.logger.Info("removed orphaned mqtt wake subscription on loop delete",
			"id", ws.ID, "topic", ws.Topic, "loop", loopName)
	}
	s.subs = kept
	return removed, configRefs, err
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
