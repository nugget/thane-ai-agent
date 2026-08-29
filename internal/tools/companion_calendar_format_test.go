package tools

import (
	"strings"
	"testing"
	"time"
)

// chicago is the household zone in production and the frame every
// expectation below is written in.
func chicago(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatalf("load America/Chicago: %v", err)
	}
	return loc
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

func TestFormatCompanionCalendarRange(t *testing.T) {
	// A Saturday mid-morning in Chicago, during daylight time.
	now := mustTime(t, "2026-08-29T10:48:00-05:00")

	tests := []struct {
		name  string
		now   string
		event companionCalendarEvent
		want  string
	}{
		{
			name: "timed event renders in the household zone, not UTC",
			event: companionCalendarEvent{
				Start: "2026-08-29T09:00:00-05:00",
				End:   "2026-08-29T09:30:00-05:00",
			},
			want: "Sat Aug 29 9:00AM-9:30AM CDT (-1h48m)",
		},
		{
			// A bare Z is an older companion discarding the zone, not an
			// event scheduled on UTC's clock, so it earns no away reading.
			name: "a UTC instant from an older companion converts to home",
			event: companionCalendarEvent{
				Start: "2026-08-29T14:00:00Z",
				End:   "2026-08-29T14:30:00Z",
			},
			want: "Sat Aug 29 9:00AM-9:30AM CDT (-1h48m)",
		},
		{
			name: "an event in another zone carries both readings",
			event: companionCalendarEvent{
				Start:    "2026-08-29T21:00:00+02:00",
				End:      "2026-08-29T22:00:00+02:00",
				TimeZone: "Europe/Berlin",
			},
			want: "Sat Aug 29 2:00PM-3:00PM CDT (+3h12m) [Europe/Berlin 9:00PM-10:00PM]",
		},
		{
			name: "an offset with no declared zone still earns the second reading",
			event: companionCalendarEvent{
				Start: "2026-08-29T21:00:00+02:00",
				End:   "2026-08-29T22:00:00+02:00",
			},
			want: "Sat Aug 29 2:00PM-3:00PM CDT (+3h12m) [UTC+2 9:00PM-10:00PM]",
		},
		{
			name: "a zone that keeps the same clock as home is not annotated",
			event: companionCalendarEvent{
				Start:    "2026-08-29T14:00:00-05:00",
				End:      "2026-08-29T15:00:00-05:00",
				TimeZone: "America/Winnipeg",
			},
			want: "Sat Aug 29 2:00PM-3:00PM CDT (+3h12m)",
		},
		{
			// The span crosses midnight in Chicago but not in Berlin, so
			// the away reading collapses to the single date it occupies.
			name: "an away reading on another date carries that date",
			event: companionCalendarEvent{
				Start:    "2026-08-29T23:00:00-05:00",
				End:      "2026-08-30T00:00:00-05:00",
				TimeZone: "Europe/Berlin",
			},
			want: "Sat Aug 29 11:00PM CDT -> Sun Aug 30 12:00AM CDT (+12h12m) " +
				"[Europe/Berlin Sun Aug 30 6:00AM-7:00AM]",
		},
		{
			name: "a span crossing midnight repeats the date on both ends",
			event: companionCalendarEvent{
				Start: "2026-08-29T23:00:00-05:00",
				End:   "2026-08-30T01:00:00-05:00",
			},
			want: "Sat Aug 29 11:00PM CDT -> Sun Aug 30 1:00AM CDT (+12h12m)",
		},
		{
			name: "an event outside the current year carries it",
			event: companionCalendarEvent{
				Start: "2027-01-04T09:00:00-06:00",
				End:   "2027-01-04T10:00:00-06:00",
			},
			want: "Mon Jan 4 2027 9:00AM-10:00AM CST (+127d23h)",
		},
		{
			name: "an event with no end renders a single instant",
			event: companionCalendarEvent{
				Start: "2026-08-29T09:00:00-05:00",
			},
			want: "Sat Aug 29 9:00AM CDT (-1h48m)",
		},
		{
			name: "an all-day date renders without a clock or a zone",
			event: companionCalendarEvent{
				Start:  "2026-08-29",
				End:    "2026-08-29",
				AllDay: true,
			},
			want: "Sat Aug 29 (all day, today)",
		},
		{
			name: "a multi-day all-day event names its inclusive last day",
			event: companionCalendarEvent{
				Start:  "2026-08-31",
				End:    "2026-09-02",
				AllDay: true,
			},
			want: "Mon Aug 31 -> Wed Sep 2 (all day, +2d)",
		},
		{
			name: "tomorrow reads as a word rather than a count",
			event: companionCalendarEvent{
				Start:  "2026-08-30",
				End:    "2026-08-30",
				AllDay: true,
			},
			want: "Sun Aug 30 (all day, tomorrow)",
		},
		{
			name: "yesterday reads as a word rather than a count",
			event: companionCalendarEvent{
				Start:  "2026-08-28",
				End:    "2026-08-28",
				AllDay: true,
			},
			want: "Fri Aug 28 (all day, yesterday)",
		},
		{
			name: "an all-day event never shows an hours-and-minutes delta",
			event: companionCalendarEvent{
				Start:  "2026-09-05",
				End:    "2026-09-05",
				AllDay: true,
			},
			want: "Sat Sep 5 (all day, +7d)",
		},
		{
			// The bug that started this: a Berlin all-day event is local
			// midnight, which an older companion sent as the previous day
			// in UTC. Rendering that instant literally lost a day.
			name: "a legacy all-day instant east of UTC keeps its own date",
			event: companionCalendarEvent{
				Start:  "2026-08-28T22:00:00Z",
				End:    "2026-08-29T22:00:00Z",
				AllDay: true,
			},
			want: "Sat Aug 29 (all day, today)",
		},
		{
			name: "a legacy all-day instant west of UTC keeps its own date",
			event: companionCalendarEvent{
				Start:  "2026-08-29T05:00:00Z",
				End:    "2026-08-30T05:00:00Z",
				AllDay: true,
			},
			want: "Sat Aug 29 (all day, today)",
		},
		{
			name: "a legacy multi-day all-day end stays exclusive",
			event: companionCalendarEvent{
				Start:  "2026-08-31T05:00:00Z",
				End:    "2026-09-03T05:00:00Z",
				AllDay: true,
			},
			want: "Mon Aug 31 -> Wed Sep 2 (all day, +2d)",
		},
		{
			// Spring forward in Chicago is 2026-03-08. A whole-day count
			// taken by subtracting local midnights would see 23h here and
			// call the 8th "today" on the 7th.
			name: "a day delta survives the spring-forward short day",
			now:  "2026-03-07T10:00:00-06:00",
			event: companionCalendarEvent{
				Start:  "2026-03-08",
				End:    "2026-03-08",
				AllDay: true,
			},
			want: "Sun Mar 8 (all day, tomorrow)",
		},
		{
			name: "an event spanning the DST boundary keeps each end's own abbreviation",
			now:  "2026-03-07T10:00:00-06:00",
			event: companionCalendarEvent{
				Start: "2026-03-07T23:00:00-06:00",
				End:   "2026-03-08T03:00:00-05:00",
			},
			want: "Sat Mar 7 11:00PM CST -> Sun Mar 8 3:00AM CDT (+13h)",
		},
		{
			name: "a malformed start echoes what the companion sent",
			event: companionCalendarEvent{
				Start: "not a timestamp",
				End:   "also not one",
			},
			want: "not a timestamp - also not one",
		},
		{
			name: "a malformed end still renders the start",
			event: companionCalendarEvent{
				Start: "2026-08-29T09:00:00-05:00",
				End:   "garbage",
			},
			want: "Sat Aug 29 9:00AM CDT (-1h48m)",
		},
	}

	home := chicago(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			at := now
			if tc.now != "" {
				at = mustTime(t, tc.now)
			}
			got := newCalendarRenderer(home, at).formatRange(tc.event)
			if got != tc.want {
				t.Errorf("formatRange()\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestFormatCompanionCalendarResponseStatesItsFrame(t *testing.T) {
	out := formatCompanionCalendarResponse(companionCalendarResponse{
		Events: []companionCalendarEvent{{
			Title:    "Design sync",
			Calendar: "Work",
			Start:    "2026-08-29T09:00:00-05:00",
			End:      "2026-08-29T09:30:00-05:00",
		}},
	}, chicago(t), mustTime(t, "2026-08-29T10:48:00-05:00"))

	want := "Found 1 macOS calendar events (times in America/Chicago; now Sat Aug 29 10:48AM CDT):" +
		"\n- Sat Aug 29 9:00AM-9:30AM CDT (-1h48m) | Design sync (Work)"
	if out != want {
		t.Fatalf("response\n got: %s\nwant: %s", out, want)
	}
}

func TestFormatCompanionCalendarResponseFallsBackToLocalZone(t *testing.T) {
	// A nil home must not panic or silently render UTC; the host's local
	// zone is the honest answer when nothing was configured.
	out := formatCompanionCalendarResponse(companionCalendarResponse{
		Events: []companionCalendarEvent{{
			Title: "Anything",
			Start: "2026-08-29T09:00:00-05:00",
		}},
	}, nil, mustTime(t, "2026-08-29T10:48:00-05:00"))

	if !strings.HasPrefix(out, "Found 1 macOS calendar events (times in ") {
		t.Fatalf("expected a stated frame, got: %s", out)
	}
}

func TestFormatCompanionCalendarResponseEmpty(t *testing.T) {
	out := formatCompanionCalendarResponse(companionCalendarResponse{}, chicago(t), time.Now())
	if out != "No macOS calendar events found in the requested window." {
		t.Fatalf("empty response: got %q", out)
	}
}

func TestFormatCompanionCalendarResponseTruncatesOutput(t *testing.T) {
	response := companionCalendarResponse{
		Events: make([]companionCalendarEvent, 0, 80),
	}
	for i := 0; i < 80; i++ {
		response.Events = append(response.Events, companionCalendarEvent{
			Title:        strings.Repeat("Quarterly planning sync ", 8),
			Calendar:     "Work",
			Start:        "2026-04-02T09:00:00-05:00",
			End:          "2026-04-02T10:00:00-05:00",
			Location:     strings.Repeat("Conference Room A ", 6),
			NotesExcerpt: strings.Repeat("Bring status notes. ", 12),
		})
	}

	formatted := formatCompanionCalendarResponse(response, chicago(t), time.Now())
	if len(formatted) > maxCompanionCalendarResultBytes {
		t.Fatalf("formatted output exceeded hard cap: got %d, want <= %d", len(formatted), maxCompanionCalendarResultBytes)
	}
	if !strings.Contains(formatted, "[... output truncated;") {
		t.Fatalf("expected truncated note, got: %s", formatted)
	}
}

// TestFormatCompanionCalendarResponseMixedDay renders a whole result set
// of the kinds this household actually keeps — same-zone meetings, a trip
// abroad, a flight crossing both midnight and a zone, and all-day events —
// so a change to any one rule shows up as a change to the thing a model
// reads rather than to a fragment of it.
func TestFormatCompanionCalendarResponseMixedDay(t *testing.T) {
	out := formatCompanionCalendarResponse(companionCalendarResponse{Events: []companionCalendarEvent{
		{
			Title: "Standup", Calendar: "Work", TimeZone: "America/Chicago",
			Start: "2026-08-29T09:00:00-05:00", End: "2026-08-29T09:30:00-05:00",
		},
		{
			Title: "Berlin kickoff", Calendar: "Work", TimeZone: "Europe/Berlin",
			Start: "2026-09-02T10:00:00+02:00", End: "2026-09-02T11:30:00+02:00",
			NotesExcerpt: "Bring the deck.",
		},
		{
			Title: "Flight to Berlin", Calendar: "Travel", TimeZone: "America/Chicago",
			Start: "2026-09-01T18:35:00-05:00", End: "2026-09-02T10:05:00+02:00",
		},
		{Title: "Conference", Calendar: "Work", Start: "2026-09-02", End: "2026-09-04", AllDay: true},
		{Title: "Trash day", Calendar: "Home", Start: "2026-08-30", End: "2026-08-30", AllDay: true},
	}}, chicago(t), mustTime(t, "2026-08-29T10:48:00-05:00"))

	want := strings.Join([]string{
		"Found 5 macOS calendar events (times in America/Chicago; now Sat Aug 29 10:48AM CDT):",
		"- Sat Aug 29 9:00AM-9:30AM CDT (-1h48m) | Standup (Work)",
		"- Wed Sep 2 3:00AM-4:30AM CDT (+3d16h) [Europe/Berlin 10:00AM-11:30AM] | Berlin kickoff (Work)",
		"  Notes: Bring the deck.",
		"- Tue Sep 1 6:35PM CDT -> Wed Sep 2 3:05AM CDT (+3d7h) | Flight to Berlin (Travel)",
		"- Wed Sep 2 -> Fri Sep 4 (all day, +4d) | Conference (Work)",
		"- Sun Aug 30 (all day, tomorrow) | Trash day (Home)",
	}, "\n")

	if out != want {
		t.Fatalf("mixed day\n got:\n%s\nwant:\n%s", out, want)
	}
}

func TestParseCompanionTimeArg(t *testing.T) {
	home := chicago(t)
	fallback := mustTime(t, "2026-08-29T10:48:00-05:00")

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "an offset is taken at face value",
			value: "2026-08-29T14:00:00+02:00",
			want:  "2026-08-29T07:00:00-05:00",
		},
		{
			name:  "an explicit Z is taken at face value",
			value: "2026-08-29T14:00:00Z",
			want:  "2026-08-29T09:00:00-05:00",
		},
		{
			// The bug: a zone-less parse defaults to UTC, shifting the
			// requested window by the household offset.
			name:  "a zone-less timestamp is read in the household zone",
			value: "2026-08-29 14:00:00",
			want:  "2026-08-29T14:00:00-05:00",
		},
		{
			name:  "a zone-less timestamp may omit its seconds",
			value: "2026-08-29 14:00",
			want:  "2026-08-29T14:00:00-05:00",
		},
		{
			name:  "a bare date is household midnight, not UTC midnight",
			value: "2026-08-29",
			want:  "2026-08-29T00:00:00-05:00",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCompanionTimeArg(map[string]any{"start": tc.value}, "start", home, fallback)
			if err != nil {
				t.Fatalf("parseCompanionTimeArg(%q): %v", tc.value, err)
			}
			if !got.Equal(mustTime(t, tc.want)) {
				t.Errorf("parseCompanionTimeArg(%q) = %s, want %s", tc.value, got.Format(time.RFC3339), tc.want)
			}
		})
	}
}

func TestParseCompanionTimeArgDefaultsAndRejects(t *testing.T) {
	home := chicago(t)
	fallback := mustTime(t, "2026-08-29T10:48:00-05:00")

	got, err := parseCompanionTimeArg(map[string]any{}, "start", home, fallback)
	if err != nil {
		t.Fatalf("missing value should fall back: %v", err)
	}
	if !got.Equal(fallback) {
		t.Errorf("missing value = %s, want the fallback %s", got, fallback)
	}

	if _, err := parseCompanionTimeArg(map[string]any{"start": "next tuesday"}, "start", home, fallback); err == nil {
		t.Fatal("expected an unparseable value to be rejected")
	}
}
