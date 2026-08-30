package companions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
)

// Observation status values. A withdrawn row keeps its provenance
// (kind, times, event id) but its payload is cleared — previously
// stored data must not continue to appear available after the operator
// or the OS revokes sharing (#1437).
const (
	ObservationStatusPresent   = "present"
	ObservationStatusWithdrawn = "withdrawn"
)

// StoredObservation is the latest observation of one kind from one
// device. ObservedAt is the device's claim; ReceivedAt is the server's
// authoritative receipt time. Withdrawn rows carry an empty payload.
type StoredObservation struct {
	DeviceID      string
	Kind          string
	EventID       string
	SchemaVersion int
	ObservedAt    time.Time
	ReceivedAt    time.Time
	Withdrawn     bool
	Payload       json.RawMessage
}

// EnsureDevice resolves (account, client_id) to the immutable
// device_id, creating the inventory row if the device has never been
// seen — a companion may push its first background observation before
// it ever connects a WebSocket — and MIN/MAX-guarding the seen
// timestamps either way. It never stamps connection times: an HTTPS
// upload means "recently seen", not "connected" (#1437). Implements
// [companion.ObservationSink].
func (s *Store) EnsureDevice(ctx context.Context, account, clientID string, seenAt time.Time) (string, error) {
	if err := validateKey(account, clientID); err != nil {
		return "", err
	}
	seenAt = normalizeTime(seenAt)

	deviceID, err := generateDeviceID()
	if err != nil {
		return "", fmt.Errorf("ensure companion device %s/%s: %w", account, clientID, err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO companion_devices (
			device_id, account, client_id, first_seen_at, last_seen_at, state
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(account, client_id) DO UPDATE SET
			first_seen_at = MIN(companion_devices.first_seen_at, excluded.first_seen_at),
			last_seen_at  = MAX(companion_devices.last_seen_at, excluded.last_seen_at)
	`, deviceID, account, clientID, seenAt, seenAt, DeviceStateActive); err != nil {
		return "", fmt.Errorf("ensure companion device %s/%s: %w", account, clientID, err)
	}

	var stored string
	if err := s.db.QueryRowContext(ctx,
		`SELECT device_id FROM companion_devices WHERE account = ? AND client_id = ?`,
		account, clientID,
	).Scan(&stored); err != nil {
		return "", fmt.Errorf("ensure companion device %s/%s: %w", account, clientID, err)
	}
	return stored, nil
}

// UpsertObservation applies latest-only semantics for one
// (device, kind): the newest observation by device-claimed observed_at
// wins, an exact event_id replay is a duplicate, and anything older
// than (or tied with) the stored observation is superseded. Verdicts
// are decided and written inside one transaction so racing uploads
// serialize. Implements [companion.ObservationSink].
func (s *Store) UpsertObservation(ctx context.Context, deviceID string, obs companion.Observation) (companion.ObservationOutcome, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return "", fmt.Errorf("device_id is required")
	}
	if strings.TrimSpace(obs.EventID) == "" || strings.TrimSpace(obs.Kind) == "" {
		return "", fmt.Errorf("event_id and kind are required")
	}
	obs.ObservedAt = normalizeTime(obs.ObservedAt)
	obs.ReceivedAt = normalizeTime(obs.ReceivedAt)

	status := ObservationStatusPresent
	payload := string(obs.Payload)
	if obs.Withdrawn {
		status = ObservationStatusWithdrawn
		payload = "{}"
	} else if payload == "" {
		payload = "{}"
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("upsert observation %s/%s: %w", deviceID, obs.Kind, err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		storedEventID    string
		storedObservedAt time.Time
	)
	err = tx.QueryRowContext(ctx,
		`SELECT event_id, observed_at FROM companion_observations WHERE device_id = ? AND kind = ?`,
		deviceID, obs.Kind,
	).Scan(&storedEventID, &storedObservedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO companion_observations (
				device_id, kind, event_id, schema_version,
				observed_at, received_at, payload, status
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, deviceID, obs.Kind, obs.EventID, obs.SchemaVersion,
			obs.ObservedAt, obs.ReceivedAt, payload, status); err != nil {
			return "", fmt.Errorf("insert observation %s/%s: %w", deviceID, obs.Kind, err)
		}
	case err != nil:
		return "", fmt.Errorf("read observation %s/%s: %w", deviceID, obs.Kind, err)
	case storedEventID == obs.EventID:
		// Replay of an accepted event: idempotent, nothing to write.
		return companion.ObservationDuplicate, nil
	case obs.ObservedAt.After(storedObservedAt.UTC()):
		if _, err := tx.ExecContext(ctx, `
			UPDATE companion_observations
			SET event_id = ?, schema_version = ?, observed_at = ?,
			    received_at = ?, payload = ?, status = ?
			WHERE device_id = ? AND kind = ?
		`, obs.EventID, obs.SchemaVersion, obs.ObservedAt,
			obs.ReceivedAt, payload, status, deviceID, obs.Kind); err != nil {
			return "", fmt.Errorf("update observation %s/%s: %w", deviceID, obs.Kind, err)
		}
	default:
		// Older than the stored observation, or a distinct event tied
		// with it — either way the stored row is at least as current.
		return companion.ObservationSuperseded, nil
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit observation %s/%s: %w", deviceID, obs.Kind, err)
	}
	return companion.ObservationApplied, nil
}

// GetObservation returns the latest observation of one kind for a
// device. The boolean reports existence; a missing row is not an error.
func (s *Store) GetObservation(ctx context.Context, deviceID, kind string) (StoredObservation, bool, error) {
	rows, err := s.scanObservations(ctx, `
		SELECT device_id, kind, event_id, schema_version,
		       observed_at, received_at, payload, status
		FROM companion_observations
		WHERE device_id = ? AND kind = ?
	`, deviceID, kind)
	if err != nil {
		return StoredObservation{}, false, err
	}
	if len(rows) == 0 {
		return StoredObservation{}, false, nil
	}
	return rows[0], true, nil
}

// ListObservations returns every latest observation for a device,
// ordered by kind for a stable shape.
func (s *Store) ListObservations(ctx context.Context, deviceID string) ([]StoredObservation, error) {
	return s.scanObservations(ctx, `
		SELECT device_id, kind, event_id, schema_version,
		       observed_at, received_at, payload, status
		FROM companion_observations
		WHERE device_id = ?
		ORDER BY kind ASC
	`, deviceID)
}

func (s *Store) scanObservations(ctx context.Context, query string, args ...any) ([]StoredObservation, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StoredObservation
	for rows.Next() {
		var (
			o       StoredObservation
			payload string
			status  string
		)
		if err := rows.Scan(
			&o.DeviceID, &o.Kind, &o.EventID, &o.SchemaVersion,
			&o.ObservedAt, &o.ReceivedAt, &payload, &status,
		); err != nil {
			return nil, err
		}
		o.ObservedAt = o.ObservedAt.UTC()
		o.ReceivedAt = o.ReceivedAt.UTC()
		o.Withdrawn = status == ObservationStatusWithdrawn
		o.Payload = json.RawMessage(payload)
		out = append(out, o)
	}
	return out, rows.Err()
}
