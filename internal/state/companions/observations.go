package companions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
)

// ResolveObservationIdentity maps the current account/client claim onto the
// inventory's immutable device ID. It is passed to
// [companion.NewBearerObservationAuthenticator] as an
// [companion.ObservationIdentityLookup].
func (s *Store) ResolveObservationIdentity(ctx context.Context, account, clientID string) (string, bool, error) {
	if err := validateKey(account, clientID); err != nil {
		return "", false, err
	}
	var deviceID string
	err := s.db.QueryRowContext(ctx, `
		SELECT device_id FROM companion_devices
		WHERE account = ? AND client_id = ?
	`, account, clientID).Scan(&deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve companion observation identity %s/%s: %w", account, clientID, err)
	}
	return deviceID, true, nil
}

// IngestObservations atomically advances the device's last-seen time and the
// latest value for each observation kind. Equal timestamps use event ID as a
// deterministic tie-breaker; an exact event replay never rewrites a value.
// Implements [companion.ObservationStore].
func (s *Store) IngestObservations(
	ctx context.Context,
	principal companion.ObservationPrincipal,
	batch companion.ObservationBatch,
	receivedAt time.Time,
) (companion.IngestResult, error) {
	result := companion.IngestResult{ReceivedAt: receivedAt.UTC()}
	if strings.TrimSpace(principal.Account) == "" || strings.TrimSpace(principal.DeviceID) == "" {
		return result, fmt.Errorf("companion observation principal requires account and device ID")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin companion observation ingest: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var storedAccount string
	if err := tx.QueryRowContext(ctx,
		`SELECT account FROM companion_devices WHERE device_id = ?`, principal.DeviceID,
	).Scan(&storedAccount); err != nil {
		return result, fmt.Errorf("verify companion observation device %s: %w", principal.DeviceID, err)
	}
	if storedAccount != principal.Account {
		return result, fmt.Errorf("companion observation device does not belong to authenticated account")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE companion_devices
		SET last_seen_at = MAX(last_seen_at, ?)
		WHERE device_id = ? AND account = ?
	`, result.ReceivedAt, principal.DeviceID, principal.Account); err != nil {
		return result, fmt.Errorf("update companion observation last seen: %w", err)
	}

	for _, event := range batch.Events {
		status := event.Status
		if status == "" {
			status = companion.ObservationAvailable
		}
		var payload any
		if status == companion.ObservationAvailable {
			payload = string(event.Payload)
		}
		write, err := tx.ExecContext(ctx, `
			INSERT INTO companion_latest_observations (
				device_id, kind, event_id, schema_version, status,
				observed_at, received_at, payload
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(device_id, kind) DO UPDATE SET
				event_id = excluded.event_id,
				schema_version = excluded.schema_version,
				status = excluded.status,
				observed_at = excluded.observed_at,
				received_at = excluded.received_at,
				payload = excluded.payload
			WHERE excluded.event_id != companion_latest_observations.event_id
			  AND (excluded.observed_at > companion_latest_observations.observed_at
			       OR (excluded.observed_at = companion_latest_observations.observed_at
			           AND excluded.event_id > companion_latest_observations.event_id))
		`, principal.DeviceID, event.Kind, event.EventID, event.SchemaVersion,
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

// ListLatestObservations returns latest observations in stable device/kind
// order. Payloads remain available only to explicit consumers, never ambient
// context by implication.
func (s *Store) ListLatestObservations(ctx context.Context) ([]companion.LatestObservation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.account, o.device_id, d.client_id, o.kind, o.event_id,
		       o.schema_version, o.status, o.observed_at, o.received_at, o.payload
		FROM companion_latest_observations o
		JOIN companion_devices d ON d.device_id = o.device_id
		ORDER BY d.account, d.client_id, o.kind
	`)
	if err != nil {
		return nil, fmt.Errorf("list companion observations: %w", err)
	}
	defer rows.Close()

	var observations []companion.LatestObservation
	for rows.Next() {
		observation, err := scanLatestObservation(rows)
		if err != nil {
			return nil, err
		}
		observations = append(observations, observation)
	}
	return observations, rows.Err()
}

// ResolveLatestObservation selects one latest observation by kind and
// optional account/client routing hints. Ambiguous matches are never guessed.
func (s *Store) ResolveLatestObservation(ctx context.Context, account, clientID, kind string) (companion.LatestObservation, error) {
	query := `SELECT d.account, o.device_id, d.client_id, o.kind, o.event_id,
		o.schema_version, o.status, o.observed_at, o.received_at, o.payload
		FROM companion_latest_observations o
		JOIN companion_devices d ON d.device_id = o.device_id
		WHERE o.kind = ?`
	args := []any{kind}
	if account != "" {
		query += " AND d.account = ?"
		args = append(args, account)
	}
	if clientID != "" {
		query += " AND d.client_id = ?"
		args = append(args, clientID)
	}
	query += " ORDER BY d.account, d.client_id"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return companion.LatestObservation{}, fmt.Errorf("resolve companion observation: %w", err)
	}
	defer rows.Close()

	var matches []companion.LatestObservation
	for rows.Next() {
		observation, err := scanLatestObservation(rows)
		if err != nil {
			return companion.LatestObservation{}, err
		}
		matches = append(matches, observation)
	}
	if err := rows.Err(); err != nil {
		return companion.LatestObservation{}, err
	}
	switch len(matches) {
	case 0:
		return companion.LatestObservation{}, fmt.Errorf("%w: no companion has published %q for the requested account and client_id", companion.ErrObservationNotFound, kind)
	case 1:
		return matches[0], nil
	default:
		labels := make([]string, 0, len(matches))
		for _, match := range matches {
			labels = append(labels, match.Account+"/"+match.ClientID)
		}
		sort.Strings(labels)
		return companion.LatestObservation{}, fmt.Errorf("%w: multiple companions have %q (%s); retry with account and client_id", companion.ErrObservationAmbiguous, kind, strings.Join(labels, ", "))
	}
}

func scanLatestObservation(row *sql.Rows) (companion.LatestObservation, error) {
	var observation companion.LatestObservation
	var status string
	var payload sql.NullString
	if err := row.Scan(
		&observation.Account, &observation.DeviceID, &observation.ClientID, &observation.Kind,
		&observation.EventID, &observation.SchemaVersion, &status,
		&observation.ObservedAt, &observation.ReceivedAt, &payload,
	); err != nil {
		return observation, fmt.Errorf("scan companion observation: %w", err)
	}
	observation.Status = companion.ObservationStatus(status)
	observation.ObservedAt = observation.ObservedAt.UTC()
	observation.ReceivedAt = observation.ReceivedAt.UTC()
	if payload.Valid {
		observation.Payload = json.RawMessage(payload.String)
	}
	return observation, nil
}
