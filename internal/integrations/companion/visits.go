package companion

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// VisitObservationKind is the observation kind carrying the visit window.
const VisitObservationKind = "ios.visits"

// VisitWindow is one companion's rolling record of the places it decided
// mattered. It arrives as a whole window rather than one visit per
// observation, which is what makes it survive latest-only storage: the
// single retained row still carries the recent sequence.
type VisitWindow struct {
	CapturedAt    time.Time `json:"captured_at"`
	WindowHours   float64   `json:"window_hours,omitempty"`
	MaxEntries    int       `json:"max_entries,omitempty"`
	ReturnedCount int       `json:"returned_count,omitempty"`
	Truncated     bool      `json:"truncated,omitempty"`
	Visits        []Visit   `json:"visits,omitempty"`
}

// Visit is one stay the device's platform judged significant. The
// judgment is the device's, made on sensor data the server never sees,
// which is why a visit is worth more than the fix that would otherwise
// stand in for it: somebody already decided this was a place.
type Visit struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	// HorizontalAccuracyMeters is the platform's own confidence in the
	// coordinate, not a dwell-area radius.
	HorizontalAccuracyMeters float64 `json:"horizontal_accuracy_meters,omitempty"`
	// Arrival is "precise" when the platform timed the arrival, and
	// "unknown" when the stay was already underway before monitoring
	// began — an honest absence rather than a guessed timestamp.
	Arrival    string     `json:"arrival,omitempty"`
	ArrivedAt  *time.Time `json:"arrived_at,omitempty"`
	DepartedAt *time.Time `json:"departed_at,omitempty"`
	// State is "ongoing" while the device is still there, "settled"
	// once it has left.
	State string `json:"state,omitempty"`
	// DwellSeconds is how long the stay lasted. DwellIsPartial marks a
	// dwell still accruing, so a reader never reports an in-progress
	// stay as a finished one.
	DwellSeconds   float64 `json:"dwell_seconds,omitempty"`
	DwellIsPartial bool    `json:"dwell_is_partial,omitempty"`
}

// identity keys a visit for deduplication. A window carries the same
// stay more than once — captured while ongoing, then again once settled
// — so the arrival time is the stay's identity. A stay that began before
// monitoring has no arrival, and its departure identifies it instead.
func (v Visit) identity() string {
	switch {
	case v.ArrivedAt != nil:
		return "arrived:" + v.ArrivedAt.UTC().Format(time.RFC3339Nano)
	case v.DepartedAt != nil:
		return "departed:" + v.DepartedAt.UTC().Format(time.RFC3339Nano)
	default:
		return fmt.Sprintf("position:%.6f,%.6f", v.Latitude, v.Longitude)
	}
}

// sortKey is the moment a visit is ordered by: when it began, or when it
// ended for a stay whose beginning was never observed.
func (v Visit) sortKey() time.Time {
	if v.ArrivedAt != nil {
		return *v.ArrivedAt
	}
	if v.DepartedAt != nil {
		return *v.DepartedAt
	}
	return time.Time{}
}

// ParseVisitWindow decodes a stored ios.visits payload into distinct
// visits, newest first.
//
// Deduplication is the point. The device republishes a stay as its state
// advances, so a six-entry window is commonly four stays: the same
// arrival appears once ongoing and once settled. A reader that takes the
// array at face value reports a person visiting the same place twice in
// a row, minutes apart. The later capture wins, because it carries the
// departure and the final dwell.
func ParseVisitWindow(payload json.RawMessage) (VisitWindow, error) {
	if len(payload) == 0 {
		return VisitWindow{}, nil
	}
	var raw struct {
		CapturedAt    string  `json:"captured_at"`
		WindowHours   float64 `json:"window_hours"`
		MaxEntries    int     `json:"max_entries"`
		ReturnedCount int     `json:"returned_count"`
		Truncated     bool    `json:"truncated"`
		Visits        []struct {
			Latitude                 float64 `json:"latitude"`
			Longitude                float64 `json:"longitude"`
			HorizontalAccuracyMeters float64 `json:"horizontal_accuracy_meters"`
			Arrival                  string  `json:"arrival"`
			ArrivedAt                string  `json:"arrived_at"`
			DepartedAt               string  `json:"departed_at"`
			CapturedAt               string  `json:"captured_at"`
			State                    string  `json:"state"`
			DwellSeconds             float64 `json:"dwell_seconds"`
			DwellIsPartial           bool    `json:"dwell_is_partial"`
		} `json:"visits"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return VisitWindow{}, fmt.Errorf("decode visit window: %w", err)
	}

	window := VisitWindow{
		WindowHours:   raw.WindowHours,
		MaxEntries:    raw.MaxEntries,
		ReturnedCount: raw.ReturnedCount,
		Truncated:     raw.Truncated,
	}
	window.CapturedAt, _ = parseVisitTime(raw.CapturedAt)

	// Keyed by stay identity, keeping the latest capture of each.
	type candidate struct {
		visit      Visit
		capturedAt time.Time
	}
	best := make(map[string]candidate, len(raw.Visits))
	for _, entry := range raw.Visits {
		visit := Visit{
			Latitude:                 entry.Latitude,
			Longitude:                entry.Longitude,
			HorizontalAccuracyMeters: entry.HorizontalAccuracyMeters,
			Arrival:                  strings.TrimSpace(entry.Arrival),
			State:                    strings.TrimSpace(entry.State),
			DwellSeconds:             entry.DwellSeconds,
			DwellIsPartial:           entry.DwellIsPartial,
		}
		if at, ok := parseVisitTime(entry.ArrivedAt); ok {
			visit.ArrivedAt = &at
		}
		if at, ok := parseVisitTime(entry.DepartedAt); ok {
			visit.DepartedAt = &at
		}
		capturedAt, _ := parseVisitTime(entry.CapturedAt)

		key := visit.identity()
		if existing, seen := best[key]; seen && existing.capturedAt.After(capturedAt) {
			continue
		}
		best[key] = candidate{visit: visit, capturedAt: capturedAt}
	}

	window.Visits = make([]Visit, 0, len(best))
	for _, c := range best {
		window.Visits = append(window.Visits, c.visit)
	}
	sort.SliceStable(window.Visits, func(i, j int) bool {
		return window.Visits[i].sortKey().After(window.Visits[j].sortKey())
	})
	return window, nil
}

func parseVisitTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}
