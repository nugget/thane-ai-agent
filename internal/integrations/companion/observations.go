package companion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Ingestion limits. Companion batches are outbox drains, not bulk
// imports: a phone waking from a background event uploads a handful of
// observations. The caps below are documented API contract
// (native.yaml carries the same numbers).
const (
	// maxObservationBodyBytes caps the request body.
	maxObservationBodyBytes = 256 << 10 // 256 KiB
	// maxObservationBatch caps events per request.
	maxObservationBatch = 64
	// maxObservationPayloadBytes caps one event's payload.
	maxObservationPayloadBytes = 16 << 10 // 16 KiB
	// maxObservationEventIDBytes caps the idempotency key.
	maxObservationEventIDBytes = 128
	// maxObservedAtFutureSkew is how far ahead of server time a
	// device-supplied observation time may claim to be before it is
	// rejected as implausible. Past times are legal at any distance —
	// draining a long-offline outbox is the point of the endpoint.
	maxObservedAtFutureSkew = 5 * time.Minute
)

// observedAtFloor rejects device clocks that are nonsensically far in
// the past (an unset clock reporting the epoch must not become a
// "latest" observation that nothing newer can supersede... backwards).
var observedAtFloor = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// observationKindRE constrains observation kinds to a compact
// namespace-friendly token (e.g. "ios.location").
var observationKindRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ObservationEvent is one wire-format event in an ingestion batch.
// ObservedAt is a string so one malformed timestamp invalidates one
// event, not the whole batch.
type ObservationEvent struct {
	EventID       string          `json:"event_id"`
	Kind          string          `json:"kind"`
	SchemaVersion int             `json:"schema_version"`
	ObservedAt    string          `json:"observed_at"`
	Withdrawn     bool            `json:"withdrawn,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

// Observation is a validated event bound for storage. ObservedAt is
// the device's claim about when the observation happened; ReceivedAt
// is the server's authoritative receipt time. The two are never
// conflated (#1437).
type Observation struct {
	EventID       string
	Kind          string
	SchemaVersion int
	ObservedAt    time.Time
	ReceivedAt    time.Time
	Withdrawn     bool
	Payload       json.RawMessage
}

// ObservationOutcome is a storage verdict for one event. Every outcome
// is terminal for the client's outbox entry: applied means stored,
// duplicate means this exact event was already accepted, superseded
// means a newer observation of the same kind is already stored.
type ObservationOutcome string

const (
	ObservationApplied    ObservationOutcome = "applied"
	ObservationDuplicate  ObservationOutcome = "duplicate"
	ObservationSuperseded ObservationOutcome = "superseded"
)

// ObservationSink stores validated observations. Implemented by the
// companions device store; the ingestion layer never touches SQL.
type ObservationSink interface {
	// EnsureDevice resolves (account, client_id) to the immutable
	// device_id, creating the inventory row when a device pushes its
	// first observation before ever connecting a WebSocket, and
	// bumping last-seen either way.
	EnsureDevice(ctx context.Context, account, clientID string, seenAt time.Time) (string, error)
	// UpsertObservation applies latest-only semantics for one
	// (device, kind) and reports what happened.
	UpsertObservation(ctx context.Context, deviceID string, obs Observation) (ObservationOutcome, error)
}

// ObservationAuthenticator resolves a request's bearer credential to
// an account. This is the seam the enrollment arc (#1444) replaces
// with signature verification; ingestion itself never sees the
// credential material.
type ObservationAuthenticator interface {
	Authenticate(r *http.Request) (account string, ok bool)
}

// observationBatch is the wire request shape.
type observationBatch struct {
	ClientID string             `json:"client_id"`
	Events   []ObservationEvent `json:"events"`
}

// observationResult is the wire verdict for one event.
type observationResult struct {
	EventID string `json:"event_id"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
}

// observationResponse is the wire response shape.
type observationResponse struct {
	Results []observationResult `json:"results"`
}

// ObservationsHandler serves POST /v1/companion/observations: the
// authenticated HTTPS path a companion uses to push observations when
// it cannot hold a WebSocket — iOS waking briefly for a background
// event is the motivating client (#1437). The batch → validate →
// EnsureDevice → UpsertObservation core is transport-independent; a
// future WebSocket observation message reuses everything below the
// HTTP decode.
type ObservationsHandler struct {
	auth   ObservationAuthenticator
	sink   ObservationSink
	logger *slog.Logger
}

// NewObservationsHandler creates the ingestion handler.
func NewObservationsHandler(auth ObservationAuthenticator, sink ObservationSink, logger *slog.Logger) *ObservationsHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &ObservationsHandler{auth: auth, sink: sink, logger: logger}
}

func (h *ObservationsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.auth == nil || h.sink == nil {
		http.Error(w, `{"error":"observation ingestion not configured"}`, http.StatusServiceUnavailable)
		return
	}

	account, ok := h.auth.Authenticate(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="companion"`)
		http.Error(w, `{"error":"invalid or missing companion token"}`, http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxObservationBodyBytes)
	var batch observationBatch
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeObservationError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("request body exceeds %d bytes", maxObservationBodyBytes))
			return
		}
		writeObservationError(w, http.StatusBadRequest, "malformed JSON body")
		return
	}

	clientID := strings.TrimSpace(batch.ClientID)
	if clientID == "" {
		writeObservationError(w, http.StatusBadRequest, "client_id is required")
		return
	}
	if len(batch.Events) == 0 {
		writeObservationError(w, http.StatusBadRequest, "events must not be empty")
		return
	}
	if len(batch.Events) > maxObservationBatch {
		writeObservationError(w, http.StatusBadRequest,
			fmt.Sprintf("batch exceeds %d events", maxObservationBatch))
		return
	}

	received := time.Now().UTC()
	deviceID, err := h.sink.EnsureDevice(r.Context(), account, clientID, received)
	if err != nil {
		h.logger.Error("companion observation device resolve failed",
			"account", account,
			"client_id", clientID,
			"error", err,
		)
		writeObservationError(w, http.StatusInternalServerError, "device resolution failed")
		return
	}

	results := make([]observationResult, 0, len(batch.Events))
	applied := 0
	for _, event := range batch.Events {
		obs, verr := validateObservationEvent(event, received)
		if verr != "" {
			results = append(results, observationResult{
				EventID: event.EventID,
				Status:  "invalid",
				Error:   verr,
			})
			continue
		}
		outcome, err := h.sink.UpsertObservation(r.Context(), deviceID, obs)
		if err != nil {
			// A storage failure is the one non-terminal case: fail the
			// whole batch so the client retries it verbatim — the
			// idempotency contract is exactly what makes that safe.
			h.logger.Error("companion observation store failed",
				"account", account,
				"client_id", clientID,
				"kind", obs.Kind,
				"error", err,
			)
			writeObservationError(w, http.StatusInternalServerError, "observation storage failed")
			return
		}
		if outcome == ObservationApplied {
			applied++
		}
		results = append(results, observationResult{
			EventID: event.EventID,
			Status:  string(outcome),
		})
	}

	// Counts and kinds are loggable; payloads never are (#1437 requires
	// precise location stay out of logs).
	h.logger.Info("companion observations ingested",
		"account", account,
		"client_id", clientID,
		"events", len(batch.Events),
		"applied", applied,
	)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(observationResponse{Results: results}); err != nil {
		h.logger.Debug("companion observation response write failed", "error", err)
	}
}

// validateObservationEvent turns one wire event into a storable
// observation, or a terminal per-event rejection reason. Rejections
// are terminal by design: a malformed event will not improve on retry,
// and the client should drop it from its outbox rather than wedge on it.
func validateObservationEvent(event ObservationEvent, received time.Time) (Observation, string) {
	eventID := strings.TrimSpace(event.EventID)
	if eventID == "" {
		return Observation{}, "event_id is required"
	}
	if len(eventID) > maxObservationEventIDBytes {
		return Observation{}, fmt.Sprintf("event_id exceeds %d bytes", maxObservationEventIDBytes)
	}
	if !observationKindRE.MatchString(event.Kind) {
		return Observation{}, "kind must match " + observationKindRE.String()
	}
	if event.SchemaVersion < 1 {
		return Observation{}, "schema_version must be >= 1"
	}

	observedAt, err := time.Parse(time.RFC3339, event.ObservedAt)
	if err != nil {
		return Observation{}, "observed_at must be RFC 3339"
	}
	observedAt = observedAt.UTC()
	if observedAt.After(received.Add(maxObservedAtFutureSkew)) {
		return Observation{}, "observed_at is implausibly in the future"
	}
	if observedAt.Before(observedAtFloor) {
		return Observation{}, "observed_at is implausibly old"
	}

	payload := event.Payload
	if event.Withdrawn {
		// A withdrawal erases; it must not smuggle data in.
		if len(payload) != 0 && string(payload) != "null" {
			return Observation{}, "a withdrawn event must not carry a payload"
		}
		payload = nil
	} else {
		if len(payload) == 0 || string(payload) == "null" {
			return Observation{}, "payload is required"
		}
		if len(payload) > maxObservationPayloadBytes {
			return Observation{}, fmt.Sprintf("payload exceeds %d bytes", maxObservationPayloadBytes)
		}
		if !json.Valid(payload) || payload[firstNonSpace(payload)] != '{' {
			return Observation{}, "payload must be a JSON object"
		}
	}

	return Observation{
		EventID:       eventID,
		Kind:          event.Kind,
		SchemaVersion: event.SchemaVersion,
		ObservedAt:    observedAt,
		ReceivedAt:    received,
		Withdrawn:     event.Withdrawn,
		Payload:       payload,
	}, ""
}

// firstNonSpace returns the index of the first non-whitespace byte
// (JSON whitespace per RFC 8259). Callers pass payloads json.Valid
// already accepted, so a non-space byte exists.
func firstNonSpace(b []byte) int {
	for i, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return i
		}
	}
	return 0
}

func writeObservationError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
