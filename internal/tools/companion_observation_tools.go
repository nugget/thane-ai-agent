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
	"github.com/nugget/thane-ai-agent/internal/state/companions"
)

// companionLocationStaleAfter is the freshness horizon for last-known
// location: a phone at rest legitimately stops pushing, so the payload
// stays useful for hours — but past this age the result is labeled
// stale so the model weighs it accordingly. Both ages are always
// returned; the label is a judgment aid, not a gate.
const companionLocationStaleAfter = 24 * time.Hour

// EnableCompanionObservationTools adds server-native tools that read
// the durable companion observation store (#1437). Unlike the live
// registry-backed companion tools, these answer from persistence and
// work while every device is offline — that is their point.
func (r *Registry) EnableCompanionObservationTools(store *companions.Store, contacts companions.ContactResolver) {
	if store == nil {
		return
	}

	r.Register(&Tool{
		Name: "companion_last_known_location",
		Description: "Report the last location a companion device pushed to Thane — durable data that " +
			"answers even while the phone is offline, locked, or asleep. This is NOT a live query: the result " +
			"always carries observed_ago (the device's claim of when the fix happened) and received_ago (when it " +
			"reached Thane), and freshness turns \"stale\" after 24h. For a live fix from a currently-online " +
			"device, use that device's own advertised tool instead. Status is reported distinctly: \"available\" " +
			"(payload included), \"withdrawn\" (sharing was revoked — the data is gone, not stale), or an error " +
			"naming whether nothing was ever observed or the selection was ambiguous. With several candidate " +
			"devices, retry with account and/or client_id from the choices the error lists.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account": map[string]any{
					"type":        "string",
					"description": "Optional companion account to select when more than one account has published a location.",
				},
				"client_id": map[string]any{
					"type":        "string",
					"description": "Optional device client_id (from the Companion Devices context block) when one account has several devices.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			account, _ := args["account"].(string)
			clientID, _ := args["client_id"].(string)
			return r.handleCompanionLastKnownLocation(ctx, store, contacts, strings.TrimSpace(account), strings.TrimSpace(clientID))
		},
	})
}

// companionLastKnownLocationResult is the tool's result shape. Ages are
// deltas for reasoning; absolute times remain for record-keeping.
type companionLastKnownLocationResult struct {
	Contact          string          `json:"contact,omitempty"`
	ContactTrustZone string          `json:"contact_trust_zone,omitempty"`
	Account          string          `json:"account"`
	ClientName       string          `json:"client_name,omitempty"`
	ClientID         string          `json:"client_id"`
	DeviceID         string          `json:"device_id"`
	Platform         string          `json:"platform,omitempty"`
	Status           string          `json:"status"`
	Freshness        string          `json:"freshness,omitempty"`
	ObservedAgo      string          `json:"observed_ago"`
	ReceivedAgo      string          `json:"received_ago"`
	ObservedAt       string          `json:"observed_at"`
	SchemaVersion    int             `json:"schema_version"`
	Location         json.RawMessage `json:"location,omitempty"`
	Note             string          `json:"note,omitempty"`
}

func (r *Registry) handleCompanionLastKnownLocation(ctx context.Context, store *companions.Store, contacts companions.ContactResolver, account, clientID string) (string, error) {
	obs, err := store.ResolveLatestObservation(ctx, account, clientID, "ios.location")
	if errors.Is(err, companion.ErrObservationAmbiguous) {
		// The store's error already enumerates account/client_id
		// choices — exactly what the model needs to retry.
		return "", err
	}
	if errors.Is(err, companion.ErrObservationNotFound) {
		return "", fmt.Errorf("%w; devices listed in the Companion Devices context block that show no ios.location observation have never pushed one — there is no stored location to report, which is different from a device being offline", err)
	}
	if err != nil {
		return "", fmt.Errorf("read last known location: %w", err)
	}

	now := time.Now().UTC()
	result := companionLastKnownLocationResult{
		Account:       obs.Account,
		ClientID:      obs.ClientID,
		DeviceID:      obs.DeviceID,
		Status:        string(obs.Status),
		ObservedAgo:   promptfmt.FormatDeltaOnly(obs.ObservedAt, now),
		ReceivedAgo:   promptfmt.FormatDeltaOnly(obs.ReceivedAt, now),
		ObservedAt:    obs.ObservedAt.Format(time.RFC3339),
		SchemaVersion: obs.SchemaVersion,
	}

	// Device metadata and the counterparty attribution the result is
	// really about: whose phone this was.
	if device, ok, err := store.Get(ctx, obs.Account, obs.ClientID); err == nil && ok {
		result.ClientName = device.ClientName
		result.Platform = device.Platform
	}
	if contacts != nil {
		if binding, ok := contacts(ctx, obs.Account); ok {
			result.Contact = binding.Name
			result.ContactTrustZone = binding.TrustZone
		}
	}

	switch obs.Status {
	case companion.ObservationWithdrawn:
		result.Note = "location sharing was revoked on this device; the previously stored fix is gone, not stale — do not treat this as a location"
	default:
		result.Location = obs.Payload
		if now.Sub(obs.ObservedAt) > companionLocationStaleAfter {
			result.Freshness = "stale"
		} else {
			result.Freshness = "fresh"
		}
	}

	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal last known location: %w", err)
	}
	return string(out), nil
}
