package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

const (
	iosLocationFreshFor  = 2 * time.Hour
	iosLocationExpiresIn = 24 * time.Hour
)

// SetCompanionObservationStore adds tools backed by durable companion pushes.
// Unlike live companion tools, these remain callable while the source device
// is suspended or offline.
func (r *Registry) SetCompanionObservationStore(store *companion.ObservationStore) {
	if store == nil {
		return
	}
	r.Register(&Tool{
		Name: "ios_last_known_location",
		Description: "Read the latest location explicitly shared by an iOS companion, including its source and age. " +
			"This is cached last-known data, not a live request; inspect availability and freshness before describing where the operator is now.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account": map[string]any{
					"type":        "string",
					"description": "Optional companion account. Supply this with client_id when more than one iPhone has shared location.",
				},
				"client_id": map[string]any{
					"type":        "string",
					"description": "Optional stable companion client ID. Supply this with account when more than one iPhone has shared location.",
				},
			},
		},
		Tags: []string{"companion", "ios", "location", "read"},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			return handleIOSLastKnownLocation(ctx, store, args, time.Now())
		},
	})
}

type iosLastKnownLocationResult struct {
	Availability string            `json:"availability"`
	Freshness    string            `json:"freshness,omitempty"`
	Source       iosLocationSource `json:"source,omitempty"`
	ObservedAgo  string            `json:"observed_ago,omitempty"`
	ReceivedAgo  string            `json:"received_ago,omitempty"`
	Location     json.RawMessage   `json:"location,omitempty"`
	Guidance     string            `json:"guidance,omitempty"`
}

type iosLocationSource struct {
	Account       string `json:"account"`
	ClientID      string `json:"client_id"`
	SchemaVersion int    `json:"schema_version"`
}

func handleIOSLastKnownLocation(ctx context.Context, store *companion.ObservationStore, args map[string]any, now time.Time) (string, error) {
	observation, err := store.ResolveLatest(
		ctx,
		strings.TrimSpace(stringArg(args, "account")),
		strings.TrimSpace(stringArg(args, "client_id")),
		"ios.location",
	)
	if errors.Is(err, companion.ErrObservationNotFound) {
		return marshalIOSLocationResult(iosLastKnownLocationResult{
			Availability: "never_observed",
			Guidance:     "No iOS companion has published location in this scope. Use ios_current_location while a matching companion is online, or wait for an enabled background update.",
		})
	}
	if err != nil {
		return "", err
	}

	result := iosLastKnownLocationResult{
		Source: iosLocationSource{
			Account: observation.Account, ClientID: observation.ClientID,
			SchemaVersion: observation.SchemaVersion,
		},
		ObservedAgo: promptfmt.FormatDeltaOnly(observation.ObservedAt, now),
		ReceivedAgo: promptfmt.FormatDeltaOnly(observation.ReceivedAt, now),
	}
	if observation.Status == companion.ObservationWithdrawn {
		result.Availability = "withdrawn"
		result.Guidance = "The operator or iOS permission state withdrew background location sharing; do not reuse an earlier location."
		return marshalIOSLocationResult(result)
	}

	age := now.Sub(observation.ObservedAt)
	switch {
	case age < 0:
		result.Availability = "available"
		result.Freshness = "fresh"
		result.Location = observation.Payload
	case age <= iosLocationFreshFor:
		result.Availability = "available"
		result.Freshness = "fresh"
		result.Location = observation.Payload
	case age <= iosLocationExpiresIn:
		result.Availability = "available"
		result.Freshness = "stale"
		result.Location = observation.Payload
		result.Guidance = "This is a stale last-known location; state its age and do not describe it as current."
	default:
		result.Availability = "expired"
		result.Freshness = "expired"
		result.Guidance = "The last observation is too old to disclose as the operator's location. Use live retrieval if the companion is online or wait for a background update."
	}
	return marshalIOSLocationResult(result)
}

func marshalIOSLocationResult(result iosLastKnownLocationResult) (string, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal iOS last-known location: %w", err)
	}
	return string(data), nil
}
