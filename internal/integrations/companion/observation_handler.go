package companion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	maxObservationBodyBytes    = 64 * 1024
	maxObservationEvents       = 16
	maxObservationPayloadBytes = 32 * 1024
	maxObservationStringRunes  = 128
	maxObservationFutureSkew   = 5 * time.Minute
)

// ObservationHandler authenticates companion tokens and ingests bounded
// background observation batches without requiring a live WebSocket.
type ObservationHandler struct {
	authenticator ObservationAuthenticator
	store         ObservationStore
	logger        *slog.Logger
	now           func() time.Time
}

// NewObservationHandler creates the authenticated companion observation
// endpoint. Authentication is replaceable so ingestion and storage do not
// own bearer-token configuration or the future device-key mechanism.
func NewObservationHandler(authenticator ObservationAuthenticator, store ObservationStore, logger *slog.Logger) *ObservationHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ObservationHandler{
		authenticator: authenticator,
		store:         store,
		logger:        logger,
		now:           time.Now,
	}
}

// ServeHTTP validates, authenticates, and persists one observation batch.
func (h *ObservationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeObservationError(w, http.StatusServiceUnavailable, "observation_store_unavailable", "companion observation storage is unavailable")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeObservationError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxObservationBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeObservationError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "observation batch exceeds 64 KiB")
			return
		}
		writeObservationError(w, http.StatusBadRequest, "invalid_body", "read observation batch: "+err.Error())
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var batch ObservationBatch
	if err := decoder.Decode(&batch); err != nil {
		writeObservationError(w, http.StatusBadRequest, "invalid_json", "decode observation batch: "+err.Error())
		return
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		writeObservationError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if h.authenticator == nil {
		writeObservationError(w, http.StatusServiceUnavailable, "observation_auth_unavailable", "companion observation authentication is unavailable")
		return
	}
	principal, err := h.authenticator.AuthenticateObservation(r.Context(), ObservationAuthRequest{
		Method:          r.Method,
		RequestTarget:   r.URL.RequestURI(),
		Header:          r.Header.Clone(),
		Body:            body,
		ClaimedClientID: batch.ClientID,
	})
	if errors.Is(err, ErrObservationUnauthorized) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="companion"`)
		writeObservationError(w, http.StatusUnauthorized, "unauthorized", "a valid companion bearer token is required")
		return
	}
	if err != nil {
		h.logger.Error("companion observation authentication failed",
			"client_id", batch.ClientID,
			"error", err,
		)
		writeObservationError(w, http.StatusInternalServerError, "authentication_failed", "failed to authenticate companion observation")
		return
	}

	receivedAt := h.now().UTC()
	if err := validateObservationBatch(&batch, receivedAt); err != nil {
		writeObservationError(w, http.StatusBadRequest, "invalid_observation", err.Error())
		return
	}
	result, err := h.store.IngestObservations(r.Context(), principal, batch, receivedAt)
	if err != nil {
		h.logger.Error("companion observation ingest failed",
			"account", principal.Account,
			"client_id", batch.ClientID,
			"events", len(batch.Events),
			"error", err,
		)
		writeObservationError(w, http.StatusInternalServerError, "ingest_failed", "failed to store companion observations")
		return
	}

	h.logger.Info("companion observations received",
		"account", principal.Account,
		"client_id", batch.ClientID,
		"platform", batch.Platform,
		"stored", result.Stored,
		"ignored", result.Ignored,
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(result); err != nil {
		h.logger.Debug("write companion observation response failed", "error", err)
	}
}

func validateObservationBatch(batch *ObservationBatch, receivedAt time.Time) error {
	trimDeviceMetadata(&batch.ObservationDeviceMetadata)
	if err := validateBoundedOpaque("client_id", batch.ClientID); err != nil {
		return err
	}
	if err := validateOptionalString("client_name", batch.ClientName); err != nil {
		return err
	}
	if err := validateOptionalIdentifier("platform", batch.Platform); err != nil {
		return err
	}
	if err := validateOptionalString("app_version", batch.AppVersion); err != nil {
		return err
	}
	if err := validateOptionalString("os_version", batch.OSVersion); err != nil {
		return err
	}
	if len(batch.Events) == 0 || len(batch.Events) > maxObservationEvents {
		return fmt.Errorf("events must contain between 1 and %d items", maxObservationEvents)
	}
	for i := range batch.Events {
		event := &batch.Events[i]
		event.EventID = strings.TrimSpace(event.EventID)
		event.Kind = strings.TrimSpace(event.Kind)
		parsedEventID, err := uuid.Parse(event.EventID)
		if err != nil {
			return fmt.Errorf("events[%d].event_id must be a UUID", i)
		}
		event.EventID = parsedEventID.String()
		if err := validateBoundedIdentifier(fmt.Sprintf("events[%d].kind", i), event.Kind); err != nil {
			return err
		}
		if event.SchemaVersion < 1 || event.SchemaVersion > 1000 {
			return fmt.Errorf("events[%d].schema_version must be between 1 and 1000", i)
		}
		if event.ObservedAt.IsZero() {
			return fmt.Errorf("events[%d].observed_at is required", i)
		}
		if event.ObservedAt.After(receivedAt.Add(maxObservationFutureSkew)) {
			return fmt.Errorf("events[%d].observed_at is too far in the future", i)
		}
		if event.Status == "" {
			event.Status = ObservationAvailable
		}
		switch event.Status {
		case ObservationAvailable:
			if len(event.Payload) == 0 || string(event.Payload) == "null" {
				return fmt.Errorf("events[%d].payload is required while status is available", i)
			}
			if len(event.Payload) > maxObservationPayloadBytes {
				return fmt.Errorf("events[%d].payload exceeds %d bytes", i, maxObservationPayloadBytes)
			}
			trimmedPayload := strings.TrimSpace(string(event.Payload))
			if !json.Valid(event.Payload) || !strings.HasPrefix(trimmedPayload, "{") {
				return fmt.Errorf("events[%d].payload must be a JSON object", i)
			}
		case ObservationWithdrawn:
			if len(event.Payload) != 0 && string(event.Payload) != "null" {
				return fmt.Errorf("events[%d].payload must be omitted while status is withdrawn", i)
			}
			event.Payload = nil
		default:
			return fmt.Errorf("events[%d].status must be available or withdrawn", i)
		}
		event.ObservedAt = event.ObservedAt.UTC()
	}
	return nil
}

func trimDeviceMetadata(device *ObservationDeviceMetadata) {
	device.ClientID = strings.TrimSpace(device.ClientID)
	device.ClientName = strings.TrimSpace(device.ClientName)
	device.Platform = strings.TrimSpace(device.Platform)
	device.AppVersion = strings.TrimSpace(device.AppVersion)
	device.OSVersion = strings.TrimSpace(device.OSVersion)
}

func validateBoundedIdentifier(name, value string) error {
	if err := validateBoundedOpaque(name, value); err != nil {
		return err
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("%s contains an unsupported character", name)
	}
	return nil
}

func validateBoundedOpaque(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if utf8.RuneCountInString(value) > maxObservationStringRunes {
		return fmt.Errorf("%s exceeds %d characters", name, maxObservationStringRunes)
	}
	return nil
}

func validateOptionalIdentifier(name, value string) error {
	if value == "" {
		return nil
	}
	return validateBoundedIdentifier(name, value)
}

func validateOptionalString(name, value string) error {
	if utf8.RuneCountInString(value) > maxObservationStringRunes {
		return fmt.Errorf("%s exceeds %d characters", name, maxObservationStringRunes)
	}
	return nil
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("observation batch must contain one JSON object")
	}
	return fmt.Errorf("decode trailing observation data: %w", err)
}

func writeObservationError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
