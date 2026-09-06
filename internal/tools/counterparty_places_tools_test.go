package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	companion "github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

// newPlacesFixture binds one contact to one companion account and seeds
// the account's device with a visit window.
func newPlacesFixture(t *testing.T, window string) (CounterpartyToolDeps, string) {
	t.Helper()
	cdb, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("contacts db: %v", err)
	}
	t.Cleanup(func() { cdb.Close() })
	contactStore, err := contacts.NewStore(cdb, slog.Default())
	if err != nil {
		t.Fatalf("contacts store: %v", err)
	}
	alice, err := contactStore.Upsert(&contacts.Contact{FormattedName: "Alice Operator"})
	if err != nil {
		t.Fatal(err)
	}

	companionStore := newObservationToolStoreForContact(t, map[string]string{"alice-acct": alice.ID.String()})
	ctx := context.Background()
	now := time.Now().UTC()
	if err := companionStore.RecordConnected(ctx, "alice-acct", "iphone", companion.DeviceMetadata{Platform: "ios"}, now); err != nil {
		t.Fatalf("record device: %v", err)
	}
	device, found, err := companionStore.Get(ctx, "alice-acct", "iphone")
	if err != nil || !found {
		t.Fatalf("get device: found=%t err=%v", found, err)
	}
	if window != "" {
		if _, err := companionStore.IngestObservations(ctx,
			companion.ObservationPrincipal{Account: "alice-acct", DeviceID: device.DeviceID},
			companion.ObservationBatch{
				ObservationDeviceMetadata: companion.ObservationDeviceMetadata{ClientID: "iphone", Platform: "ios"},
				Events: []companion.ObservationEvent{{
					EventID: "visits-1", Kind: companion.VisitObservationKind, SchemaVersion: 1,
					Status: companion.ObservationAvailable, ObservedAt: now,
					Payload: json.RawMessage(window),
				}},
			}, now); err != nil {
			t.Fatalf("ingest visits: %v", err)
		}
	}
	return CounterpartyToolDeps{Contacts: contactStore, Companions: companionStore}, alice.ID.String()
}

// TestRecentPlacesCollapsesAndOrdersRealWindow runs a production payload
// end to end: six raw entries, four stays, current one first.
func TestRecentPlacesCollapsesAndOrdersRealWindow(t *testing.T) {
	deps, contactID := newPlacesFixture(t, productionVisitWindowJSON)
	out, err := handleContactRecentPlaces(context.Background(), deps, "", contactID)
	if err != nil {
		t.Fatalf("handleContactRecentPlaces: %v", err)
	}
	var result recentPlacesResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, out)
	}
	if len(result.Places) != 4 {
		t.Fatalf("got %d places, want 4 distinct stays: %s", len(result.Places), out)
	}
	if result.Places[0].State != "here_now" {
		t.Errorf("leading place = %q, want here_now", result.Places[0].State)
	}
	if !result.Places[0].DwellStillAccruing {
		t.Error("an ongoing stay must mark its dwell as still accruing")
	}
	// The completed errand keeps the settled dwell, not the partial one
	// captured while it was still underway.
	if got := result.Places[1].Dwell; got != "11m0s" {
		t.Errorf("second place dwell = %q, want the settled 11m0s", got)
	}
	if result.Places[1].DwellStillAccruing {
		t.Error("a finished stay must not mark its dwell as accruing")
	}
	// The stay underway before monitoring began reports no arrival
	// rather than a zero one.
	oldest := result.Places[len(result.Places)-1]
	if oldest.ArrivalTimed || oldest.Arrived != "" {
		t.Errorf("oldest place claims a timed arrival: %+v", oldest)
	}
	if !strings.Contains(result.Basis, "where they are now") {
		t.Errorf("basis does not say the leading place is current: %q", result.Basis)
	}
}

// TestRecentPlacesDistinguishesAbsences pins that "no window" does not
// read as "went nowhere".
func TestRecentPlacesDistinguishesAbsences(t *testing.T) {
	deps, contactID := newPlacesFixture(t, "")
	out, err := handleContactRecentPlaces(context.Background(), deps, "", contactID)
	if err != nil {
		t.Fatalf("handleContactRecentPlaces: %v", err)
	}
	var result recentPlacesResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Places) != 0 {
		t.Fatalf("expected no places, got %+v", result.Places)
	}
	for _, want := range []string{"no device is bound", "has not published"} {
		if !strings.Contains(result.Basis, want) {
			t.Errorf("basis %q does not enumerate the possible absences (%q)", result.Basis, want)
		}
	}
}

const productionVisitWindowJSON = `{"captured_at":"2026-09-06T17:05:50.111Z","max_entries":16,"returned_count":6,"truncated":false,"visits":[
{"arrival":"precise","arrived_at":"2026-09-06T17:05:13.098Z","captured_at":"2026-09-06T17:05:50.110Z","dwell_is_partial":true,"dwell_seconds":37.01,"horizontal_accuracy_meters":5,"latitude":29.831151442236557,"longitude":-98.464568898586,"state":"ongoing"},
{"arrival":"precise","arrived_at":"2026-09-06T16:47:26.719Z","captured_at":"2026-09-06T16:59:22.041Z","departed_at":"2026-09-06T16:58:45.128Z","dwell_is_partial":false,"dwell_seconds":678.4,"horizontal_accuracy_meters":108.75,"latitude":29.79732248483415,"longitude":-98.43010016634825,"state":"settled"},
{"arrival":"precise","arrived_at":"2026-09-06T16:47:26.719Z","captured_at":"2026-09-06T16:51:57.871Z","dwell_is_partial":true,"dwell_seconds":271.15,"horizontal_accuracy_meters":11.51,"latitude":29.796542773386278,"longitude":-98.42982839849608,"state":"ongoing"},
{"arrival":"precise","arrived_at":"2026-09-06T16:21:51.633Z","captured_at":"2026-09-06T16:45:58.082Z","departed_at":"2026-09-06T16:45:20.669Z","dwell_is_partial":false,"dwell_seconds":1409.03,"horizontal_accuracy_meters":54.07,"latitude":29.797003751266306,"longitude":-98.41852298892312,"state":"settled"},
{"arrival":"precise","arrived_at":"2026-09-06T16:21:51.633Z","captured_at":"2026-09-06T16:37:18.763Z","dwell_is_partial":true,"dwell_seconds":927.13,"horizontal_accuracy_meters":50.47,"latitude":29.79694558881348,"longitude":-98.4184658414713,"state":"ongoing"},
{"arrival":"unknown","captured_at":"2026-09-06T16:12:23.138Z","departed_at":"2026-09-06T16:11:57.501Z","dwell_is_partial":false,"horizontal_accuracy_meters":13.05,"latitude":29.83122951133177,"longitude":-98.46431478315205,"state":"settled"}],
"window_hours":48}`
