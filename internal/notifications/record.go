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
)

// Action represents a single action button on a notification.
type Action struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Record tracks an actionable notification from creation through
// response or expiry. It is the central data type for the HITL
// callback routing system.
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
// do not already exist.
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
	_, err := s.db.Exec(ddl)
	return err
}

// Create inserts a new notification record. The record's Status is
// set to [StatusPending] regardless of the caller's value.
func (s *RecordStore) Create(r *Record) error {
	actionsJSON, err := json.Marshal(r.Actions)
	if err != nil {
		return fmt.Errorf("marshal actions: %w", err)
	}
	_, err = s.db.Exec(`
INSERT INTO notification_records
    (request_id, recipient, origin_session, origin_conversation, context,
     actions_json, timeout_seconds, timeout_action, status, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		r.RequestID, r.Recipient, r.OriginSession, r.OriginConversation,
		r.Context, string(actionsJSON), r.TimeoutSeconds, r.TimeoutAction,
		r.CreatedAt, r.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("insert notification record: %w", err)
	}
	return nil
}

// Get retrieves a notification record by request ID. Returns
// [sql.ErrNoRows] if no record is found.
func (s *RecordStore) Get(requestID string) (*Record, error) {
	row := s.db.QueryRow(`
SELECT request_id, recipient, origin_session, origin_conversation, context,
       actions_json, timeout_seconds, timeout_action, status, response_action,
       responded_at, created_at, expires_at
FROM notification_records
WHERE request_id = ?`, requestID)

	var r Record
	var actionsJSON string
	var respondedAt sql.NullTime
	err := row.Scan(
		&r.RequestID, &r.Recipient, &r.OriginSession, &r.OriginConversation,
		&r.Context, &actionsJSON, &r.TimeoutSeconds, &r.TimeoutAction,
		&r.Status, &r.ResponseAction, &respondedAt, &r.CreatedAt, &r.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}
	if respondedAt.Valid {
		r.RespondedAt = respondedAt.Time
	}
	if err := json.Unmarshal([]byte(actionsJSON), &r.Actions); err != nil {
		return nil, fmt.Errorf("unmarshal actions: %w", err)
	}
	return &r, nil
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
	rows, err := s.db.Query(`
SELECT request_id, recipient, origin_session, origin_conversation, context,
       actions_json, timeout_seconds, timeout_action, status, response_action,
       responded_at, created_at, expires_at
FROM notification_records
WHERE status = 'pending' AND expires_at <= ?
ORDER BY expires_at ASC`, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("query pending expired: %w", err)
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
		)
		if err != nil {
			return nil, fmt.Errorf("scan pending expired row: %w", err)
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
