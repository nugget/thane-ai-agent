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
// latest value for each observation kind. At equal timestamps, withdrawal
// dominates availability; matching statuses use event ID as a deterministic
// tie-breaker. An exact event replay never rewrites a value.
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

	metadataAt := any(nil)
	if batch.ClientName != "" || batch.Platform != "" || batch.AppVersion != "" || batch.OSVersion != "" {
		metadataAt = result.ReceivedAt
	}
	// Take SQLite's write lock before establishing any read snapshot. A
	// deferred transaction that reads first cannot upgrade after a concurrent
	// writer commits in WAL mode and fails with SQLITE_BUSY_SNAPSHOT instead.
	deviceWrite, err := tx.ExecContext(ctx, `
		UPDATE companion_devices
		SET client_name = CASE WHEN ? != ''
				AND (client_name = '' OR metadata_recorded_at IS NULL OR ? >= metadata_recorded_at)
			THEN ? ELSE client_name END,
			platform = CASE WHEN ? != ''
				AND (platform = '' OR metadata_recorded_at IS NULL OR ? >= metadata_recorded_at)
			THEN ? ELSE platform END,
			app_version = CASE WHEN ? != ''
				AND (app_version = '' OR metadata_recorded_at IS NULL OR ? >= metadata_recorded_at)
			THEN ? ELSE app_version END,
			os_version = CASE WHEN ? != ''
				AND (os_version = '' OR metadata_recorded_at IS NULL OR ? >= metadata_recorded_at)
			THEN ? ELSE os_version END,
			metadata_recorded_at = CASE WHEN ? IS NOT NULL
				AND (metadata_recorded_at IS NULL OR ? >= metadata_recorded_at)
			THEN ? ELSE metadata_recorded_at END,
			last_seen_at = MAX(last_seen_at, ?)
		WHERE device_id = ? AND account = ?
	`, batch.ClientName, metadataAt, batch.ClientName,
		batch.Platform, metadataAt, batch.Platform,
		batch.AppVersion, metadataAt, batch.AppVersion,
		batch.OSVersion, metadataAt, batch.OSVersion,
		metadataAt, metadataAt, metadataAt,
		result.ReceivedAt, principal.DeviceID, principal.Account)
	if err != nil {
		return result, fmt.Errorf("update companion observation last seen: %w", err)
	}
	deviceRows, err := deviceWrite.RowsAffected()
	if err != nil {
		return result, fmt.Errorf("verify companion observation device ownership: %w", err)
	}
	if deviceRows == 0 {
		return result, fmt.Errorf("companion observation device does not belong to authenticated account")
	}

	existingKinds := make(map[string]struct{}, companion.MaxObservationKindsPerDevice)
	rows, err := tx.QueryContext(ctx,
		`SELECT kind FROM companion_latest_observations WHERE device_id = ?`, principal.DeviceID,
	)
	if err != nil {
		return result, fmt.Errorf("list companion observation kinds: %w", err)
	}
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			_ = rows.Close()
			return result, fmt.Errorf("scan companion observation kind: %w", err)
		}
		existingKinds[kind] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return result, fmt.Errorf("list companion observation kinds: %w", err)
	}
	if err := rows.Close(); err != nil {
		return result, fmt.Errorf("close companion observation kinds: %w", err)
	}
	newKinds := make(map[string]struct{}, len(batch.Events))
	for _, event := range batch.Events {
		if _, exists := existingKinds[event.Kind]; exists {
			continue
		}
		newKinds[event.Kind] = struct{}{}
	}
	if len(newKinds) > 0 && len(existingKinds)+len(newKinds) > companion.MaxObservationKindsPerDevice {
		return result, fmt.Errorf("%w: device may retain at most %d distinct kinds",
			companion.ErrObservationKindLimit, companion.MaxObservationKindsPerDevice)
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
			           AND ((excluded.status = 'withdrawn'
			                 AND companion_latest_observations.status != 'withdrawn')
			                OR (excluded.status = companion_latest_observations.status
			                    AND excluded.event_id > companion_latest_observations.event_id))))
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
		return companion.LatestObservation{}, fmt.Errorf("%w: no companion has published %q %s",
			companion.ErrObservationNotFound, kind, observationRoutingScope(account, clientID))
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

func observationRoutingScope(account, clientID string) string {
	switch {
	case account != "" && clientID != "":
		return fmt.Sprintf("for account %q and client_id %q", account, clientID)
	case account != "":
		return fmt.Sprintf("for account %q", account)
	case clientID != "":
		return fmt.Sprintf("for client_id %q", clientID)
	default:
		return "across all companions"
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
