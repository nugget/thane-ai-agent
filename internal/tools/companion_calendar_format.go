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
const (
	calendarDateLayout = "2006-01-02"

	// Weekday-and-date shapes. The year appears only when the event does
	// not fall in the reader's current year: on a calendar the near term
	// is the common case, and "2026" on every line of a week's schedule
	// is noise that pushes the parts that vary off to the right.
	calendarDayLayout     = "Mon Jan 2"
	calendarDayYearLayout = "Mon Jan 2 2006"

	// Clock shapes. Seconds never appear — no calendar event means them,
	// and rendering ":00" on every line invites the model to treat the
	// precision as real.
	calendarClockLayout     = "3:04PM MST"
	calendarClockOnlyLayout = "3:04PM"
)

// calendarRenderer turns companion calendar events into the lines a model
// reads. It resolves two frames for every event:
//
//   - home, the household zone from configuration, which is the frame the
//     agent's own process reasons in and the one "is this soon?" is asked
//     against;
//   - the event's own zone, which on this operator's calendar is a
//     statement of intent — an event recorded in Europe/Berlin means the
//     operator expects to be in Berlin for it, so that reading is the
//     wall clock they will actually live.
//
// Home is primary and every event is rendered in it. The event's own
// reading is appended whenever the two disagree about what the clock says,
// so neither frame has to be inferred.
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
	fmt.Fprintf(&b, "Found %d macOS calendar events (times in %s; now %s):",
		len(response.Events), r.homeZoneName(), r.now.Format(calendarDayLayout+" "+calendarClockLayout))
	for _, event := range response.Events {
		fmt.Fprintf(&b, "\n- %s | %s", r.formatRange(event), strings.TrimSpace(event.Title))
		if event.Calendar != "" {
			fmt.Fprintf(&b, " (%s)", event.Calendar)
		}
		if event.Location != "" {
			fmt.Fprintf(&b, "\n  Location: %s", event.Location)
		}
		if event.NotesExcerpt != "" {
			fmt.Fprintf(&b, "\n  Notes: %s", event.NotesExcerpt)
		}
		if event.URL != "" {
			fmt.Fprintf(&b, "\n  URL: %s", event.URL)
		}
	}
	formatted := b.String()
	if len(formatted) <= maxCompanionCalendarResultBytes {
		return formatted
	}
	const note = "\n\n[... output truncated; narrow the window, filters, or limit for more ...]"
	allowed := maxCompanionCalendarResultBytes - len(note)
	if allowed < 0 {
		allowed = 0
	}
	return truncateUTF8(formatted, allowed) + note
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

// formatRange renders the time portion of one event line.
func (r calendarRenderer) formatRange(event companionCalendarEvent) string {
	start, startOK := parseCalendarBoundary(event.Start)
	end, endOK := parseCalendarBoundary(event.End)

	// An unparseable start leaves nothing to reason from. Echo whatever the
	// companion sent rather than dropping the event: a malformed timestamp
	// is a bug worth seeing, and the title and location are still useful.
	if !startOK {
		if strings.TrimSpace(event.End) == "" {
			return strings.TrimSpace(event.Start)
		}
		return strings.TrimSpace(event.Start) + " - " + strings.TrimSpace(event.End)
	}

	if event.AllDay {
		return r.formatAllDay(start, end, endOK)
	}
	return r.formatTimed(event, start, end, endOK)
}

// formatAllDay renders a date range with no clock and no zone. An all-day
// event is a property of a date in the place the operator will be, not an
// interval on any particular clock, so imposing one would be a fiction.
func (r calendarRenderer) formatAllDay(start, end calendarBoundary, endOK bool) string {
	first := r.allDayDate(start)
	delta := promptfmt.FormatDayDelta(first, r.now)

	last := first
	if endOK {
		last = r.allDayLastDate(end, first)
	}

	if sameDay(first, last) {
		return fmt.Sprintf("%s (all day, %s)", r.formatDay(first), delta)
	}
	return fmt.Sprintf("%s -> %s (all day, %s)", r.formatDay(first), r.formatDay(last), delta)
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

// formatTimed renders an event that occupies a span of clock time, in home
// with the event's own reading appended when the two clocks disagree.
func (r calendarRenderer) formatTimed(event companionCalendarEvent, start, end calendarBoundary, endOK bool) string {
	homeStart := start.t.In(r.home)
	line := fmt.Sprintf("%s (%s)",
		r.formatSpan(homeStart, end.t.In(r.home), endOK, spanStyle{withDate: true, withZone: true}),
		promptfmt.FormatDeltaOnly(start.t, r.now))

	if away, ok := r.awayReading(event, start, end, endOK, homeStart); ok {
		return line + " [" + away + "]"
	}
	return line
}

// awayReading renders the event on its own clock, and reports whether that
// reading says anything the home reading did not.
func (r calendarRenderer) awayReading(event companionCalendarEvent, start, end calendarBoundary, endOK bool, homeStart time.Time) (string, bool) {
	declared := declaredLocation(event)

	// Resolve each boundary in the frame that actually governs it. With a
	// declared zone that is the zone itself, transitions included. Without
	// one, each timestamp's own offset is the only evidence there is, and
	// the two ends can legitimately disagree across a transition the event
	// spans — forcing the end into the start's offset would move it.
	awayStart, awayEnd := start.t, end.t
	if declared != nil {
		awayStart, awayEnd = start.t.In(declared), end.t.In(declared)
	} else if _, offset := start.t.Zone(); offset == 0 {
		// No declared zone and a bare Z offset is an absence of evidence,
		// not evidence of UTC: companions predating the time_zone field
		// forced every event to UTC no matter where it was scheduled.
		// Annotating those as UTC events would invent the very fact the
		// old wire format destroyed.
		return "", false
	}

	if !r.divergesFromHome(awayStart, awayEnd, endOK) {
		return "", false
	}

	// Label the frame once where a single one governs the whole span. When
	// the ends sit at different offsets there is no one frame to name, so
	// each end carries its own and the label is dropped rather than
	// claiming the start's offset covers both.
	zonesDiffer := endOK && !sameOffset(awayStart, awayEnd)
	span := r.formatSpan(awayStart, awayEnd, endOK, spanStyle{
		// The away reading repeats the date only when it is a different
		// one. A 9pm Berlin event is 2pm the same afternoon in Chicago and
		// the date would be noise; an 11pm Chicago event is the following
		// morning in Berlin, and there the date is the whole point.
		withDate: !sameDay(homeStart, awayStart),
		withZone: zonesDiffer,
	})

	if zonesDiffer && declared == nil {
		return span, true
	}
	return r.eventZoneName(event, awayStart) + " " + span, true
}

// divergesFromHome reports whether the event's own clock reads differently
// from home at either end.
//
// The comparison is on the offsets in effect, not on the zone names:
// America/Chicago and America/Winnipeg keep the same clock, and annotating
// one with the other would add a line of text that says nothing a reader
// can act on. Both ends are checked because two zones can share an offset
// at the start and part before the end — America/Phoenix and
// America/Los_Angeles agree all summer and diverge at the fall-back — and
// an annotation suppressed on the strength of the start alone would drop
// exactly the hour that made it worth printing.
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

// eventZoneName labels the event's own frame, preferring the IANA name the
// companion declared. A timestamp offset alone has no name to report, so
// fall back to the zone abbreviation, and then to the numeric offset when
// even that is only a "+02" placeholder.
func (r calendarRenderer) eventZoneName(event companionCalendarEvent, at time.Time) string {
	if declaredLocation(event) != nil {
		return strings.TrimSpace(event.TimeZone)
	}
	abbrev, offset := at.Zone()
	if abbrev != "" && !strings.HasPrefix(abbrev, "+") && !strings.HasPrefix(abbrev, "-") {
		return abbrev
	}
	return formatZoneOffset(offset)
}

// sameOffset reports whether two times sit at the same UTC offset. Two
// readings that do are the same wall clock, whatever their zones are named.
func sameOffset(a, b time.Time) bool {
	_, ao := a.Zone()
	_, bo := b.Zone()
	return ao == bo
}

// spanStyle selects which parts of a span are worth printing. The home
// reading carries both the date and the zone; an away reading is already
// introduced by its zone name and sits beside the home date, so it usually
// needs neither.
type spanStyle struct {
	withDate bool
	withZone bool
}

// formatSpan renders a start and end already converted to one location. A
// span that stays inside a single day states the date once and drops it
// from the end; one that crosses a day boundary states both regardless of
// style, because a reader cannot supply the second date themselves. A
// zero-length or unparseable end drops the end entirely.
func (r calendarRenderer) formatSpan(start, end time.Time, endOK bool, style spanStyle) string {
	clock := calendarClockOnlyLayout
	if style.withZone {
		clock = calendarClockLayout
	}
	day := func(t time.Time) string {
		if !style.withDate {
			return ""
		}
		return r.formatDay(t) + " "
	}

	if !endOK || !end.After(start) {
		return day(start) + start.Format(clock)
	}
	if sameDay(start, end) {
		// Fall back repeats an hour: 1:30AM CDT and 1:30AM CST are a real
		// hour apart on the same date. Carrying the zone on the end alone
		// would label the start with the end's zone and render that hour
		// as a zero-length event.
		if style.withZone && !sameOffset(start, end) {
			return day(start) + start.Format(clock) + "-" + end.Format(clock)
		}
		return day(start) + start.Format(calendarClockOnlyLayout) + "-" + end.Format(clock)
	}
	return fmt.Sprintf("%s %s -> %s %s",
		r.formatDay(start), start.Format(clock),
		r.formatDay(end), end.Format(clock))
}

// formatDay renders a weekday and date, carrying the year only when the
// event falls outside the reader's current one.
func (r calendarRenderer) formatDay(t time.Time) string {
	if t.Year() == r.now.Year() {
		return t.Format(calendarDayLayout)
	}
	return t.Format(calendarDayYearLayout)
}

// formatZoneOffset renders a UTC offset as "UTC+2" or "UTC-5:30", the last
// resort when a zone has neither an IANA name nor a usable abbreviation.
func formatZoneOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	h, m := seconds/3600, (seconds%3600)/60
	if m == 0 {
		return fmt.Sprintf("UTC%s%d", sign, h)
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, h, m)
}

// sameDay reports whether two times fall on the same calendar day. Callers
// pass times already in one location; comparing across locations would ask
// a question with no answer.
func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}
