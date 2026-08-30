package companion

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	// ErrObservationNotFound means no companion has ever published the
	// requested observation kind within the supplied routing scope.
	ErrObservationNotFound = errors.New("companion observation not found")
	// ErrObservationAmbiguous means more than one device matches and the
	// caller must supply account and client ID rather than guess.
	ErrObservationAmbiguous = errors.New("companion observation is ambiguous")
	// ErrObservationKindLimit means a device has reached the bounded number
	// of distinct latest-value kinds it may retain.
	ErrObservationKindLimit = errors.New("companion observation kind limit reached")
)

// MaxObservationKindsPerDevice bounds latest-value storage independently of
// request size. Existing kinds may continue to advance after the limit is
// reached; only creation of another distinct kind is rejected.
const MaxObservationKindsPerDevice = 64

// ObservationStatus describes whether a companion currently shares an
// observation or has explicitly withdrawn it.
type ObservationStatus string

const (
	ObservationAvailable ObservationStatus = "available"
	ObservationWithdrawn ObservationStatus = "withdrawn"
)

// ObservationDeviceMetadata describes the installation sending a batch.
// ClientID is the stable opaque claim reused by WebSocket authentication;
// the remaining fields are optional client-reported metadata.
type ObservationDeviceMetadata struct {
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
	ObservationDeviceMetadata
	Events []ObservationEvent `json:"events"`
}

// ObservationPrincipal is the server-resolved ownership boundary applied to
// an upload. DeviceID is the inventory's immutable identifier, not a client
// claim or credential fingerprint.
type ObservationPrincipal struct {
	Account  string
	DeviceID string
}

// IngestResult reports how many latest-value rows advanced. Ignored events
// were valid but older than, or identical to, the latest stored observation.
type IngestResult struct {
	Stored     int       `json:"stored"`
	Ignored    int       `json:"ignored"`
	ReceivedAt time.Time `json:"received_at"`
}

// LatestObservation is the durable latest-value record for one observation
// kind. Payload remains raw JSON so the store does not claim ownership of
// provider-versioned payload schemas.
type LatestObservation struct {
	Account       string
	DeviceID      string
	ClientID      string
	Kind          string
	EventID       string
	SchemaVersion int
	Status        ObservationStatus
	ObservedAt    time.Time
	ReceivedAt    time.Time
	Payload       json.RawMessage
}

// ObservationStore is the transport-independent latest-value persistence
// boundary used by HTTP today and reusable by another companion transport.
type ObservationStore interface {
	IngestObservations(context.Context, ObservationPrincipal, ObservationBatch, time.Time) (IngestResult, error)
}

// ObservationIdentityLookup maps today's authenticated account and opaque
// client claim to the inventory's immutable device ID. A future signed-request
// authenticator can resolve a key credential to that same ID instead.
type ObservationIdentityLookup func(context.Context, string, string) (string, bool, error)
