package tools

import (
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
)

// The layouts a companion may use for an event boundary. A timed event
// carries a full RFC3339 timestamp with the offset of the zone the event
// is scheduled in; an all-day event carries a bare calendar date, which
// has no time and no zone by definition.
const calendarDateLayout = "2006-01-02"

// calendarRenderer turns companion calendar events into what the model
// reads: a one-line framing header stating the frame, then one JSON
// object per event (docs/model-facing-context.md — generated runtime
// data defaults to JSON; one object per line for homogeneous lists).
//
// The value of rendering server-side is derivation, not prose. Every
// event resolves against two frames:
//
//   - home, the household zone from configuration, which is the frame the
//     agent's own process reasons in and the one "is this soon?" is asked
//     against — start/end are re-expressed in it, and every event carries
//     a delta so the model never does timestamp arithmetic;
//   - the event's own zone, which on this operator's calendar is a
//     statement of intent — an event recorded in Europe/Berlin means the
//     operator expects to be in Berlin for it, so that reading is the
//     wall clock they will actually live. It is emitted as
//     event_local_start/end whenever it disagrees with home's clock.
type calendarRenderer struct {
	home *time.Location
	now  time.Time
}

func newCalendarRenderer(home *time.Location, now time.Time) calendarRenderer {
	if home == nil {
		home = time.Local
	}
	return calendarRenderer{home: home, now: now.In(home)}
}

// calendarBoundary is a parsed start or end of an event.
type calendarBoundary struct {
	t time.Time

	// dateOnly marks a boundary that named a calendar date rather than a
	// moment. Its clock and zone are placeholders and must never be shown.
	dateOnly bool
}

// parseCalendarBoundary reads one end of an event, preferring the date
// form so an all-day event is recognised before its value is mistaken for
// a midnight instant.
//
// This deliberately does not reuse database.ParseTimestamp: that is the
// read-side salvage parser for SQLite TEXT columns and accepts eight
// historical shapes, none of which a companion emits. Borrowing it here
// would silently accept payloads the contract does not permit and hide
// exactly the drift these formats exist to catch.
func parseCalendarBoundary(value string) (calendarBoundary, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return calendarBoundary{}, false
	}
	if t, err := time.Parse(calendarDateLayout, value); err == nil {
		return calendarBoundary{t: t, dateOnly: true}, true
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, value); err == nil {
			return calendarBoundary{t: t}, true
		}
	}
	return calendarBoundary{}, false
}

// calendarEventJSON is one event as the model reads it. Timed events use
// start/end (RFC3339 in the household zone, end exclusive); all-day
// events use first_day/last_day (bare dates, both inclusive) — distinct
// field names because the two kinds answer inclusivity differently, and
// a reader should never have to know which convention start/end is in.
type calendarEventJSON struct {
	Title    string `json:"title"`
	Calendar string `json:"calendar,omitempty"`
	AllDay   bool   `json:"all_day,omitempty"`

	// Day is the weekday the event starts, in the household frame.
	// Derivable from the date in principle, but models are bad at
	// calendar arithmetic and "what's on Wednesday" is a first-class
	// calendar question.
	Day string `json:"day,omitempty"`

	Start    string `json:"start,omitempty"`
	End      string `json:"end,omitempty"`
	FirstDay string `json:"first_day,omitempty"`
	LastDay  string `json:"last_day,omitempty"`

	// StartDelta is the signed distance from now to the start —
	// "-1h48m", "+3d16h" — or a day word for all-day events ("today",
	// "tomorrow", "+4d"), which occupy a date rather than a moment and
	// must not be rendered as though there were a clock to be early for.
	StartDelta string `json:"start_delta,omitempty"`

	// EventZone is the IANA zone the event declares, present only when
	// the companion named one that reads differently from home. An
	// event's own zone is intent on this calendar: where the operator
	// expects to be standing.
	EventZone string `json:"event_zone,omitempty"`

	// EventLocalStart/End re-express the span on the event's own clock,
	// present only when that clock disagrees with home's at either end.
	// Each carries its boundary's own offset, so a span crossing a DST
	// transition — local or remote — shows both offsets rather than
	// forcing one end into the other's.
	EventLocalStart string `json:"event_local_start,omitempty"`
	EventLocalEnd   string `json:"event_local_end,omitempty"`

	Location string `json:"location,omitempty"`
	Notes    string `json:"notes,omitempty"`
	URL      string `json:"url,omitempty"`

	// Unparsed marks an event whose boundaries did not parse; start/end
	// then echo the companion's bytes verbatim. A malformed timestamp is
	// a bug worth seeing, and the title and location are still useful —
	// but nothing downstream should read those strings as times.
	Unparsed bool `json:"unparsed,omitempty"`
}

// formatCompanionCalendarResponse renders the whole result set, headed by
// the frame every line below it is written in. Stating the zone and the
// current time once removes the arithmetic the model would otherwise have
// to do per line to decide what is imminent.
func formatCompanionCalendarResponse(response companionCalendarResponse, home *time.Location, now time.Time) string {
	if len(response.Events) == 0 {
		return "No macOS calendar events found in the requested window."
	}

	r := newCalendarRenderer(home, now)

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d macOS calendar events (times in %s; now %s %s):",
		len(response.Events), r.homeZoneName(),
		r.now.Format("Mon"), r.now.Format(time.RFC3339))
	for _, event := range response.Events {
		b.WriteString("\n")
		b.WriteString(promptfmt.MarshalCompact(r.renderEvent(event)))
	}

	// A capped result must say so on the line the reader finishes on.
	// Twenty events with nothing appended reads as "there are twenty",
	// and a calendar answer that quietly omits the rest is worse than one
	// that fails: the reader has no reason to look further.
	//
	// Built as a suffix rather than written into the body, because the
	// byte cap below slices from the tail — the exact place this marker
	// has to survive. A result can be capped by count and oversized in
	// bytes at once, and losing the marker to the size cut would defeat
	// the reason it is carried. When both fire, the size note comes first
	// and the capped-events marker keeps the final word.
	const cappedNote = "\n\n[the window held more events than were returned; narrow it, filter by calendar, or raise limit]"
	const sizeNote = "\n\n[... output truncated; narrow the window, filters, or limit for more ...]"

	var suffix string
	if response.Truncated {
		suffix = cappedNote
	}

	body := b.String()
	if len(body)+len(suffix) <= maxCompanionCalendarResultBytes {
		return body + suffix
	}
	suffix = sizeNote + suffix
	allowed := maxCompanionCalendarResultBytes - len(suffix)
	if allowed < 0 {
		allowed = 0
	}
	return truncateUTF8(body, allowed) + suffix
}

// homeZoneName is the IANA name of the reader's frame where one exists.
// A location loaded from configuration carries its name; time.Local does
// not, and reports "Local", which tells a model nothing — fall back to the
// zone abbreviation currently in effect, which at least anchors the offset.
func (r calendarRenderer) homeZoneName() string {
	name := r.home.String()
	if name == "" || name == "Local" {
		abbrev, _ := r.now.Zone()
		return abbrev
	}
	return name
}

// renderEvent derives one event's JSON object.
func (r calendarRenderer) renderEvent(event companionCalendarEvent) calendarEventJSON {
	out := calendarEventJSON{
		Title:    strings.TrimSpace(event.Title),
		Calendar: event.Calendar,
		Location: event.Location,
		Notes:    event.NotesExcerpt,
		URL:      event.URL,
	}

	start, startOK := parseCalendarBoundary(event.Start)
	end, endOK := parseCalendarBoundary(event.End)

	if !startOK {
		out.Start = strings.TrimSpace(event.Start)
		out.End = strings.TrimSpace(event.End)
		out.Unparsed = true
		return out
	}

	if event.AllDay {
		r.renderAllDay(&out, start, end, endOK)
		return out
	}
	r.renderTimed(&out, event, start, end, endOK)
	return out
}

// renderAllDay fills the date fields. An all-day event is a property of a
// date in the place the operator will be, not an interval on any
// particular clock, so it carries no times and no zone.
func (r calendarRenderer) renderAllDay(out *calendarEventJSON, start, end calendarBoundary, endOK bool) {
	first := r.allDayDate(start)
	last := first
	if endOK {
		last = r.allDayLastDate(end, first)
	}

	out.AllDay = true
	out.Day = first.Format("Mon")
	out.FirstDay = first.Format(calendarDateLayout)
	out.LastDay = last.Format(calendarDateLayout)
	out.StartDelta = promptfmt.FormatDayDelta(first, r.now)
}

// renderTimed fills the instant fields, in home with the event's own
// reading added when the two clocks disagree.
func (r calendarRenderer) renderTimed(out *calendarEventJSON, event companionCalendarEvent, start, end calendarBoundary, endOK bool) {
	homeStart := start.t.In(r.home)
	out.Day = homeStart.Format("Mon")
	out.Start = homeStart.Format(time.RFC3339)
	if endOK && end.t.After(start.t) {
		out.End = end.t.In(r.home).Format(time.RFC3339)
	}
	out.StartDelta = promptfmt.FormatDeltaOnly(start.t, r.now)

	awayStart, awayEnd, ok := r.awayReading(event, start, end, endOK)
	if !ok {
		return
	}
	if declaredLocation(event) != nil {
		out.EventZone = strings.TrimSpace(event.TimeZone)
	}
	// Each boundary keeps its own offset. With a declared zone both were
	// converted into it, transitions included; without one, each
	// timestamp's embedded offset is the only evidence there is, and the
	// two can legitimately differ across a transition the event spans.
	out.EventLocalStart = awayStart.Format(time.RFC3339)
	if endOK && end.t.After(start.t) {
		out.EventLocalEnd = awayEnd.Format(time.RFC3339)
	}
}

// awayReading resolves the event on its own clock, and reports whether
// that reading says anything the home reading did not.
func (r calendarRenderer) awayReading(event companionCalendarEvent, start, end calendarBoundary, endOK bool) (time.Time, time.Time, bool) {
	declared := declaredLocation(event)

	// Resolve each boundary in the frame that actually governs it. With a
	// declared zone that is the zone itself, transitions included. Without
	// one, each timestamp's own offset is the only evidence there is.
	awayStart, awayEnd := start.t, end.t
	if declared != nil {
		awayStart, awayEnd = start.t.In(declared), end.t.In(declared)
	} else if _, offset := start.t.Zone(); offset == 0 {
		// No declared zone and a bare Z offset is an absence of evidence,
		// not evidence of UTC: companions predating the time_zone field
		// forced every event to UTC no matter where it was scheduled.
		// Annotating those as UTC events would invent the very fact the
		// old wire format destroyed.
		return time.Time{}, time.Time{}, false
	}

	if !r.divergesFromHome(awayStart, awayEnd, endOK) {
		return time.Time{}, time.Time{}, false
	}
	return awayStart, awayEnd, true
}

// divergesFromHome reports whether the event's own clock reads differently
// from home at either end.
//
// The comparison is on the offsets in effect, not on the zone names:
// America/Chicago and America/Winnipeg keep the same clock, and annotating
// one with the other would add fields that say nothing a reader can act
// on. Both ends are checked because two zones can share an offset at the
// start and part before the end — America/Phoenix and America/Los_Angeles
// agree all summer and diverge at the fall-back — and a reading suppressed
// on the strength of the start alone would drop exactly the hour that made
// it worth emitting.
func (r calendarRenderer) divergesFromHome(awayStart, awayEnd time.Time, endOK bool) bool {
	if !sameOffset(awayStart, awayStart.In(r.home)) {
		return true
	}
	return endOK && !sameOffset(awayEnd, awayEnd.In(r.home))
}

// declaredLocation loads the zone the companion named, or nil when it named
// none or named one this host cannot resolve.
func declaredLocation(event companionCalendarEvent) *time.Location {
	name := strings.TrimSpace(event.TimeZone)
	if name == "" {
		return nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil
	}
	return loc
}

// allDayDate resolves the calendar date an all-day boundary names.
//
// The date form needs no interpretation. The instant form is what older
// companions send: some zone's midnight, with the zone itself discarded
// before it reached the wire, which is the bug this contract replaces.
// Rounding to the nearest UTC midnight recovers the intended date for
// every offset inside ±12h — the Chicago reading 05:00Z and the Berlin
// reading 22:00Z of the previous day both round to the same date — which
// covers every zone this household operates in. Offsets beyond ±12h
// (Kiribati, Baker Island) would land a day out; they are unreachable
// here, and a companion new enough to visit one sends the date form.
func (r calendarRenderer) allDayDate(b calendarBoundary) time.Time {
	if b.dateOnly {
		return b.t
	}
	rounded := b.t.UTC().Round(24 * time.Hour)
	return time.Date(rounded.Year(), rounded.Month(), rounded.Day(), 0, 0, 0, 0, time.UTC)
}

// allDayLastDate resolves the inclusive final day of an all-day event.
//
// The date form is already inclusive: the companion computed it against
// the event's own calendar, which is the only place the answer is
// knowable. The instant form is exclusive in EventKit's model, so the day
// before it is the last one the event actually occupies.
func (r calendarRenderer) allDayLastDate(end calendarBoundary, first time.Time) time.Time {
	if end.dateOnly {
		if end.t.Before(first) {
			return first
		}
		return end.t
	}
	last := r.allDayDate(end).AddDate(0, 0, -1)
	if last.Before(first) {
		return first
	}
	return last
}

// sameOffset reports whether two times sit at the same UTC offset. Two
// readings that do are the same wall clock, whatever their zones are named.
func sameOffset(a, b time.Time) bool {
	_, ao := a.Zone()
	_, bo := b.Zone()
	return ao == bo
}
