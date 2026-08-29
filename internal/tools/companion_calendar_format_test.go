package tools

import (
	"encoding/json"
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

func TestRenderCalendarEvent(t *testing.T) {
	// A Saturday mid-morning in Chicago, during daylight time.
	now := mustTime(t, "2026-08-29T10:48:00-05:00")

	tests := []struct {
		name  string
		now   string
		event companionCalendarEvent
		want  calendarEventJSON
	}{
		{
			name: "a home-zone event carries home instants and a delta, nothing more",
			event: companionCalendarEvent{
				Title: "Standup", Calendar: "Work",
				Start: "2026-08-29T09:00:00-05:00",
				End:   "2026-08-29T09:30:00-05:00",
			},
			want: calendarEventJSON{
				Title: "Standup", Calendar: "Work", Day: "Sat",
				Start: "2026-08-29T09:00:00-05:00", End: "2026-08-29T09:30:00-05:00",
				StartDelta: "-1h48m",
			},
		},
		{
			// A bare Z is an older companion discarding the zone, not an
			// event scheduled on UTC's clock: it converts to home and earns
			// no event-local reading.
			name: "a UTC instant from an older companion converts to home",
			event: companionCalendarEvent{
				Title: "Standup",
				Start: "2026-08-29T14:00:00Z",
				End:   "2026-08-29T14:30:00Z",
			},
			want: calendarEventJSON{
				Title: "Standup", Day: "Sat",
				Start: "2026-08-29T09:00:00-05:00", End: "2026-08-29T09:30:00-05:00",
				StartDelta: "-1h48m",
			},
		},
		{
			name: "an event in a declared zone carries both readings",
			event: companionCalendarEvent{
				Title: "Berlin kickoff", TimeZone: "Europe/Berlin",
				Start: "2026-08-29T21:00:00+02:00",
				End:   "2026-08-29T22:00:00+02:00",
			},
			want: calendarEventJSON{
				Title: "Berlin kickoff", Day: "Sat",
				Start: "2026-08-29T14:00:00-05:00", End: "2026-08-29T15:00:00-05:00",
				StartDelta:      "+3h12m",
				EventZone:       "Europe/Berlin",
				EventLocalStart: "2026-08-29T21:00:00+02:00",
				EventLocalEnd:   "2026-08-29T22:00:00+02:00",
			},
		},
		{
			// No declared zone: the embedded offsets are the only evidence,
			// so the local reading keeps them and no zone is named.
			name: "an offset-only event keeps its offsets and names no zone",
			event: companionCalendarEvent{
				Title: "Somewhere east",
				Start: "2026-08-29T21:00:00+02:00",
				End:   "2026-08-29T22:00:00+02:00",
			},
			want: calendarEventJSON{
				Title: "Somewhere east", Day: "Sat",
				Start: "2026-08-29T14:00:00-05:00", End: "2026-08-29T15:00:00-05:00",
				StartDelta:      "+3h12m",
				EventLocalStart: "2026-08-29T21:00:00+02:00",
				EventLocalEnd:   "2026-08-29T22:00:00+02:00",
			},
		},
		{
			name: "a zone that keeps the same clock as home adds nothing",
			event: companionCalendarEvent{
				Title: "Winnipeg call", TimeZone: "America/Winnipeg",
				Start: "2026-08-29T14:00:00-05:00",
				End:   "2026-08-29T15:00:00-05:00",
			},
			want: calendarEventJSON{
				Title: "Winnipeg call", Day: "Sat",
				Start: "2026-08-29T14:00:00-05:00", End: "2026-08-29T15:00:00-05:00",
				StartDelta: "+3h12m",
			},
		},
		{
			// Phoenix never leaves -07:00; Los Angeles shares it until the
			// fall-back, then drops to -08:00. Comparing only the start
			// would find them equal and suppress a reading the end needs.
			name: "a zone that diverges only by the end is still emitted",
			now:  "2026-11-01T00:00:00-07:00",
			event: companionCalendarEvent{
				Title: "LA overnight", TimeZone: "America/Los_Angeles",
				Start: "2026-11-01T01:30:00-07:00",
				End:   "2026-11-01T01:30:00-08:00",
			},
			want: calendarEventJSON{
				Title: "LA overnight", Day: "Sun",
				Start: "2026-11-01T02:30:00-06:00", End: "2026-11-01T03:30:00-06:00",
				StartDelta:      "+1h30m",
				EventZone:       "America/Los_Angeles",
				EventLocalStart: "2026-11-01T01:30:00-07:00",
				EventLocalEnd:   "2026-11-01T01:30:00-08:00",
			},
		},
		{
			// A span across a remote transition: each end keeps its own
			// embedded offset rather than being forced into the other's,
			// so the 4:00 AM end stays 4:00 AM.
			name: "an offset-only span across a remote transition keeps both offsets",
			event: companionCalendarEvent{
				Title: "Overnight abroad",
				Start: "2026-09-01T23:00:00+01:00",
				End:   "2026-09-02T04:00:00+02:00",
			},
			want: calendarEventJSON{
				Title: "Overnight abroad", Day: "Tue",
				Start: "2026-09-01T17:00:00-05:00", End: "2026-09-01T21:00:00-05:00",
				StartDelta:      "+3d6h",
				EventLocalStart: "2026-09-01T23:00:00+01:00",
				EventLocalEnd:   "2026-09-02T04:00:00+02:00",
			},
		},
		{
			// Fall back in Chicago: the home instants themselves carry the
			// two offsets, -05:00 then -06:00, with no labeling to get wrong.
			name: "a span across home's fall-back shows both home offsets",
			now:  "2026-11-01T00:00:00-05:00",
			event: companionCalendarEvent{
				Title: "Repeated hour",
				Start: "2026-11-01T01:30:00-05:00",
				End:   "2026-11-01T01:30:00-06:00",
			},
			want: calendarEventJSON{
				Title: "Repeated hour", Day: "Sun",
				Start: "2026-11-01T01:30:00-05:00", End: "2026-11-01T01:30:00-06:00",
				StartDelta: "+1h30m",
			},
		},
		{
			name: "an event with no end omits end fields",
			event: companionCalendarEvent{
				Title: "Ping",
				Start: "2026-08-29T09:00:00-05:00",
			},
			want: calendarEventJSON{
				Title: "Ping", Day: "Sat",
				Start:      "2026-08-29T09:00:00-05:00",
				StartDelta: "-1h48m",
			},
		},
		{
			name: "an all-day date renders as inclusive dates with a day word",
			event: companionCalendarEvent{
				Title: "Trash day", Calendar: "Home", AllDay: true,
				Start: "2026-08-30", End: "2026-08-30",
			},
			want: calendarEventJSON{
				Title: "Trash day", Calendar: "Home", AllDay: true, Day: "Sun",
				FirstDay: "2026-08-30", LastDay: "2026-08-30",
				StartDelta: "tomorrow",
			},
		},
		{
			name: "a multi-day all-day event keeps its inclusive last day",
			event: companionCalendarEvent{
				Title: "Conference", AllDay: true,
				Start: "2026-08-31", End: "2026-09-02",
			},
			want: calendarEventJSON{
				Title: "Conference", AllDay: true, Day: "Mon",
				FirstDay: "2026-08-31", LastDay: "2026-09-02",
				StartDelta: "+2d",
			},
		},
		{
			// The bug that started the arc: a Berlin all-day event is local
			// midnight, which an older companion sent as the previous day
			// in UTC. The rounding recovery keeps its own date.
			name: "a legacy all-day instant east of UTC keeps its own date",
			event: companionCalendarEvent{
				Title: "Berlin holiday", AllDay: true,
				Start: "2026-08-28T22:00:00Z", End: "2026-08-29T22:00:00Z",
			},
			want: calendarEventJSON{
				Title: "Berlin holiday", AllDay: true, Day: "Sat",
				FirstDay: "2026-08-29", LastDay: "2026-08-29",
				StartDelta: "today",
			},
		},
		{
			name: "a legacy multi-day all-day end stays exclusive",
			event: companionCalendarEvent{
				Title: "Long weekend", AllDay: true,
				Start: "2026-08-31T05:00:00Z", End: "2026-09-03T05:00:00Z",
			},
			want: calendarEventJSON{
				Title: "Long weekend", AllDay: true, Day: "Mon",
				FirstDay: "2026-08-31", LastDay: "2026-09-02",
				StartDelta: "+2d",
			},
		},
		{
			// Spring forward: a whole-day count taken by subtracting local
			// midnights would see 23h and call the 8th "today" on the 7th.
			name: "a day delta survives the spring-forward short day",
			now:  "2026-03-07T10:00:00-06:00",
			event: companionCalendarEvent{
				Title: "Next day", AllDay: true,
				Start: "2026-03-08", End: "2026-03-08",
			},
			want: calendarEventJSON{
				Title: "Next day", AllDay: true, Day: "Sun",
				FirstDay: "2026-03-08", LastDay: "2026-03-08",
				StartDelta: "tomorrow",
			},
		},
		{
			name: "a malformed boundary is echoed and marked, never read as a time",
			event: companionCalendarEvent{
				Title: "Broken",
				Start: "not a timestamp", End: "also not one",
			},
			want: calendarEventJSON{
				Title:    "Broken",
				Start:    "not a timestamp",
				End:      "also not one",
				Unparsed: true,
			},
		},
	}

	home := chicago(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			at := now
			if tc.now != "" {
				at = mustTime(t, tc.now)
			}
			got := newCalendarRenderer(home, at).renderEvent(tc.event)
			if got != tc.want {
				t.Errorf("renderEvent()\n got: %+v\nwant: %+v", got, tc.want)
			}
		})
	}
}

// TestFormatCompanionCalendarResponseMixedDay locks the full output shape —
// framing header, one JSON object per event, deterministic field order —
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
		{Title: "Conference", Calendar: "Work", AllDay: true, Start: "2026-09-02", End: "2026-09-04"},
	}}, chicago(t), mustTime(t, "2026-08-29T10:48:00-05:00"))

	want := strings.Join([]string{
		"Found 3 macOS calendar events (times in America/Chicago; now Sat 2026-08-29T10:48:00-05:00):",
		`{"title":"Standup","calendar":"Work","day":"Sat","start":"2026-08-29T09:00:00-05:00","end":"2026-08-29T09:30:00-05:00","start_delta":"-1h48m"}`,
		`{"title":"Berlin kickoff","calendar":"Work","day":"Wed","start":"2026-09-02T03:00:00-05:00","end":"2026-09-02T04:30:00-05:00","start_delta":"+3d16h","event_zone":"Europe/Berlin","event_local_start":"2026-09-02T10:00:00+02:00","event_local_end":"2026-09-02T11:30:00+02:00","notes":"Bring the deck."}`,
		`{"title":"Conference","calendar":"Work","all_day":true,"day":"Wed","first_day":"2026-09-02","last_day":"2026-09-04","start_delta":"+4d"}`,
	}, "\n")

	if out != want {
		t.Fatalf("mixed day\n got:\n%s\nwant:\n%s", out, want)
	}
}

// TestRenderedEventsAreValidJSONLines guards the contract the header
// implies: every non-header line parses as a standalone JSON object.
func TestRenderedEventsAreValidJSONLines(t *testing.T) {
	out := formatCompanionCalendarResponse(companionCalendarResponse{Events: []companionCalendarEvent{
		{Title: "A \"quoted\" title\nwith a newline", Start: "2026-08-29T09:00:00-05:00", End: "2026-08-29T10:00:00-05:00"},
		{Title: "All-day", AllDay: true, Start: "2026-08-30", End: "2026-08-30"},
	}}, chicago(t), mustTime(t, "2026-08-29T10:48:00-05:00"))

	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header + 2 event lines, got %d:\n%s", len(lines), out)
	}
	for _, line := range lines[1:] {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Errorf("line is not valid JSON: %v\n%s", err, line)
		}
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

func oversizedCalendarResponse() companionCalendarResponse {
	response := companionCalendarResponse{}
	for i := 0; i < 100; i++ {
		response.Events = append(response.Events, companionCalendarEvent{
			Title:        strings.Repeat("Quarterly planning sync ", 8),
			Calendar:     "Work",
			Start:        "2026-04-02T09:00:00-05:00",
			End:          "2026-04-02T10:00:00-05:00",
			Location:     strings.Repeat("Conference Room A ", 6),
			NotesExcerpt: strings.Repeat("Bring status notes. ", 12),
		})
	}
	return response
}

func TestFormatCompanionCalendarResponseTruncatesOutput(t *testing.T) {
	formatted := formatCompanionCalendarResponse(oversizedCalendarResponse(), chicago(t), time.Now())
	if len(formatted) > maxCompanionCalendarResultBytes {
		t.Fatalf("formatted output exceeded hard cap: got %d, want <= %d", len(formatted), maxCompanionCalendarResultBytes)
	}
	if !strings.Contains(formatted, "[... output truncated;") {
		t.Fatalf("expected truncated note, got: %s", formatted[len(formatted)-200:])
	}
}

func TestFormatCompanionCalendarResponseReportsTruncation(t *testing.T) {
	events := []companionCalendarEvent{{
		Title:    "Standup",
		Calendar: "Work",
		Start:    "2026-08-29T09:00:00-05:00",
		End:      "2026-08-29T09:30:00-05:00",
	}}
	now := mustTime(t, "2026-08-29T10:48:00-05:00")
	const note = "the window held more events than were returned"

	complete := formatCompanionCalendarResponse(
		companionCalendarResponse{Events: events}, chicago(t), now)
	if strings.Contains(complete, note) {
		t.Errorf("a complete result must not claim truncation:\n%s", complete)
	}

	capped := formatCompanionCalendarResponse(
		companionCalendarResponse{Events: events, Truncated: true}, chicago(t), now)
	if !strings.Contains(capped, note) {
		t.Errorf("a capped result must say so:\n%s", capped)
	}
	// The note has to be the last thing read; a reader that stops at the
	// final event is exactly the reader this is for.
	if !strings.HasSuffix(strings.TrimSpace(capped), "narrow it, filter by calendar, or raise limit]") {
		t.Errorf("truncation note should close the output:\n%s", capped)
	}
}

func TestFormatCompanionCalendarResponseKeepsTheCappedMarkerWhenBytesAlsoOverflow(t *testing.T) {
	// A result can be capped by count and oversized in bytes at once — a
	// hundred capped events with fat notes clears the byte ceiling easily.
	// The byte cut slices from the tail, which is exactly where the capped
	// marker lives; it must survive the cut and keep the final word.
	response := oversizedCalendarResponse()
	response.Truncated = true

	out := formatCompanionCalendarResponse(response, chicago(t), mustTime(t, "2026-08-29T10:48:00-05:00"))

	if len(out) > maxCompanionCalendarResultBytes {
		t.Fatalf("output exceeded the byte cap: %d", len(out))
	}
	if !strings.Contains(out, "[... output truncated;") {
		t.Errorf("expected the size note to be present:\n...%s", out[len(out)-300:])
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "narrow it, filter by calendar, or raise limit]") {
		t.Errorf("the capped-events marker must keep the final word:\n...%s", out[len(out)-300:])
	}
}

func TestCompanionCalendarResponseDecodesTruncated(t *testing.T) {
	var resp companionCalendarResponse
	if err := json.Unmarshal([]byte(`{"events":[],"truncated":true}`), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Truncated {
		t.Error("truncated should decode as true")
	}

	// A companion predating the field leaves it absent, which must read as
	// "not told", never as a positive claim of completeness.
	var legacy companionCalendarResponse
	if err := json.Unmarshal([]byte(`{"events":[]}`), &legacy); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if legacy.Truncated {
		t.Error("an absent field must not decode as truncated")
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
