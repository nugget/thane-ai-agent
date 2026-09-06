package companion

import (
	"encoding/json"
	"testing"
	"time"
)

// productionVisitWindow is a real payload from an operator's phone: two
// errands and a return home. It carries six entries for four stays,
// which is the shape the parser exists to normalize.
const productionVisitWindow = `{"captured_at":"2026-09-06T17:05:50.111Z","max_entries":16,"returned_count":6,"truncated":false,"visits":[
{"arrival":"precise","arrived_at":"2026-09-06T17:05:13.098Z","captured_at":"2026-09-06T17:05:50.110Z","dwell_is_partial":true,"dwell_seconds":37.011552929878235,"horizontal_accuracy_meters":5,"latitude":29.831151442236557,"longitude":-98.464568898586,"state":"ongoing"},
{"arrival":"precise","arrived_at":"2026-09-06T16:47:26.719Z","captured_at":"2026-09-06T16:59:22.041Z","departed_at":"2026-09-06T16:58:45.128Z","dwell_is_partial":false,"dwell_seconds":678.4090310335159,"horizontal_accuracy_meters":108.75376219064817,"latitude":29.79732248483415,"longitude":-98.43010016634825,"state":"settled"},
{"arrival":"precise","arrived_at":"2026-09-06T16:47:26.719Z","captured_at":"2026-09-06T16:51:57.871Z","dwell_is_partial":true,"dwell_seconds":271.15273106098175,"horizontal_accuracy_meters":11.519004002296295,"latitude":29.796542773386278,"longitude":-98.42982839849608,"state":"ongoing"},
{"arrival":"precise","arrived_at":"2026-09-06T16:21:51.633Z","captured_at":"2026-09-06T16:45:58.082Z","departed_at":"2026-09-06T16:45:20.669Z","dwell_is_partial":false,"dwell_seconds":1409.0365599393845,"horizontal_accuracy_meters":54.07629544931456,"latitude":29.797003751266306,"longitude":-98.41852298892312,"state":"settled"},
{"arrival":"precise","arrived_at":"2026-09-06T16:21:51.633Z","captured_at":"2026-09-06T16:37:18.763Z","dwell_is_partial":true,"dwell_seconds":927.1302980184555,"horizontal_accuracy_meters":50.47494742062936,"latitude":29.79694558881348,"longitude":-98.4184658414713,"state":"ongoing"},
{"arrival":"unknown","captured_at":"2026-09-06T16:12:23.138Z","departed_at":"2026-09-06T16:11:57.501Z","dwell_is_partial":false,"horizontal_accuracy_meters":13.056274755587332,"latitude":29.83122951133177,"longitude":-98.46431478315205,"state":"settled"}],
"window_hours":48}`

// TestParseVisitWindowCollapsesRepublishedStays is the whole reason this
// parser exists. The device republishes a stay as its state advances, so
// taking the array at face value reports six stops for four — the same
// place visited twice, minutes apart, which is a false claim about a
// person's day.
func TestParseVisitWindowCollapsesRepublishedStays(t *testing.T) {
	window, err := ParseVisitWindow(json.RawMessage(productionVisitWindow))
	if err != nil {
		t.Fatalf("ParseVisitWindow: %v", err)
	}
	if window.ReturnedCount != 6 {
		t.Errorf("ReturnedCount = %d, want the raw 6 preserved", window.ReturnedCount)
	}
	if len(window.Visits) != 4 {
		t.Fatalf("got %d distinct visits, want 4: %+v", len(window.Visits), window.Visits)
	}

	// Newest first, and the settled capture wins over the ongoing one
	// because it carries the departure and the final dwell.
	first := window.Visits[0]
	if first.State != "ongoing" || first.DepartedAt != nil {
		t.Errorf("newest visit should be the current ongoing stay, got %+v", first)
	}
	second := window.Visits[1]
	if second.State != "settled" || second.DepartedAt == nil {
		t.Fatalf("second visit should be settled with a departure, got %+v", second)
	}
	if got := second.DwellSeconds; got < 678 || got > 679 {
		t.Errorf("second visit dwell = %v, want the settled 678s not the ongoing 271s", got)
	}
	if second.DwellIsPartial {
		t.Error("a settled stay must not report a partial dwell")
	}
}

// TestParseVisitWindowKeepsAnUntimedArrival covers the stay already
// underway when monitoring began. It has no arrival, and inventing one
// would be worse than reporting the gap, so it is identified by its
// departure and kept.
func TestParseVisitWindowKeepsAnUntimedArrival(t *testing.T) {
	window, err := ParseVisitWindow(json.RawMessage(productionVisitWindow))
	if err != nil {
		t.Fatalf("ParseVisitWindow: %v", err)
	}
	oldest := window.Visits[len(window.Visits)-1]
	if oldest.Arrival != "unknown" || oldest.ArrivedAt != nil {
		t.Fatalf("oldest visit should carry an unknown arrival, got %+v", oldest)
	}
	if oldest.DepartedAt == nil {
		t.Fatal("an untimed arrival still has a departure and must be kept")
	}
	want := time.Date(2026, 9, 6, 16, 11, 57, 501000000, time.UTC)
	if !oldest.DepartedAt.Equal(want) {
		t.Errorf("departure = %v, want %v", oldest.DepartedAt, want)
	}
}

// TestParseVisitWindowOrdersNewestFirst pins the order a reader depends
// on: the current stay leads, and the sequence reads backwards through
// the day.
func TestParseVisitWindowOrdersNewestFirst(t *testing.T) {
	window, err := ParseVisitWindow(json.RawMessage(productionVisitWindow))
	if err != nil {
		t.Fatalf("ParseVisitWindow: %v", err)
	}
	for i := 1; i < len(window.Visits); i++ {
		prev, cur := window.Visits[i-1], window.Visits[i]
		if cur.sortKey().After(prev.sortKey()) {
			t.Fatalf("visit %d (%v) is newer than visit %d (%v)", i, cur.sortKey(), i-1, prev.sortKey())
		}
	}
}

func TestParseVisitWindowHandlesAbsentAndMalformed(t *testing.T) {
	if w, err := ParseVisitWindow(nil); err != nil || len(w.Visits) != 0 {
		t.Errorf("nil payload = %+v, %v; want an empty window and no error", w, err)
	}
	if _, err := ParseVisitWindow(json.RawMessage(`{"visits":`)); err == nil {
		t.Error("malformed payload accepted; a reader would see an empty window and call it truth")
	}
	// A timestamp the device could not format must not become the zero
	// time, which would sort as the oldest visit ever recorded.
	w, err := ParseVisitWindow(json.RawMessage(`{"visits":[{"arrived_at":"not-a-time","latitude":1,"longitude":2}]}`))
	if err != nil {
		t.Fatalf("ParseVisitWindow: %v", err)
	}
	if len(w.Visits) != 1 || w.Visits[0].ArrivedAt != nil {
		t.Errorf("unparseable arrival should be absent, got %+v", w.Visits)
	}
}

// TestParseVisitWindowPrefersTheLatestCaptureRegardlessOfOrder pins the
// comparison rather than the array order. The production payload happens
// to list the settled capture before the ongoing one, so a parser that
// simply kept the first entry it saw would agree with a correct one on
// that data and disagree the first time the device emits them the other
// way round.
func TestParseVisitWindowPrefersTheLatestCaptureRegardlessOfOrder(t *testing.T) {
	// Same stay twice, ongoing listed FIRST, settled second.
	payload := `{"captured_at":"2026-09-06T17:00:00Z","visits":[
{"arrival":"precise","arrived_at":"2026-09-06T16:21:51Z","captured_at":"2026-09-06T16:37:18Z","dwell_is_partial":true,"dwell_seconds":927,"latitude":29.7969,"longitude":-98.4184,"state":"ongoing"},
{"arrival":"precise","arrived_at":"2026-09-06T16:21:51Z","captured_at":"2026-09-06T16:45:58Z","departed_at":"2026-09-06T16:45:20Z","dwell_is_partial":false,"dwell_seconds":1409,"latitude":29.7970,"longitude":-98.4185,"state":"settled"}]}`

	window, err := ParseVisitWindow(json.RawMessage(payload))
	if err != nil {
		t.Fatalf("ParseVisitWindow: %v", err)
	}
	if len(window.Visits) != 1 {
		t.Fatalf("got %d visits, want the pair collapsed to 1: %+v", len(window.Visits), window.Visits)
	}
	got := window.Visits[0]
	if got.State != "settled" || got.DepartedAt == nil {
		t.Fatalf("kept the earlier ongoing capture instead of the later settled one: %+v", got)
	}
	if got.DwellSeconds != 1409 {
		t.Errorf("dwell = %v, want the settled 1409", got.DwellSeconds)
	}
	if got.DwellIsPartial {
		t.Error("kept a partial dwell for a stay that has finished")
	}
}
