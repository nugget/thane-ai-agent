package homeassistant

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/router"
)

// WakeSubscription binds an MQTT topic to an agent wake with pre-loaded
// context and routing configuration. When a message arrives on the
// subscribed topic, the wake handler resolves KB context from the seed
// and invokes the agent loop.
type WakeSubscription struct {
	ID          string          `json:"id"`         // Internal ID: "wake_{nanotime}"
	Topic       string          `json:"topic"`      // MQTT topic filter (supports wildcards)
	Name        string          `json:"name"`       // Human-readable description
	KBRef       string          `json:"kb_ref"`     // KB fact key or file path
	Context     string          `json:"context"`    // Wake instructions
	Seed        router.LoopSeed `json:"seed"`       // Routing, model, capabilities
	Enabled     bool            `json:"enabled"`    // Whether the subscription is active
	FireCount   int64           `json:"fire_count"` // Total fires
	LastFiredAt *time.Time      `json:"last_fired_at,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// WakeStore manages wake subscription persistence in SQLite.
type WakeStore struct {
	db *sql.DB
}

// NewWakeStore creates a wake store backed by the given database.
func NewWakeStore(db *sql.DB) (*WakeStore, error) {
	s := &WakeStore{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate wake_subscriptions: %w", err)
	}
	return s, nil
}

func (s *WakeStore) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS wake_subscriptions (
			id          TEXT PRIMARY KEY,
			topic       TEXT NOT NULL,
			name        TEXT NOT NULL,
			kb_ref      TEXT NOT NULL DEFAULT '',
			context     TEXT NOT NULL DEFAULT '',
			seed_json   TEXT NOT NULL DEFAULT '{}',
			enabled     BOOLEAN NOT NULL DEFAULT 1,
			fire_count  INTEGER NOT NULL DEFAULT 0,
			last_fired_at TIMESTAMP,
			created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			deleted_at  TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_wake_subscriptions_active
			ON wake_subscriptions(deleted_at, enabled);
		CREATE INDEX IF NOT EXISTS idx_wake_subscriptions_topic
			ON wake_subscriptions(topic);
	`)
	return err
}

// Create inserts a new wake subscription.
func (s *WakeStore) Create(w *WakeSubscription) error {
	if w.ID == "" {
		w.ID = fmt.Sprintf("wake_%d", time.Now().UnixNano())
	}
	now := time.Now().UTC()
	w.CreatedAt = now
	w.UpdatedAt = now

	seedJSON, err := json.Marshal(w.Seed)
	if err != nil {
		return fmt.Errorf("marshal seed: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO wake_subscriptions (
			id, topic, name, kb_ref, context, seed_json,
			enabled, fire_count, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		w.ID, w.Topic, w.Name, w.KBRef, w.Context, string(seedJSON),
		w.Enabled,
		w.CreatedAt.Format(time.RFC3339Nano), w.UpdatedAt.Format(time.RFC3339Nano),
	)
	return err
}

// Get retrieves a wake subscription by internal ID.
func (s *WakeStore) Get(id string) (*WakeSubscription, error) {
	row := s.db.QueryRow(`SELECT `+wakeColumns+` FROM wake_subscriptions WHERE id = ? AND deleted_at IS NULL`, id)
	w, err := scanWakeSubscription(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return w, err
}

// Active returns all non-deleted wake subscriptions.
func (s *WakeStore) Active() ([]*WakeSubscription, error) {
	rows, err := s.db.Query(`SELECT ` + wakeColumns + ` FROM wake_subscriptions WHERE deleted_at IS NULL ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*WakeSubscription
	for rows.Next() {
		w, err := scanWakeSubscription(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, w)
	}
	return result, rows.Err()
}

// ActiveByTopic returns non-deleted, enabled subscriptions that match
// the given MQTT topic. Exact match only; wildcard evaluation is done
// by the caller if needed.
func (s *WakeStore) ActiveByTopic(topic string) ([]*WakeSubscription, error) {
	rows, err := s.db.Query(
		`SELECT `+wakeColumns+` FROM wake_subscriptions WHERE topic = ? AND enabled = 1 AND deleted_at IS NULL`,
		topic,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []*WakeSubscription
	for rows.Next() {
		w, err := scanWakeSubscription(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, w)
	}
	return result, rows.Err()
}

// Update modifies a non-deleted wake subscription.
func (s *WakeStore) Update(w *WakeSubscription) error {
	w.UpdatedAt = time.Now().UTC()

	seedJSON, err := json.Marshal(w.Seed)
	if err != nil {
		return fmt.Errorf("marshal seed: %w", err)
	}

	res, err := s.db.Exec(`
		UPDATE wake_subscriptions SET
			topic = ?, name = ?, kb_ref = ?, context = ?, seed_json = ?,
			enabled = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL`,
		w.Topic, w.Name, w.KBRef, w.Context, string(seedJSON),
		w.Enabled, w.UpdatedAt.Format(time.RFC3339Nano),
		w.ID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("wake subscription not found: %s", w.ID)
	}
	return nil
}

// Delete soft-deletes a wake subscription.
func (s *WakeStore) Delete(id string) error {
	res, err := s.db.Exec(
		`UPDATE wake_subscriptions SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("wake subscription not found: %s", id)
	}
	return nil
}

// RecordFire increments the fire count and updates the last fired timestamp.
func (s *WakeStore) RecordFire(id string) error {
	res, err := s.db.Exec(
		`UPDATE wake_subscriptions SET fire_count = fire_count + 1, last_fired_at = ? WHERE id = ? AND deleted_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339Nano), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("wake subscription not found: %s", id)
	}
	return nil
}

// wakeColumns lists the columns in scan order.
const wakeColumns = `id, topic, name, kb_ref, context, seed_json,
	enabled, fire_count, last_fired_at, created_at, updated_at`

// wakeScanner is satisfied by both *sql.Row and *sql.Rows.
type wakeScanner interface {
	Scan(dest ...any) error
}

func scanWakeSubscription(row wakeScanner) (*WakeSubscription, error) {
	var w WakeSubscription
	var seedJSON string
	var lastFiredAt sql.NullString
	var createdAt, updatedAt string

	err := row.Scan(
		&w.ID, &w.Topic, &w.Name, &w.KBRef, &w.Context, &seedJSON,
		&w.Enabled, &w.FireCount, &lastFiredAt, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}

	if seedJSON != "" {
		if err := json.Unmarshal([]byte(seedJSON), &w.Seed); err != nil {
			return nil, fmt.Errorf("unmarshal seed: %w", err)
		}
	}

	w.CreatedAt, _ = database.ParseTimestamp(createdAt)
	w.UpdatedAt, _ = database.ParseTimestamp(updatedAt)
	if lastFiredAt.Valid {
		t, err := database.ParseTimestamp(lastFiredAt.String)
		if err == nil {
			w.LastFiredAt = &t
		}
	}

	return &w, nil
}
