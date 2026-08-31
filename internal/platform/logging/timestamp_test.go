package logging

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"testing"
	"time"
)

// TestTimestampLayoutOrdersLexicographically pins the property every
// window clause in this package depends on: for the shapes that made
// RFC3339Nano misorder (trimmed fractions, bare seconds), the fixed-
// width format's lexicographic order equals time order.
func TestTimestampLayoutOrdersLexicographically(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	instants := []time.Time{
		base,                                    // .000000000 (RFC3339Nano renders no fraction at all)
		base.Add(50 * time.Millisecond),         // .05
		base.Add(100 * time.Millisecond),        // .1  — the trimmed shape that inverted
		base.Add(100001 * time.Microsecond),     // .100001
		base.Add(150 * time.Millisecond),        // .15 — RFC3339Nano sorts this BELOW .1
		base.Add(999999999 * time.Nanosecond),   // .999999999
		base.Add(time.Second),                   // next bare second
		base.Add(time.Second + time.Nanosecond), // .000000001
		time.Date(2026, 8, 31, 7, 0, 0, 5e8, time.FixedZone("CDT", -5*3600)), // non-UTC input normalizes
	}

	formatted := make([]string, len(instants))
	for i, at := range instants {
		formatted[i] = FormatTimestamp(at)
		if len(formatted[i]) != len(FormatTimestamp(base)) {
			t.Fatalf("variable width: %q", formatted[i])
		}
	}

	sorted := append([]string(nil), formatted...)
	sort.Strings(sorted)
	byTime := append([]time.Time(nil), instants...)
	sort.Slice(byTime, func(i, j int) bool { return byTime[i].Before(byTime[j]) })
	for i, at := range byTime {
		if want := FormatTimestamp(at); sorted[i] != want {
			t.Fatalf("lexicographic order diverges from time order at %d: got %q, want %q\nall: %v",
				i, sorted[i], want, formatted)
		}
	}

	// The inversion this exists to kill, stated directly.
	if FormatTimestamp(base.Add(100*time.Millisecond)) >= FormatTimestamp(base.Add(150*time.Millisecond)) {
		t.Fatal(".1s must sort below .15s")
	}
}

// TestQueryWindowSubsecondEdges drives the real write path and a real
// window: a row stamped inside the window must land regardless of how
// its fraction would have trimmed — the flake TestQuery_ExtendedLogEntry
// exposed, pinned deterministically.
func TestQueryWindowSubsecondEdges(t *testing.T) {
	db := openTestDB(t)
	inner := slog.NewJSONHandler(discardWriter{}, nil)
	h := NewIndexHandler(inner, db)

	base := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	stamps := []time.Time{
		base.Add(100 * time.Millisecond), // ".1" under the old format
		base.Add(150 * time.Millisecond),
		base.Add(2 * time.Second), // outside the window below
	}
	for i, at := range stamps {
		rec := slog.NewRecord(at, slog.LevelInfo, fmt.Sprintf("row %d", i), 0)
		if err := h.Handle(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
	}
	h.Close()

	got, err := Query(db, QueryParams{
		Since: base,
		Until: base.Add(151 * time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("window [.0, .151] returned %d rows, want 2 (the sub-second rows)", len(got))
	}
}
