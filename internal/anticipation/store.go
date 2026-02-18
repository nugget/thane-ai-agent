// Package anticipation provides storage and matching for agent anticipations.
// Anticipations are "things I'm expecting to happen" that bridge scheduled/event
// wakes to purpose — the agent knows *why* it woke up.
package anticipation

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Anticipation represents something the agent is expecting/waiting for.
type Anticipation struct {
	ID              string            `json:"id"`
	Description     string            `json:"description"`                // Human-readable: "Dan's flight arriving"
	Context         string            `json:"context"`                    // Injected on match: instructions/reasoning
	ContextEntities []string          `json:"context_entities,omitempty"` // Entity IDs to snapshot on wake
	Recurring       bool              `json:"recurring,omitempty"`        // true = keep firing; false = auto-resolve after wake
	CooldownSeconds int               `json:"cooldown_seconds,omitempty"` // 0 = use global default
	Trigger         Trigger           `json:"trigger"`                    // When this anticipation activates
	CreatedAt       time.Time         `json:"created_at"`
	ExpiresAt       *time.Time        `json:"expires_at,omitempty"` // nil = no expiration
	ResolvedAt      *time.Time        `json:"resolved_at,omitempty"`
	LastFiredAt     *time.Time        `json:"last_fired_at,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"` // Arbitrary k/v for matching
}

// Trigger defines conditions for when an anticipation activates.
type Trigger struct {
	// Time-based: activates after this time
	AfterTime *time.Time `json:"after_time,omitempty"`

	// State-based: activates when entity matches state
	EntityID    string `json:"entity_id,omitempty"`    // e.g., "person.dan"
	EntityState string `json:"entity_state,omitempty"` // e.g., "home" or state to match

	// Zone-based: activates on zone transition
	Zone       string `json:"zone,omitempty"`        // e.g., "airport"
	ZoneAction string `json:"zone_action,omitempty"` // "enter" or "leave"

	// Event-based: activates on specific event type
	EventType string `json:"event_type,omitempty"` // e.g., "presence_change"

	// Custom expression (future: CEL or simple DSL)
	Expression string `json:"expression,omitempty"`
}

// Store manages anticipation persistence.
type Store struct {
	db *sql.DB
}

// NewStore creates a new anticipation store.
func NewStore(db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate anticipations: %w", err)
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS anticipations (
			id TEXT PRIMARY KEY,
			description TEXT NOT NULL,
			context TEXT NOT NULL,
			trigger_json TEXT NOT NULL,
			metadata_json TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			expires_at TIMESTAMP,
			resolved_at TIMESTAMP,
			deleted_at TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_anticipations_active
			ON anticipations(resolved_at, deleted_at, expires_at);
	`)
	if err != nil {
		return err
	}

	// Additive migrations: each column may already exist from a previous
	// run. Only the "duplicate column name" error is ignored — other
	// failures (locked/corrupt DB) surface immediately.
	for _, stmt := range []struct {
		sql  string
		desc string
	}{
		{`ALTER TABLE anticipations ADD COLUMN context_entities_json TEXT`, "context_entities_json"},
		{`ALTER TABLE anticipations ADD COLUMN recurring BOOLEAN DEFAULT 0`, "recurring"},
		{`ALTER TABLE anticipations ADD COLUMN cooldown_seconds INTEGER DEFAULT 0`, "cooldown_seconds"},
		{`ALTER TABLE anticipations ADD COLUMN last_fired_at TIMESTAMP`, "last_fired_at"},
	} {
		if _, err := s.db.Exec(stmt.sql); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				return fmt.Errorf("migrate %s: %w", stmt.desc, err)
			}
		}
	}

	return nil
}

// Create adds a new anticipation.
func (s *Store) Create(a *Anticipation) error {
	if a.ID == "" {
		a.ID = fmt.Sprintf("ant_%d", time.Now().UnixNano())
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	} else {
		a.CreatedAt = a.CreatedAt.UTC()
	}
	if a.ExpiresAt != nil {
		utc := a.ExpiresAt.UTC()
		a.ExpiresAt = &utc
	}
	if a.Trigger.AfterTime != nil {
		utc := a.Trigger.AfterTime.UTC()
		a.Trigger.AfterTime = &utc
	}

	triggerJSON, err := json.Marshal(a.Trigger)
	if err != nil {
		return fmt.Errorf("marshal trigger: %w", err)
	}

	var metadataJSON []byte
	if len(a.Metadata) > 0 {
		metadataJSON, _ = json.Marshal(a.Metadata)
	}

	var contextEntitiesJSON []byte
	if len(a.ContextEntities) > 0 {
		contextEntitiesJSON, _ = json.Marshal(a.ContextEntities)
	}

	_, err = s.db.Exec(`
		INSERT INTO anticipations (id, description, context, trigger_json, metadata_json, context_entities_json, recurring, cooldown_seconds, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, a.ID, a.Description, a.Context, string(triggerJSON), string(metadataJSON), string(contextEntitiesJSON), a.Recurring, a.CooldownSeconds, a.CreatedAt, a.ExpiresAt)

	return err
}

// Active returns all non-resolved, non-expired, non-deleted anticipations.
func (s *Store) Active() ([]*Anticipation, error) {
	now := time.Now().UTC()
	rows, err := s.db.Query(`
		SELECT id, description, context, trigger_json, metadata_json, context_entities_json, recurring, cooldown_seconds, created_at, expires_at, last_fired_at
		FROM anticipations
		WHERE resolved_at IS NULL
		  AND deleted_at IS NULL
		  AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY created_at ASC
	`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanAnticipations(rows)
}

// Get retrieves a single anticipation by ID.
func (s *Store) Get(id string) (*Anticipation, error) {
	row := s.db.QueryRow(`
		SELECT id, description, context, trigger_json, metadata_json, context_entities_json, recurring, cooldown_seconds, created_at, expires_at, resolved_at, last_fired_at
		FROM anticipations
		WHERE id = ? AND deleted_at IS NULL
	`, id)

	a := &Anticipation{}
	var triggerJSON, metadataJSON, contextEntitiesJSON sql.NullString
	var expiresAt, resolvedAt, lastFiredAt sql.NullTime

	err := row.Scan(&a.ID, &a.Description, &a.Context, &triggerJSON, &metadataJSON,
		&contextEntitiesJSON, &a.Recurring, &a.CooldownSeconds, &a.CreatedAt, &expiresAt, &resolvedAt, &lastFiredAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if triggerJSON.Valid {
		_ = json.Unmarshal([]byte(triggerJSON.String), &a.Trigger)
	}
	if metadataJSON.Valid && metadataJSON.String != "" {
		_ = json.Unmarshal([]byte(metadataJSON.String), &a.Metadata)
	}
	if contextEntitiesJSON.Valid && contextEntitiesJSON.String != "" {
		_ = json.Unmarshal([]byte(contextEntitiesJSON.String), &a.ContextEntities)
	}
	if expiresAt.Valid {
		a.ExpiresAt = &expiresAt.Time
	}
	if resolvedAt.Valid {
		a.ResolvedAt = &resolvedAt.Time
	}
	if lastFiredAt.Valid {
		a.LastFiredAt = &lastFiredAt.Time
	}

	return a, nil
}

// Resolve marks an anticipation as resolved (it happened).
func (s *Store) Resolve(id string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE anticipations SET resolved_at = ? WHERE id = ?
	`, now, id)
	return err
}

// Delete soft-deletes an anticipation.
func (s *Store) Delete(id string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE anticipations SET deleted_at = ? WHERE id = ?
	`, now, id)
	return err
}

// MarkFired records the current time as the last fire time for the
// anticipation. This persists across restarts unlike the previous
// in-memory tracking.
func (s *Store) MarkFired(id string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE anticipations SET last_fired_at = ? WHERE id = ?
	`, now, id)
	return err
}

// OnCooldown reports whether the anticipation has fired too recently.
// If the anticipation has a per-row cooldown_seconds > 0, that value
// is used; otherwise globalDefault applies. Returns false, nil if the
// anticipation has never fired or does not exist. Database errors other
// than sql.ErrNoRows are returned so the caller can decide how to
// handle them.
func (s *Store) OnCooldown(id string, globalDefault time.Duration) (bool, error) {
	var cooldownSec int
	var lastFired sql.NullTime

	err := s.db.QueryRow(`
		SELECT cooldown_seconds, last_fired_at FROM anticipations WHERE id = ?
	`, id).Scan(&cooldownSec, &lastFired)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query cooldown for %s: %w", id, err)
	}
	if !lastFired.Valid {
		return false, nil
	}

	cooldown := globalDefault
	if cooldownSec > 0 {
		cooldown = time.Duration(cooldownSec) * time.Second
	}
	return time.Since(lastFired.Time) < cooldown, nil
}

func (s *Store) scanAnticipations(rows *sql.Rows) ([]*Anticipation, error) {
	var result []*Anticipation
	for rows.Next() {
		a := &Anticipation{}
		var triggerJSON, metadataJSON, contextEntitiesJSON sql.NullString
		var expiresAt, lastFiredAt sql.NullTime

		err := rows.Scan(&a.ID, &a.Description, &a.Context, &triggerJSON, &metadataJSON,
			&contextEntitiesJSON, &a.Recurring, &a.CooldownSeconds, &a.CreatedAt, &expiresAt, &lastFiredAt)
		if err != nil {
			return nil, err
		}

		if triggerJSON.Valid {
			_ = json.Unmarshal([]byte(triggerJSON.String), &a.Trigger)
		}
		if metadataJSON.Valid && metadataJSON.String != "" {
			_ = json.Unmarshal([]byte(metadataJSON.String), &a.Metadata)
		}
		if contextEntitiesJSON.Valid && contextEntitiesJSON.String != "" {
			_ = json.Unmarshal([]byte(contextEntitiesJSON.String), &a.ContextEntities)
		}
		if expiresAt.Valid {
			a.ExpiresAt = &expiresAt.Time
		}
		if lastFiredAt.Valid {
			a.LastFiredAt = &lastFiredAt.Time
		}

		result = append(result, a)
	}
	return result, rows.Err()
}

// WakeContext represents the current state when the agent wakes.
type WakeContext struct {
	Time        time.Time
	EventType   string            // What triggered the wake: "cron", "presence", "state_change", etc.
	EntityID    string            // Relevant entity if any
	EntityState string            // Current state of that entity
	Zone        string            // Zone involved if presence event
	ZoneAction  string            // "enter" or "leave"
	Metadata    map[string]string // Additional context
}

// Match checks which active anticipations match the current wake context.
// Returns matched anticipations with their injected context.
func (s *Store) Match(ctx WakeContext) ([]*Anticipation, error) {
	active, err := s.Active()
	if err != nil {
		return nil, err
	}

	var matched []*Anticipation
	for _, a := range active {
		if s.matches(a, ctx) {
			matched = append(matched, a)
		}
	}
	return matched, nil
}

func (s *Store) matches(a *Anticipation, ctx WakeContext) bool {
	t := a.Trigger

	// Time-based: must be after trigger time
	if t.AfterTime != nil && ctx.Time.Before(*t.AfterTime) {
		return false
	}

	// Entity state match
	if t.EntityID != "" {
		if ctx.EntityID != t.EntityID {
			return false
		}
		if t.EntityState != "" && !strings.EqualFold(ctx.EntityState, t.EntityState) {
			return false
		}
	}

	// Zone match
	if t.Zone != "" {
		if !strings.EqualFold(ctx.Zone, t.Zone) {
			return false
		}
		if t.ZoneAction != "" && !strings.EqualFold(ctx.ZoneAction, t.ZoneAction) {
			return false
		}
	}

	// Event type match
	if t.EventType != "" && !strings.EqualFold(ctx.EventType, t.EventType) {
		return false
	}

	// If we have any trigger conditions and passed them all, match
	// If no conditions at all, only match on time
	hasConditions := t.AfterTime != nil || t.EntityID != "" || t.Zone != "" || t.EventType != ""
	if !hasConditions {
		return false // Anticipation with no triggers never matches
	}

	return true
}

// FormatMatchedContext builds the context injection text for matched anticipations.
func FormatMatchedContext(matched []*Anticipation) string {
	if len(matched) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Active Anticipations\n\n")
	sb.WriteString("You previously set up these anticipations that match the current wake:\n\n")

	for _, a := range matched {
		sb.WriteString(fmt.Sprintf("### %s\n", a.Description))
		sb.WriteString(fmt.Sprintf("*Created: %s*\n\n", a.CreatedAt.Format("2006-01-02 15:04")))
		sb.WriteString(a.Context)
		sb.WriteString("\n\n")
	}

	sb.WriteString("---\n")
	sb.WriteString("Consider resolving anticipations that have been fulfilled.\n")

	return sb.String()
}
