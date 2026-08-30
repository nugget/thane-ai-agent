package companion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/database"
)

var (
	// ErrObservationNotFound means no companion has ever published the
	// requested observation kind within the supplied routing scope.
	ErrObservationNotFound = errors.New("companion observation not found")
	// ErrObservationAmbiguous means more than one device matches and the
	// caller must supply account and client ID rather than guess.
	ErrObservationAmbiguous = errors.New("companion observation is ambiguous")
)

// ObservationStatus describes whether a companion currently shares an
// observation or has explicitly withdrawn it.
type ObservationStatus string

const (
	ObservationAvailable ObservationStatus = "available"
	ObservationWithdrawn ObservationStatus = "withdrawn"
)

// DeviceMetadata identifies one stable companion installation. Account is
// supplied separately by the server after authenticating the companion.
type DeviceMetadata struct {
	ClientID   string `json:"client_id"`
	ClientName string `json:"client_name,omitempty"`
	Platform   string `json:"platform,omitempty"`
	AppVersion string `json:"app_version,omitempty"`
	OSVersion  string `json:"os_version,omitempty"`
}

// ObservationEvent is one versioned snapshot or withdrawal produced by a
// companion. Payload is present only while Status is available.
type ObservationEvent struct {
	EventID       string            `json:"event_id"`
	Kind          string            `json:"kind"`
	SchemaVersion int               `json:"schema_version"`
	Status        ObservationStatus `json:"status,omitempty"`
	ObservedAt    time.Time         `json:"observed_at"`
	Payload       json.RawMessage   `json:"payload,omitempty"`
}

// ObservationBatch is the bounded upload contract accepted from companions.
type ObservationBatch struct {
	DeviceMetadata
	Events []ObservationEvent `json:"events"`
}

// IngestResult reports how many latest-value rows advanced. Ignored events
// were valid but older than, or identical to, the latest stored observation.
type IngestResult struct {
	Stored     int       `json:"stored"`
	Ignored    int       `json:"ignored"`
	ReceivedAt time.Time `json:"received_at"`
}

// DeviceRecord is the durable inventory row for one companion installation.
type DeviceRecord struct {
	Account        string
	DeviceIdentity string
	DeviceMetadata
	CapabilityManifestVersion int
	CapabilityManifest        json.RawMessage
	CapabilitiesUpdatedAt     *time.Time
	FirstSeenAt               time.Time
	LastSeenAt                time.Time
	LastConnectedAt           *time.Time
	LastDisconnectedAt        *time.Time
}

// LatestObservation is the durable latest-value record for one observation
// kind. Payload remains raw JSON so the store does not claim ownership of
// provider-versioned payload schemas.
type LatestObservation struct {
	Account        string
	DeviceIdentity string
	ClientID       string
	Kind           string
	EventID        string
	SchemaVersion  int
	Status         ObservationStatus
	ObservedAt     time.Time
	ReceivedAt     time.Time
	Payload        json.RawMessage
}

// ObservationStore persists companion inventory and latest observations in
// the caller-owned primary Thane database.
type ObservationStore struct {
	db *sql.DB
}

// NewObservationStore creates a store and applies its forward-only schema.
func NewObservationStore(db *sql.DB, logger *slog.Logger) (*ObservationStore, error) {
	if err := database.Migrate(db, observationSchema, logger); err != nil {
		return nil, err
	}
	return &ObservationStore{db: db}, nil
}

// RecordConnected upserts durable identity for a newly authenticated live
// companion. A transport disconnect later changes reachability, not identity.
func (s *ObservationStore) RecordConnected(ctx context.Context, principal ObservationPrincipal, device DeviceMetadata, at time.Time) error {
	return s.upsertDevice(ctx, principal, device, at, true, nil)
}

// RecordDisconnected records the end of a live transport without deleting the
// companion or its latest observations.
func (s *ObservationStore) RecordDisconnected(ctx context.Context, principal ObservationPrincipal, at time.Time) error {
	if principal.Account == "" || principal.DeviceIdentity == "" {
		return fmt.Errorf("companion device principal requires account and device identity")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE companion_devices
		SET last_seen_at = ?, last_disconnected_at = ?
		WHERE account = ? AND device_identity = ?`, at.UTC(), at.UTC(), principal.Account, principal.DeviceIdentity)
	if err != nil {
		return fmt.Errorf("record companion disconnect %s/%s: %w", principal.Account, principal.DeviceIdentity, err)
	}
	return nil
}

// RecordCapabilities refreshes durable identity and the latest normalized
// capability manifest after a successful live registration. Offline context
// never treats this manifest as callable; it exists for inventory provenance.
func (s *ObservationStore) RecordCapabilities(ctx context.Context, principal ObservationPrincipal, device DeviceMetadata, capabilities []Capability, at time.Time) error {
	if capabilities == nil {
		capabilities = []Capability{}
	}
	manifest, err := json.Marshal(capabilities)
	if err != nil {
		return fmt.Errorf("marshal companion capability manifest: %w", err)
	}
	return s.upsertDevice(ctx, principal, device, at, false, manifest)
}

// Ingest atomically upserts device identity and advances each observation kind
// only when the incoming event is newer. Equal timestamps use event ID as a
// deterministic tie-breaker, which makes retries idempotent without retaining
// a sensitive event history.
func (s *ObservationStore) Ingest(ctx context.Context, principal ObservationPrincipal, batch ObservationBatch, receivedAt time.Time) (IngestResult, error) {
	result := IngestResult{ReceivedAt: receivedAt.UTC()}
	if principal.Account == "" || principal.DeviceIdentity == "" {
		return result, fmt.Errorf("companion observation principal requires account and device identity")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin companion observation ingest: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := upsertDeviceTx(ctx, tx, principal, batch.DeviceMetadata, result.ReceivedAt, false, nil); err != nil {
		return result, err
	}
	for _, event := range batch.Events {
		status := event.Status
		if status == "" {
			status = ObservationAvailable
		}
		var payload any
		if status == ObservationAvailable {
			payload = string(event.Payload)
		}
		write, err := tx.ExecContext(ctx, `INSERT INTO companion_latest_observations (
				account, device_identity, kind, event_id, schema_version, status,
				observed_at, received_at, payload
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(account, device_identity, kind) DO UPDATE SET
				event_id = excluded.event_id,
				schema_version = excluded.schema_version,
				status = excluded.status,
				observed_at = excluded.observed_at,
				received_at = excluded.received_at,
				payload = excluded.payload
			WHERE excluded.observed_at > companion_latest_observations.observed_at
			   OR (excluded.observed_at = companion_latest_observations.observed_at
			       AND excluded.event_id > companion_latest_observations.event_id)`,
			principal.Account, principal.DeviceIdentity, event.Kind, event.EventID, event.SchemaVersion,
			string(status), event.ObservedAt.UTC(), result.ReceivedAt, payload)
		if err != nil {
			return result, fmt.Errorf("store companion observation %s/%s: %w", batch.ClientID, event.Kind, err)
		}
		rows, err := write.RowsAffected()
		if err != nil {
			return result, fmt.Errorf("count stored companion observation: %w", err)
		}
		if rows == 0 {
			result.Ignored++
		} else {
			result.Stored++
		}
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit companion observation ingest: %w", err)
	}
	return result, nil
}

// ListDevices returns the durable companion inventory in stable identity order.
func (s *ObservationStore) ListDevices(ctx context.Context) ([]DeviceRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		account, device_identity, client_id, client_name, platform, app_version, os_version,
		capability_manifest_version, capability_manifest, capabilities_updated_at,
		first_seen_at, last_seen_at, last_connected_at, last_disconnected_at
		FROM companion_devices ORDER BY account, device_identity`)
	if err != nil {
		return nil, fmt.Errorf("list companion devices: %w", err)
	}
	defer rows.Close()

	var devices []DeviceRecord
	for rows.Next() {
		var record DeviceRecord
		var firstSeen, lastSeen string
		var manifest sql.NullString
		var capabilitiesUpdated, connected, disconnected sql.NullString
		if err := rows.Scan(
			&record.Account, &record.DeviceIdentity, &record.ClientID, &record.ClientName,
			&record.Platform, &record.AppVersion, &record.OSVersion,
			&record.CapabilityManifestVersion, &manifest, &capabilitiesUpdated,
			&firstSeen, &lastSeen,
			&connected, &disconnected,
		); err != nil {
			return nil, fmt.Errorf("scan companion device: %w", err)
		}
		if record.FirstSeenAt, err = database.ParseTimestamp(firstSeen); err != nil {
			return nil, fmt.Errorf("parse companion first_seen_at: %w", err)
		}
		if record.LastSeenAt, err = database.ParseTimestamp(lastSeen); err != nil {
			return nil, fmt.Errorf("parse companion last_seen_at: %w", err)
		}
		if manifest.Valid {
			record.CapabilityManifest = json.RawMessage(manifest.String)
		}
		if record.CapabilitiesUpdatedAt, err = parseOptionalTimestamp(capabilitiesUpdated); err != nil {
			return nil, fmt.Errorf("parse companion capabilities_updated_at: %w", err)
		}
		if record.LastConnectedAt, err = parseOptionalTimestamp(connected); err != nil {
			return nil, fmt.Errorf("parse companion last_connected_at: %w", err)
		}
		if record.LastDisconnectedAt, err = parseOptionalTimestamp(disconnected); err != nil {
			return nil, fmt.Errorf("parse companion last_disconnected_at: %w", err)
		}
		devices = append(devices, record)
	}
	return devices, rows.Err()
}

// ListLatest returns latest observations in stable device/kind order.
func (s *ObservationStore) ListLatest(ctx context.Context) ([]LatestObservation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		o.account, o.device_identity, d.client_id, o.kind, o.event_id,
		o.schema_version, o.status, o.observed_at, o.received_at, o.payload
		FROM companion_latest_observations o
		JOIN companion_devices d
		  ON d.account = o.account AND d.device_identity = o.device_identity
		ORDER BY o.account, o.device_identity, o.kind`)
	if err != nil {
		return nil, fmt.Errorf("list companion observations: %w", err)
	}
	defer rows.Close()

	var observations []LatestObservation
	for rows.Next() {
		observation, err := scanLatestObservation(rows)
		if err != nil {
			return nil, err
		}
		observations = append(observations, observation)
	}
	return observations, rows.Err()
}

// ResolveLatest selects one latest observation by kind and optional routing
// hints. It returns a descriptive ambiguity error rather than choosing an
// operator device arbitrarily.
func (s *ObservationStore) ResolveLatest(ctx context.Context, account, clientID, kind string) (LatestObservation, error) {
	query := `SELECT o.account, o.device_identity, d.client_id, o.kind, o.event_id,
		o.schema_version, o.status, o.observed_at, o.received_at, o.payload
		FROM companion_latest_observations o
		JOIN companion_devices d
		  ON d.account = o.account AND d.device_identity = o.device_identity
		WHERE o.kind = ?`
	args := []any{kind}
	if account != "" {
		query += " AND o.account = ?"
		args = append(args, account)
	}
	if clientID != "" {
		query += " AND d.client_id = ?"
		args = append(args, clientID)
	}
	query += " ORDER BY o.account, o.device_identity"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return LatestObservation{}, fmt.Errorf("resolve companion observation: %w", err)
	}
	defer rows.Close()

	var matches []LatestObservation
	for rows.Next() {
		observation, err := scanLatestObservation(rows)
		if err != nil {
			return LatestObservation{}, err
		}
		matches = append(matches, observation)
	}
	if err := rows.Err(); err != nil {
		return LatestObservation{}, err
	}
	switch len(matches) {
	case 0:
		return LatestObservation{}, fmt.Errorf("%w: no companion has published %q for the requested account and client_id", ErrObservationNotFound, kind)
	case 1:
		return matches[0], nil
	default:
		labels := make([]string, 0, len(matches))
		for _, match := range matches {
			labels = append(labels, match.Account+"/"+match.ClientID)
		}
		sort.Strings(labels)
		return LatestObservation{}, fmt.Errorf("%w: multiple companions have %q (%s); retry with account and client_id", ErrObservationAmbiguous, kind, strings.Join(labels, ", "))
	}
}

func (s *ObservationStore) upsertDevice(
	ctx context.Context,
	principal ObservationPrincipal,
	device DeviceMetadata,
	at time.Time,
	connected bool,
	capabilityManifest json.RawMessage,
) error {
	if principal.Account == "" || principal.DeviceIdentity == "" {
		return fmt.Errorf("companion device principal requires account and device identity")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin companion device upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := upsertDeviceTx(ctx, tx, principal, device, at.UTC(), connected, capabilityManifest); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit companion device upsert: %w", err)
	}
	return nil
}

func upsertDeviceTx(
	ctx context.Context,
	tx *sql.Tx,
	principal ObservationPrincipal,
	device DeviceMetadata,
	at time.Time,
	connected bool,
	capabilityManifest json.RawMessage,
) error {
	var connectedAt any
	if connected {
		connectedAt = at
	}
	manifestVersion := 0
	var manifest any
	var capabilitiesUpdatedAt any
	if capabilityManifest != nil {
		manifestVersion = 1
		manifest = string(capabilityManifest)
		capabilitiesUpdatedAt = at
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO companion_devices (
			account, device_identity, client_id, client_name, platform, app_version,
			os_version, capability_manifest_version, capability_manifest,
			capabilities_updated_at, first_seen_at, last_seen_at, last_connected_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account, device_identity) DO UPDATE SET
			client_id = CASE WHEN excluded.client_id != '' THEN excluded.client_id ELSE companion_devices.client_id END,
			client_name = CASE WHEN excluded.client_name != '' THEN excluded.client_name ELSE companion_devices.client_name END,
			platform = CASE WHEN excluded.platform != '' THEN excluded.platform ELSE companion_devices.platform END,
			app_version = CASE WHEN excluded.app_version != '' THEN excluded.app_version ELSE companion_devices.app_version END,
			os_version = CASE WHEN excluded.os_version != '' THEN excluded.os_version ELSE companion_devices.os_version END,
			capability_manifest_version = CASE
				WHEN excluded.capability_manifest IS NOT NULL THEN excluded.capability_manifest_version
				ELSE companion_devices.capability_manifest_version
			END,
			capability_manifest = COALESCE(excluded.capability_manifest, companion_devices.capability_manifest),
			capabilities_updated_at = COALESCE(excluded.capabilities_updated_at, companion_devices.capabilities_updated_at),
			last_seen_at = excluded.last_seen_at,
			last_connected_at = COALESCE(excluded.last_connected_at, companion_devices.last_connected_at)`,
		principal.Account, principal.DeviceIdentity, device.ClientID, device.ClientName,
		device.Platform, device.AppVersion, device.OSVersion, manifestVersion, manifest,
		capabilitiesUpdatedAt, at, at, connectedAt)
	if err != nil {
		return fmt.Errorf("upsert companion device %s/%s: %w", principal.Account, principal.DeviceIdentity, err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanLatestObservation(row rowScanner) (LatestObservation, error) {
	var observation LatestObservation
	var status, observedAt, receivedAt string
	var payload sql.NullString
	if err := row.Scan(
		&observation.Account, &observation.DeviceIdentity, &observation.ClientID, &observation.Kind,
		&observation.EventID, &observation.SchemaVersion, &status,
		&observedAt, &receivedAt, &payload,
	); err != nil {
		return observation, fmt.Errorf("scan companion observation: %w", err)
	}
	observation.Status = ObservationStatus(status)
	var err error
	if observation.ObservedAt, err = database.ParseTimestamp(observedAt); err != nil {
		return observation, fmt.Errorf("parse companion observed_at: %w", err)
	}
	if observation.ReceivedAt, err = database.ParseTimestamp(receivedAt); err != nil {
		return observation, fmt.Errorf("parse companion received_at: %w", err)
	}
	if payload.Valid {
		observation.Payload = json.RawMessage(payload.String)
	}
	return observation, nil
}

func parseOptionalTimestamp(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := database.ParseTimestamp(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
