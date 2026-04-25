package notifications

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// Status constants for notification records.
const (
	StatusPending   = "pending"
	StatusResponded = "responded"
	StatusExpired   = "expired"
	StatusSent      = "sent" // fire-and-forget, no response expected
)

// Action represents a single action button on a notification.
type Action struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Kind constants for notification records.
const (
	KindFireAndForget = "fire_and_forget"
	KindActionable    = "actionable"
)

// Record tracks a notification from creation through delivery, and
// optionally through response or expiry for actionable notifications.
// Fire-and-forget records have empty Actions, zero Timeout fields,
// and Status set to [StatusSent].
type Record struct {
	RequestID          string
	Recipient          string
	OriginSession      string
	OriginConversation string
	Context            string
	Actions            []Action
	TimeoutSeconds     int
	TimeoutAction      string
	Status             string
	ResponseAction     string
	RespondedAt        time.Time
	CreatedAt          time.Time
	ExpiresAt          time.Time

	// Fields added for notification history awareness (issue #614).
	Channel string // provider name: "ha_push", "signal"
	Source  string // originating loop/conversation: "metacognitive", "signal/+1234"
	Kind    string // "fire_and_forget" or "actionable"
	Title   string // notification title
	Message string // notification body (may be truncated for display)
}

// RecordStore provides SQLite-backed CRUD for notification records.
type RecordStore struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewRecordStore creates a notification record store using the given
// database connection. The caller owns the connection — RecordStore
// does not close it. The schema is created automatically on first use.
func NewRecordStore(db *sql.DB, logger *slog.Logger) (*RecordStore, error) {
	if db == nil {
		return nil, fmt.Errorf("nil database connection")
	}
	s := &RecordStore{db: db, logger: logger}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate notifications db: %w", err)
	}
	return s, nil
}

// migrate creates the notification_records table and indexes if they
// do not already exist, and applies additive schema migrations.
func (s *RecordStore) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS notification_records (
    request_id          TEXT PRIMARY KEY,
    recipient           TEXT NOT NULL,
    origin_session      TEXT NOT NULL DEFAULT '',
    origin_conversation TEXT NOT NULL DEFAULT '',
    context             TEXT NOT NULL DEFAULT '',
    actions_json        TEXT NOT NULL DEFAULT '[]',
    timeout_seconds     INTEGER NOT NULL DEFAULT 0,
    timeout_action      TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'pending',
    response_action     TEXT NOT NULL DEFAULT '',
    responded_at        DATETIME,
    created_at          DATETIME NOT NULL,
    expires_at          DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_notif_pending_status
    ON notification_records (status) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_notif_pending_expires
    ON notification_records (expires_at) WHERE status = 'pending';
`
	if _, err := s.db.Exec(ddl); err != nil {
		return err
	}

	// Issue #614: add columns for notification history awareness.
	// These are additive-only (ALTER TABLE ADD COLUMN IF NOT EXISTS
	// is not supported by SQLite, so we check pragma table_info).
	newCols := []struct {
		name string
		ddl  string
	}{
		{"channel", `ALTER TABLE notification_records ADD COLUMN channel TEXT NOT NULL DEFAULT ''`},
		{"source", `ALTER TABLE notification_records ADD COLUMN source TEXT NOT NULL DEFAULT ''`},
		{"kind", `ALTER TABLE notification_records ADD COLUMN kind TEXT NOT NULL DEFAULT 'actionable'`},
		{"title", `ALTER TABLE notification_records ADD COLUMN title TEXT NOT NULL DEFAULT ''`},
		{"message", `ALTER TABLE notification_records ADD COLUMN message TEXT NOT NULL DEFAULT ''`},
	}

	existing := make(map[string]bool)
	rows, err := s.db.Query(`PRAGMA table_info(notification_records)`)
	if err != nil {
		return fmt.Errorf("pragma table_info: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan table_info: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate table_info: %w", err)
	}

	for _, col := range newCols {
		if existing[col.name] {
			continue
		}
		if _, err := s.db.Exec(col.ddl); err != nil {
			return fmt.Errorf("add column %s: %w", col.name, err)
		}
	}

	// Index for history queries: newest-first scan bounded by time.
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_notif_created ON notification_records (created_at)`)
	return err
}

// Create inserts a new notification record. The record's Status is
// set to [StatusPending] regardless of the caller's value. Kind
// defaults to [KindActionable] if not set.
func (s *RecordStore) Create(r *Record) error {
	actionsJSON, err := json.Marshal(r.Actions)
	if err != nil {
		return fmt.Errorf("marshal actions: %w", err)
	}
	kind := r.Kind
	if kind == "" {
		kind = KindActionable
	}
	_, err = s.db.Exec(`
INSERT INTO notification_records
    (request_id, recipient, origin_session, origin_conversation, context,
     actions_json, timeout_seconds, timeout_action, status, created_at, expires_at,
     channel, source, kind, title, message)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?)`,
		r.RequestID, r.Recipient, r.OriginSession, r.OriginConversation,
		r.Context, string(actionsJSON), r.TimeoutSeconds, r.TimeoutAction,
		r.CreatedAt, r.ExpiresAt,
		r.Channel, r.Source, kind, r.Title, r.Message,
	)
	if err != nil {
		return fmt.Errorf("insert notification record: %w", err)
	}
	return nil
}

// Log inserts a fire-and-forget notification record. Unlike [Create],
// this is for notifications that need no callback tracking — the
// record exists solely for history awareness. Status is set to
// [StatusSent] and ExpiresAt to the zero value.
func (s *RecordStore) Log(r *Record) error {
	kind := r.Kind
	if kind == "" {
		kind = KindFireAndForget
	}
	// Fire-and-forget records never expire — store zero time regardless
	// of what the caller passes to avoid ambiguity.
	_, err := s.db.Exec(`
INSERT INTO notification_records
    (request_id, recipient, origin_session, origin_conversation, context,
     actions_json, timeout_seconds, timeout_action, status, created_at, expires_at,
     channel, source, kind, title, message)
VALUES (?, ?, ?, ?, '', '[]', 0, '', ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.RequestID, r.Recipient, r.OriginSession, r.OriginConversation,
		StatusSent, r.CreatedAt, time.Time{},
		r.Channel, r.Source, kind, r.Title, r.Message,
	)
	if err != nil {
		return fmt.Errorf("log notification: %w", err)
	}
	return nil
}

// Recent returns the most recent notification records created since
// the given time, ordered newest-first. It returns both fire-and-forget
// and actionable records for history awareness.
func (s *RecordStore) Recent(since time.Time, limit int) ([]*Record, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.queryRecords(`
SELECT request_id, recipient, origin_session, origin_conversation, context,
       actions_json, timeout_seconds, timeout_action, status, response_action,
       responded_at, created_at, expires_at,
       channel, source, kind, title, message
FROM notification_records
WHERE created_at >= ?
ORDER BY created_at DESC
LIMIT ?`, since.UTC(), limit)
}

// Get retrieves a notification record by request ID. Returns
// [sql.ErrNoRows] if no record is found.
func (s *RecordStore) Get(requestID string) (*Record, error) {
	records, err := s.queryRecords(`
SELECT request_id, recipient, origin_session, origin_conversation, context,
       actions_json, timeout_seconds, timeout_action, status, response_action,
       responded_at, created_at, expires_at,
       channel, source, kind, title, message
FROM notification_records
WHERE request_id = ?`, requestID)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, sql.ErrNoRows
	}
	return records[0], nil
}

// Respond marks a pending record as responded with the given action
// ID. Returns true if the record was updated (was still pending),
// false if it was already responded or expired. Callers should check
// the bool to avoid double-processing in race scenarios.
func (s *RecordStore) Respond(requestID, actionID string) (bool, error) {
	res, err := s.db.Exec(`
UPDATE notification_records
SET status = 'responded', response_action = ?, responded_at = ?
WHERE request_id = ? AND status = 'pending'`,
		actionID, time.Now().UTC(), requestID,
	)
	if err != nil {
		return false, fmt.Errorf("respond to notification: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("respond rows affected: %w", err)
	}
	return n > 0, nil
}

// Expire marks a pending record as expired. Returns true if the
// record was updated (was still pending), false if it was already
// responded or expired. Callers should check the bool to avoid
// executing timeout actions on records that were concurrently
// responded to.
func (s *RecordStore) Expire(requestID string) (bool, error) {
	res, err := s.db.Exec(`
UPDATE notification_records
SET status = 'expired'
WHERE request_id = ? AND status = 'pending'`,
		requestID,
	)
	if err != nil {
		return false, fmt.Errorf("expire notification: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("expire rows affected: %w", err)
	}
	return n > 0, nil
}

// SetResponseAction records the action that was executed for an
// expired record. This is used by the timeout watcher to persist
// which action was auto-executed on timeout, since the record has
// already transitioned from pending to expired and Respond cannot
// update it.
func (s *RecordStore) SetResponseAction(requestID, actionID string) error {
	_, err := s.db.Exec(`
UPDATE notification_records
SET response_action = ?, responded_at = ?
WHERE request_id = ?`,
		actionID, time.Now().UTC(), requestID,
	)
	if err != nil {
		return fmt.Errorf("set response action: %w", err)
	}
	return nil
}

// PendingExpired returns all records that are still pending but whose
// expiry time has passed.
func (s *RecordStore) PendingExpired() ([]*Record, error) {
	return s.queryRecords(`
SELECT request_id, recipient, origin_session, origin_conversation, context,
       actions_json, timeout_seconds, timeout_action, status, response_action,
       responded_at, created_at, expires_at,
       channel, source, kind, title, message
FROM notification_records
WHERE status = 'pending' AND expires_at <= ?
ORDER BY expires_at ASC`, time.Now().UTC())
}

// queryRecords executes a query and scans the results into Record
// structs. The query must SELECT the full 18-column set in the
// canonical order.
func (s *RecordStore) queryRecords(query string, args ...any) ([]*Record, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query notification records: %w", err)
	}
	defer rows.Close()

	var records []*Record
	for rows.Next() {
		var r Record
		var actionsJSON string
		var respondedAt sql.NullTime
		err := rows.Scan(
			&r.RequestID, &r.Recipient, &r.OriginSession, &r.OriginConversation,
			&r.Context, &actionsJSON, &r.TimeoutSeconds, &r.TimeoutAction,
			&r.Status, &r.ResponseAction, &respondedAt, &r.CreatedAt, &r.ExpiresAt,
			&r.Channel, &r.Source, &r.Kind, &r.Title, &r.Message,
		)
		if err != nil {
			return nil, fmt.Errorf("scan notification record: %w", err)
		}
		if respondedAt.Valid {
			r.RespondedAt = respondedAt.Time
		}
		if err := json.Unmarshal([]byte(actionsJSON), &r.Actions); err != nil {
			return nil, fmt.Errorf("unmarshal actions: %w", err)
		}
		records = append(records, &r)
	}
	return records, rows.Err()
}
