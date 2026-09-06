package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	companion "github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// maxRecentPlaces bounds what one call renders. The device's own window
// is already capped; this keeps a long window from crowding a turn.
const maxRecentPlaces = 12

// placeView is one stay as the model reads it. Deltas render against the
// call, not the capture, so "40m ago" means forty minutes before this
// question was asked.
type placeView struct {
	// State is "here_now" for a stay still underway, "left" once ended.
	// The device's own vocabulary is ongoing/settled; this says the same
	// thing in the tense a reader thinks in.
	State string `json:"state"`
	// Arrived is a delta, or absent when the stay began before the
	// device started watching. ArrivalTimed says which, so a reader
	// never mistakes an unobserved arrival for a recent one.
	//
	// ArrivedAt and DepartedAt carry the same moments absolutely. Both
	// forms ship because they serve opposite jobs: the delta is how a
	// reader judges recency in this turn, the instant is the only form
	// that survives being written down. A caller with just the delta
	// either copies a value that starts rotting immediately or does
	// clock arithmetic this project deliberately keeps away from models.
	Arrived      string `json:"arrived,omitempty"`
	ArrivedAt    string `json:"arrived_at,omitempty"`
	ArrivalTimed bool   `json:"arrival_timed"`
	Departed     string `json:"departed,omitempty"`
	DepartedAt   string `json:"departed_at,omitempty"`
	// Dwell is how long the stay lasted; DwellStillAccruing marks one
	// that has not finished, so "11m" is not read as a completed visit.
	Dwell              string  `json:"dwell,omitempty"`
	DwellStillAccruing bool    `json:"dwell_still_accruing,omitempty"`
	Latitude           float64 `json:"latitude"`
	Longitude          float64 `json:"longitude"`
	AccuracyMeters     float64 `json:"accuracy_meters,omitempty"`
}

type recentPlacesResult struct {
	Contact string `json:"contact"`
	// Device names which companion produced the window; a contact with
	// several reports the one whose window was read.
	Device      string      `json:"device,omitempty"`
	CapturedAgo string      `json:"captured_ago,omitempty"`
	CapturedAt  string      `json:"captured_at,omitempty"`
	WindowHours float64     `json:"window_hours,omitempty"`
	Places      []placeView `json:"places,omitempty"`
	// Truncated reports the device dropping stays from its own window;
	// Omitted reports this tool capping what it rendered. They are
	// different losses and a reader deserves to tell them apart.
	Truncated bool   `json:"truncated,omitempty"`
	Omitted   int    `json:"omitted,omitempty"`
	Basis     string `json:"basis"`
}

// EnableCounterpartyPlacesTools adds the recent-places view: the stays a
// contact's companion device judged significant, newest first.
//
// This is not a location history. The device decides what counts as a
// place, using sensor data the server never sees, and publishes the
// whole recent window each time — so a stay carries an arrival, a
// departure and a dwell that no sequence of raw fixes would yield.
func (r *Registry) EnableCounterpartyPlacesTools(deps CounterpartyToolDeps) {
	if r == nil || deps.Contacts == nil || deps.Companions == nil {
		return
	}

	r.Register(&Tool{
		Name: "contact_recent_places",
		Description: "List the places a contact's companion device recently judged significant, newest first, with how long they stayed at each. " +
			"This answers \"where have they been\" and \"how long have they been there\" — questions a coordinate cannot. " +
			"The device decides what counts as a place from sensor data the server never sees, so a stay here carries a timed arrival, a departure, and a dwell that no sequence of raw fixes would produce. " +
			"state is here_now for a stay still underway and left once it ended; dwell_still_accruing marks a dwell that has not finished, so an in-progress stay is never read as a completed one. " +
			"arrival_timed is false when the stay was already underway before the device started watching — the arrival is genuinely unknown rather than zero. " +
			"Reach for contact_whereabouts when you need where somebody is right now fused across every source; reach here when the sequence and the dwells are the point. " +
			"Every moment ships twice: arrived/departed/captured_ago are deltas for judging recency in this turn, arrived_at/departed_at/captured_at are the same moments absolutely. " +
			"Anything you write into a document takes the absolute form — a delta copied into stored prose is wrong minutes later, and document writes refuse it. " +
			"Pass name (resolved like other contact tools) or contact_id (UUID).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Contact name (formatted name or nickname; resolved with the standard contact cascade).",
				},
				"contact_id": map[string]any{
					"type":        "string",
					"description": "Exact contact UUID; takes precedence over name when both are given.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			id, _ := args["contact_id"].(string)
			return handleContactRecentPlaces(ctx, deps, strings.TrimSpace(name), strings.TrimSpace(id))
		},
	})
}

func handleContactRecentPlaces(ctx context.Context, deps CounterpartyToolDeps, name, id string) (string, error) {
	contact, err := resolveWhereaboutsContact(deps.Contacts, name, id)
	if err != nil {
		return "", err
	}

	observations, err := deps.Companions.LatestObservationsForContact(ctx, companion.VisitObservationKind, contact.ID.String())
	if err != nil {
		return "", fmt.Errorf("read companion visit windows: %w", err)
	}

	result := recentPlacesResult{Contact: contact.FormattedName}
	if len(observations) == 0 {
		// Three different absences read the same from here, so the basis
		// says which rather than implying the person went nowhere.
		result.Basis = "no companion device bound to this contact has published a visit window; either no device is bound, the device does not share visits, or it has not published one yet"
		return marshalRecentPlaces(result)
	}

	// Newest window wins when a contact carries several devices: the
	// freshest capture is the one that knows where they are now.
	best := observations[0]
	for _, obs := range observations[1:] {
		if obs.ObservedAt.After(best.ObservedAt) {
			best = obs
		}
	}

	window, err := companion.ParseVisitWindow(best.Payload)
	if err != nil {
		return "", fmt.Errorf("decode visit window from %s: %w", best.ClientID, err)
	}

	now := time.Now()
	result.Device = best.ClientID
	result.WindowHours = window.WindowHours
	result.Truncated = window.Truncated
	if !window.CapturedAt.IsZero() {
		result.CapturedAgo = promptfmt.FormatDeltaOnly(window.CapturedAt, now)
		result.CapturedAt = window.CapturedAt.In(now.Location()).Format(time.RFC3339)
	}

	visits := window.Visits
	if len(visits) > maxRecentPlaces {
		result.Omitted = len(visits) - maxRecentPlaces
		visits = visits[:maxRecentPlaces]
	}
	for _, v := range visits {
		place := placeView{
			State:              placeState(v.State),
			ArrivalTimed:       v.ArrivedAt != nil,
			DwellStillAccruing: v.DwellIsPartial,
			Latitude:           v.Latitude,
			Longitude:          v.Longitude,
			AccuracyMeters:     v.HorizontalAccuracyMeters,
		}
		if v.ArrivedAt != nil {
			place.Arrived = promptfmt.FormatDeltaOnly(*v.ArrivedAt, now)
			place.ArrivedAt = v.ArrivedAt.In(now.Location()).Format(time.RFC3339)
		}
		if v.DepartedAt != nil {
			place.Departed = promptfmt.FormatDeltaOnly(*v.DepartedAt, now)
			place.DepartedAt = v.DepartedAt.In(now.Location()).Format(time.RFC3339)
		}
		if v.DwellSeconds > 0 {
			place.Dwell = (time.Duration(v.DwellSeconds) * time.Second).Round(time.Minute).String()
		}
		result.Places = append(result.Places, place)
	}

	switch {
	case len(result.Places) == 0:
		result.Basis = "the device published a visit window containing no stays"
	case result.Places[0].State == "here_now":
		result.Basis = "the leading place is where they are now; earlier stays follow in reverse order"
	default:
		result.Basis = "no stay is currently underway — they are between places, or moving"
	}
	return marshalRecentPlaces(result)
}

func marshalRecentPlaces(result recentPlacesResult) (string, error) {
	out, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal recent places: %w", err)
	}
	return string(out), nil
}

func placeState(deviceState string) string {
	if strings.EqualFold(strings.TrimSpace(deviceState), "ongoing") {
		return "here_now"
	}
	return "left"
}
