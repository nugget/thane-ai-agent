// Package companions persists the durable companion-device inventory:
// which operator-owned companion apps exist, when they were last seen,
// and what they most recently advertised. A device record and its live
// WebSocket connection have different lifetimes — the connection is
// ephemeral transport, the record survives disconnects and process
// restarts (#1437). Reachability is never persisted here; it stays
// derived from the in-memory provider registry.
package companions

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

// DeviceStateActive is the lifecycle state every row carries today.
// The column exists so a future forget/retire flow has somewhere to
// record its decision; nothing in this slice writes any other value.
const DeviceStateActive = "active"

// Device is one durable companion-device record.
type Device struct {
	Account    string
	ClientID   string
	ClientName string
	Platform   string
	AppVersion string
	OSVersion  string

	FirstSeenAt time.Time
	LastSeenAt  time.Time
	// LastConnectedAt and LastDisconnectedAt are zero when the event has
	// never happened (a row can exist before its first full connection
	// cycle completes).
	LastConnectedAt    time.Time
	LastDisconnectedAt time.Time

	// Capabilities is the most recently advertised capability manifest,
	// stored verbatim as JSON. It describes what the device offered when
	// last heard from — not what is callable now.
	Capabilities json.RawMessage

	State string
}

// Store persists companion devices in the primary Thane database.
type Store struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewStore creates a companion-device store, running migrations on
// first use. The db handle is borrowed (the memory store owns it).
func NewStore(db *sql.DB, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := database.Migrate(db, devicesSchema, logger); err != nil {
		return nil, err
	}
	return &Store{db: db, logger: logger}, nil
}

// RecordConnected upserts the device row for a successful companion
// authentication. A new device gets its first_seen_at anchored; a
// returning device keeps it. Metadata updates are non-empty-wins so a
// client that omits a field on one connection cannot erase what it
// reported before. Implements [companion.DeviceRecorder].
func (s *Store) RecordConnected(account, clientID string, meta companion.DeviceMetadata, at time.Time) error {
	account, clientID, err := normalizeKey(account, clientID)
	if err != nil {
		return err
	}
	at = normalizeTime(at)

	_, err = s.db.Exec(`
		INSERT INTO companion_devices (
			account, client_id, client_name, platform, app_version, os_version,
			first_seen_at, last_seen_at, last_connected_at, state
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account, client_id) DO UPDATE SET
			client_name       = CASE WHEN excluded.client_name != '' THEN excluded.client_name ELSE client_name END,
			platform          = CASE WHEN excluded.platform != '' THEN excluded.platform ELSE platform END,
			app_version       = CASE WHEN excluded.app_version != '' THEN excluded.app_version ELSE app_version END,
			os_version        = CASE WHEN excluded.os_version != '' THEN excluded.os_version ELSE os_version END,
			last_seen_at      = excluded.last_seen_at,
			last_connected_at = excluded.last_connected_at
	`, account, clientID,
		strings.TrimSpace(meta.ClientName), strings.TrimSpace(meta.Platform),
		strings.TrimSpace(meta.AppVersion), strings.TrimSpace(meta.OSVersion),
		at, at, at, DeviceStateActive)
	if err != nil {
		return fmt.Errorf("record companion connect %s/%s: %w", account, clientID, err)
	}
	return nil
}

// RecordCapabilities stores the most recently advertised capability
// manifest for a known device and bumps last_seen_at. The manifest is
// opaque JSON authored by the registration path; an empty manifest is
// stored as an empty JSON array. Registering capabilities for a device
// that was never recorded is an error — connection recording precedes
// capability registration in the protocol. Implements
// [companion.DeviceRecorder].
func (s *Store) RecordCapabilities(account, clientID string, manifest []byte, at time.Time) error {
	account, clientID, err := normalizeKey(account, clientID)
	if err != nil {
		return err
	}
	at = normalizeTime(at)
	if len(manifest) == 0 {
		manifest = []byte("[]")
	}
	if !json.Valid(manifest) {
		return fmt.Errorf("record companion capabilities %s/%s: manifest is not valid JSON", account, clientID)
	}

	res, err := s.db.Exec(`
		UPDATE companion_devices
		SET capabilities = ?, last_seen_at = ?
		WHERE account = ? AND client_id = ?
	`, string(manifest), at, account, clientID)
	if err != nil {
		return fmt.Errorf("record companion capabilities %s/%s: %w", account, clientID, err)
	}
	return requireRow(res, "record companion capabilities", account, clientID)
}

// RecordDisconnected stamps the disconnect time for a known device. It
// only updates timestamps — a disconnect must never delete or degrade
// the record; that separation is the point of the inventory (#1437).
// Implements [companion.DeviceRecorder].
func (s *Store) RecordDisconnected(account, clientID string, at time.Time) error {
	account, clientID, err := normalizeKey(account, clientID)
	if err != nil {
		return err
	}
	at = normalizeTime(at)

	res, err := s.db.Exec(`
		UPDATE companion_devices
		SET last_disconnected_at = ?, last_seen_at = ?
		WHERE account = ? AND client_id = ?
	`, at, at, account, clientID)
	if err != nil {
		return fmt.Errorf("record companion disconnect %s/%s: %w", account, clientID, err)
	}
	return requireRow(res, "record companion disconnect", account, clientID)
}

// Get returns one device record. The boolean reports whether the
// device exists; a missing device is not an error.
func (s *Store) Get(account, clientID string) (Device, bool, error) {
	account, clientID, err := normalizeKey(account, clientID)
	if err != nil {
		return Device{}, false, err
	}
	devices, err := s.scanDevices(`
		SELECT account, client_id, client_name, platform, app_version, os_version,
		       first_seen_at, last_seen_at, last_connected_at, last_disconnected_at,
		       capabilities, state
		FROM companion_devices
		WHERE account = ? AND client_id = ?
	`, account, clientID)
	if err != nil {
		return Device{}, false, err
	}
	if len(devices) == 0 {
		return Device{}, false, nil
	}
	return devices[0], true, nil
}

// List returns every device record, ordered by account then client ID
// so consumers render a stable shape across calls.
func (s *Store) List() ([]Device, error) {
	return s.scanDevices(`
		SELECT account, client_id, client_name, platform, app_version, os_version,
		       first_seen_at, last_seen_at, last_connected_at, last_disconnected_at,
		       capabilities, state
		FROM companion_devices
		ORDER BY account ASC, client_id ASC
	`)
}

func (s *Store) scanDevices(query string, args ...any) ([]Device, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []Device
	for rows.Next() {
		var (
			d            Device
			connected    sql.NullTime
			disconnected sql.NullTime
			capsJSON     string
		)
		if err := rows.Scan(
			&d.Account, &d.ClientID, &d.ClientName, &d.Platform, &d.AppVersion, &d.OSVersion,
			&d.FirstSeenAt, &d.LastSeenAt, &connected, &disconnected,
			&capsJSON, &d.State,
		); err != nil {
			return nil, err
		}
		if connected.Valid {
			d.LastConnectedAt = connected.Time.UTC()
		}
		if disconnected.Valid {
			d.LastDisconnectedAt = disconnected.Time.UTC()
		}
		d.FirstSeenAt = d.FirstSeenAt.UTC()
		d.LastSeenAt = d.LastSeenAt.UTC()
		d.Capabilities = json.RawMessage(capsJSON)
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

func normalizeKey(account, clientID string) (string, string, error) {
	account = strings.TrimSpace(account)
	clientID = strings.TrimSpace(clientID)
	if account == "" {
		return "", "", fmt.Errorf("account is required")
	}
	if clientID == "" {
		return "", "", fmt.Errorf("client_id is required")
	}
	return account, clientID, nil
}

func normalizeTime(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now().UTC()
	}
	return at.UTC()
}

func requireRow(res sql.Result, op, account, clientID string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s %s/%s: %w", op, account, clientID, err)
	}
	if n == 0 {
		return fmt.Errorf("%s %s/%s: device not recorded", op, account, clientID)
	}
	return nil
}
