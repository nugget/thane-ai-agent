// Package companions persists the durable companion-device inventory:
// which operator-owned companion apps exist, when they were last seen,
// and what they most recently advertised. A device record and its live
// WebSocket connection have different lifetimes — the connection is
// ephemeral transport, the record survives disconnects and process
// restarts (#1437). Reachability is never persisted here; it stays
// derived from the in-memory provider registry.
//
// Overlapping connections for the same durable identity are legal (a
// reconnecting phone races its own dying socket), so every write is
// guarded to be monotonic: timestamps never regress and an older write
// cannot replace newer state. The guards compare timestamps in SQL,
// which is sound because the driver stores them in the canonical text
// layout and this store normalizes every value to UTC — uniform-offset
// canonical text compares bytewise in chronological order.
package companions

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
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
	// DeviceID is the immutable server-assigned identity, minted when
	// the device is first seen and stable across credential changes.
	// Account and ClientID are the current lookup claim mapped to it
	// (#1444's enrollment arc re-points that mapping on key rotation).
	DeviceID string

	Account    string
	ClientID   string
	ClientName string
	Platform   string
	AppVersion string
	OSVersion  string
	// MetadataRecordedAt is the server receipt time of the newest
	// non-empty device metadata accepted from any companion transport.
	MetadataRecordedAt time.Time

	// ContactID is the contact this device belongs to, empty when the
	// account is unclaimed. Stamped at registration from the operator's
	// account declaration, so a reader asks the row rather than config.
	ContactID string

	FirstSeenAt time.Time
	LastSeenAt  time.Time
	// LastConnectedAt and LastDisconnectedAt are zero when the event has
	// never happened (a row can exist before its first full connection
	// cycle completes).
	LastConnectedAt    time.Time
	LastDisconnectedAt time.Time

	// Capabilities is the most recently advertised capability manifest,
	// stored verbatim as JSON. It describes what the device offered when
	// last heard from — not what is callable now. CapabilitiesRecordedAt
	// is when that advertisement happened; zero when the device has
	// never registered capabilities.
	Capabilities           json.RawMessage
	CapabilitiesRecordedAt time.Time

	State string
}

// Store persists companion devices in the primary Thane database.
type Store struct {
	db     *sql.DB
	logger *slog.Logger

	// contactForAccount resolves an account to the contact the operator
	// declared it belongs to. Nil leaves every device unbound.
	contactForAccount func(account string) string
}

// NewStore creates a companion-device store, running migrations on
// first use. The db handle is borrowed (the memory store owns it).
func NewStore(db *sql.DB, logger *slog.Logger, opts ...StoreOption) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := database.Migrate(db, devicesSchema, logger); err != nil {
		return nil, err
	}
	store := &Store{db: db, logger: logger}
	for _, opt := range opts {
		if opt != nil {
			opt(store)
		}
	}
	return store, nil
}

// StoreOption configures optional store wiring. A construction option
// rather than a setter: the resolver is fixed for the store's life, and
// the architecture baseline counts exported mutator growth deliberately.
type StoreOption func(*Store)

// WithContactForAccount supplies the operator's account-to-contact
// declaration. A device is stamped with its account's contact when it
// registers, so a phone that pairs after boot joins its person without
// waiting for a restart.
//
// The mapping stays operator config — who a device belongs to is a
// custody decision, not something a device asserts about itself — but
// the binding it produces lives on the row, where a reader can ask for
// it without consulting config.
func WithContactForAccount(resolve func(account string) string) StoreOption {
	return func(s *Store) { s.contactForAccount = resolve }
}

// contactIDForAccount resolves an account's configured contact, or ""
// when the account is unclaimed or no resolver is wired.
func (s *Store) contactIDForAccount(account string) string {
	if s == nil || s.contactForAccount == nil {
		return ""
	}
	return strings.TrimSpace(s.contactForAccount(account))
}

// RecordConnected upserts the device row for a successful companion
// authentication. A new device gets its first_seen_at anchored; a
// returning device keeps it. Timestamps are monotonic, and metadata
// replacement is gated on the report being at least as new as the
// stored metadata — an older racing write can only fill fields that
// are still empty, never overwrite what a newer transport reported.
// Metadata is stored verbatim; the auth handshake is the one place
// values are normalized. Implements [companion.DeviceRecorder].
func (s *Store) RecordConnected(ctx context.Context, account, clientID string, meta companion.DeviceMetadata, at time.Time) error {
	if err := validateKey(account, clientID); err != nil {
		return err
	}
	at = normalizeTime(at)

	deviceID, err := generateDeviceID()
	if err != nil {
		return fmt.Errorf("record companion connect %s/%s: %w", account, clientID, err)
	}
	// The freshly minted device_id is discarded when the claim already
	// maps to a device — the conflict update never touches it.
	// first_seen_at is MIN-guarded for the same reason the others are
	// MAX-guarded: async recording can land a connection's write after
	// a later connection's, and the earliest event time is the truth.
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO companion_devices (
			device_id, account, client_id, client_name, platform, app_version, os_version, metadata_recorded_at,
			first_seen_at, last_seen_at, last_connected_at, state, contact_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account, client_id) DO UPDATE SET
			contact_id = excluded.contact_id,
			first_seen_at = MIN(companion_devices.first_seen_at, excluded.first_seen_at),
			client_name = CASE WHEN excluded.client_name != ''
					AND (companion_devices.client_name = '' OR companion_devices.metadata_recorded_at IS NULL
						OR excluded.metadata_recorded_at >= companion_devices.metadata_recorded_at)
				THEN excluded.client_name ELSE companion_devices.client_name END,
			platform = CASE WHEN excluded.platform != ''
					AND (companion_devices.platform = '' OR companion_devices.metadata_recorded_at IS NULL
						OR excluded.metadata_recorded_at >= companion_devices.metadata_recorded_at)
				THEN excluded.platform ELSE companion_devices.platform END,
			app_version = CASE WHEN excluded.app_version != ''
					AND (companion_devices.app_version = '' OR companion_devices.metadata_recorded_at IS NULL
						OR excluded.metadata_recorded_at >= companion_devices.metadata_recorded_at)
				THEN excluded.app_version ELSE companion_devices.app_version END,
			os_version = CASE WHEN excluded.os_version != ''
					AND (companion_devices.os_version = '' OR companion_devices.metadata_recorded_at IS NULL
						OR excluded.metadata_recorded_at >= companion_devices.metadata_recorded_at)
				THEN excluded.os_version ELSE companion_devices.os_version END,
			metadata_recorded_at = CASE WHEN excluded.metadata_recorded_at IS NOT NULL
					AND (companion_devices.metadata_recorded_at IS NULL
						OR excluded.metadata_recorded_at >= companion_devices.metadata_recorded_at)
				THEN excluded.metadata_recorded_at ELSE companion_devices.metadata_recorded_at END,
			last_seen_at      = MAX(companion_devices.last_seen_at, excluded.last_seen_at),
			last_connected_at = MAX(companion_devices.last_connected_at, excluded.last_connected_at)
	`, deviceID, account, clientID,
		meta.ClientName, meta.Platform, meta.AppVersion, meta.OSVersion,
		deviceMetadataTime(meta, at), at, at, at, DeviceStateActive,
		s.contactIDForAccount(account))
	if err != nil {
		return fmt.Errorf("record companion connect %s/%s: %w", account, clientID, err)
	}
	return nil
}

// RecordCapabilities stores the advertised capability manifest for a
// known device, but only when it is at least as new as the one already
// stored — overlapping providers with the same durable identity may
// race, and "most recently advertised" must survive out-of-order
// writes. A stale write is dropped silently (the newer manifest is
// already the correct outcome); an unknown device is an error, since
// connection recording precedes capability registration in the
// protocol. An empty manifest is stored as an empty JSON array.
// Implements [companion.DeviceRecorder].
func (s *Store) RecordCapabilities(ctx context.Context, account, clientID string, manifest []byte, at time.Time) error {
	if err := validateKey(account, clientID); err != nil {
		return err
	}
	at = normalizeTime(at)
	if len(manifest) == 0 {
		manifest = []byte("[]")
	}
	if !json.Valid(manifest) {
		return fmt.Errorf("record companion capabilities %s/%s: manifest is not valid JSON", account, clientID)
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE companion_devices
		SET capabilities = ?, capabilities_recorded_at = ?, last_seen_at = MAX(last_seen_at, ?)
		WHERE account = ? AND client_id = ?
		  AND (capabilities_recorded_at IS NULL OR capabilities_recorded_at <= ?)
	`, string(manifest), at, at, account, clientID, at)
	if err != nil {
		return fmt.Errorf("record companion capabilities %s/%s: %w", account, clientID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("record companion capabilities %s/%s: %w", account, clientID, err)
	}
	if n > 0 {
		return nil
	}

	var exists bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM companion_devices WHERE account = ? AND client_id = ?)`,
		account, clientID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("record companion capabilities %s/%s: %w", account, clientID, err)
	}
	if exists {
		s.logger.Debug("stale companion capability manifest ignored",
			"account", account,
			"client_id", clientID,
			"recorded_at", at,
		)
		return nil
	}
	return fmt.Errorf("record companion capabilities %s/%s: device not recorded", account, clientID)
}

// RecordDisconnected stamps the disconnect on a known device without
// deleting or degrading anything else — that separation is the point
// of the inventory (#1437). It also advances last_seen_at: the
// connection was live evidence of the device until the moment it tore
// down, so teardown is the last proof of liveness (with skew bounded
// by the heartbeat read timeout when the transport died silently).
// Both stamps are monotonic for overlapping teardowns. Implements
// [companion.DeviceRecorder].
func (s *Store) RecordDisconnected(ctx context.Context, account, clientID string, at time.Time) error {
	if err := validateKey(account, clientID); err != nil {
		return err
	}
	at = normalizeTime(at)

	res, err := s.db.ExecContext(ctx, `
		UPDATE companion_devices
		SET last_disconnected_at = MAX(COALESCE(last_disconnected_at, ?), ?),
		    last_seen_at = MAX(last_seen_at, ?)
		WHERE account = ? AND client_id = ?
	`, at, at, at, account, clientID)
	if err != nil {
		return fmt.Errorf("record companion disconnect %s/%s: %w", account, clientID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("record companion disconnect %s/%s: %w", account, clientID, err)
	}
	if n == 0 {
		return fmt.Errorf("record companion disconnect %s/%s: device not recorded", account, clientID)
	}
	return nil
}

// Get returns one device record. The boolean reports whether the
// device exists; a missing device is not an error.
func (s *Store) Get(ctx context.Context, account, clientID string) (Device, bool, error) {
	if err := validateKey(account, clientID); err != nil {
		return Device{}, false, err
	}
	devices, err := s.scanDevices(ctx, `
		SELECT device_id, account, client_id, client_name, platform, app_version, os_version,
		       metadata_recorded_at, first_seen_at, last_seen_at, last_connected_at, last_disconnected_at,
		       capabilities, capabilities_recorded_at, state, contact_id
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
func (s *Store) List(ctx context.Context) ([]Device, error) {
	return s.scanDevices(ctx, `
		SELECT device_id, account, client_id, client_name, platform, app_version, os_version,
		       metadata_recorded_at, first_seen_at, last_seen_at, last_connected_at, last_disconnected_at,
		       capabilities, capabilities_recorded_at, state, contact_id
		FROM companion_devices
		ORDER BY account ASC, client_id ASC
	`)
}

// DevicesForContact returns the active devices belonging to one
// contact, newest connection first.
//
// This is the live answer to "what companion hardware can observe this
// person right now": it reads the binding off the row, so a device that
// paired since boot is included and one whose account lost its claim is
// not. Callers previously mapped a contact to accounts through config
// and filtered the full device list, which could only be as current as
// the last restart.
func (s *Store) DevicesForContact(ctx context.Context, contactID string) ([]Device, error) {
	contactID = strings.TrimSpace(contactID)
	if contactID == "" {
		return nil, nil
	}
	return s.scanDevices(ctx, `
		SELECT device_id, account, client_id, client_name, platform, app_version, os_version,
		       metadata_recorded_at, first_seen_at, last_seen_at, last_connected_at, last_disconnected_at,
		       capabilities, capabilities_recorded_at, state, contact_id
		FROM companion_devices
		WHERE contact_id = ? AND state = ?
		ORDER BY last_seen_at DESC, account ASC, client_id ASC
	`, contactID, DeviceStateActive)
}

// ReconcileContactBindings stamps every device with its account's
// configured contact and returns how many rows changed.
//
// Registration stamps a device as it connects, which covers everything
// that pairs from now on. This covers the rest: rows written before the
// column existed, and rows whose account changed hands in config while
// the device was not connecting. Config is the source of truth in both
// directions — an account the operator no longer claims has its devices
// unbound rather than left pointing at the old contact.
func (s *Store) ReconcileContactBindings(ctx context.Context, accounts []string) (int, error) {
	changed := 0
	for _, account := range accounts {
		result, err := s.db.ExecContext(ctx,
			`UPDATE companion_devices SET contact_id = ? WHERE account = ? AND contact_id != ?`,
			s.contactIDForAccount(account), account, s.contactIDForAccount(account))
		if err != nil {
			return changed, fmt.Errorf("reconcile companion contact binding for %s: %w", account, err)
		}
		if n, err := result.RowsAffected(); err == nil {
			changed += int(n)
		}
	}
	return changed, nil
}

func (s *Store) scanDevices(ctx context.Context, query string, args ...any) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []Device
	for rows.Next() {
		var (
			d            Device
			metadataAt   sql.NullTime
			connected    sql.NullTime
			disconnected sql.NullTime
			capsAt       sql.NullTime
			capsJSON     string
		)
		if err := rows.Scan(
			&d.DeviceID, &d.Account, &d.ClientID, &d.ClientName, &d.Platform, &d.AppVersion, &d.OSVersion,
			&metadataAt, &d.FirstSeenAt, &d.LastSeenAt, &connected, &disconnected,
			&capsJSON, &capsAt, &d.State, &d.ContactID,
		); err != nil {
			return nil, err
		}
		if metadataAt.Valid {
			d.MetadataRecordedAt = metadataAt.Time.UTC()
		}
		if connected.Valid {
			d.LastConnectedAt = connected.Time.UTC()
		}
		if disconnected.Valid {
			d.LastDisconnectedAt = disconnected.Time.UTC()
		}
		if capsAt.Valid {
			d.CapabilitiesRecordedAt = capsAt.Time.UTC()
		}
		d.FirstSeenAt = d.FirstSeenAt.UTC()
		d.LastSeenAt = d.LastSeenAt.UTC()
		d.Capabilities = json.RawMessage(capsJSON)
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

func deviceMetadataTime(meta companion.DeviceMetadata, at time.Time) any {
	if meta.ClientName == "" && meta.Platform == "" && meta.AppVersion == "" && meta.OSVersion == "" {
		return nil
	}
	return at
}

// validateKey rejects identities that are empty after trimming, but
// never rewrites them: client_id is an opaque claim, and the stored
// bytes must match what the live registry holds or the durable/live
// join cannot line up. Normalization is the auth handshake's job.
func validateKey(account, clientID string) error {
	if strings.TrimSpace(account) == "" {
		return fmt.Errorf("account is required")
	}
	if strings.TrimSpace(clientID) == "" {
		return fmt.Errorf("client_id is required")
	}
	return nil
}

func normalizeTime(at time.Time) time.Time {
	if at.IsZero() {
		return time.Now().UTC()
	}
	return at.UTC()
}

// generateDeviceID mints an immutable server-assigned device identity
// with the dev_ prefix and a random hex suffix.
func generateDeviceID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate device_id: %w", err)
	}
	return "dev_" + hex.EncodeToString(b), nil
}
