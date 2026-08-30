package promptfmt

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// FormatDelta returns a delta-annotated timestamp string relative to now.
// Past timestamps produce just the delta: "(-3247s)", "(-26h45m)",
// "(-5d9h)". Future timestamps keep the absolute time and annotate:
// "2026-03-07T18:00-06:00 (+14h29m)".
func FormatDelta(t time.Time, now time.Time) string {
	secs := int64(t.Sub(now).Truncate(time.Second) / time.Second)

	if secs <= 0 {
		return fmt.Sprintf("(-%s)", formatDeltaMagnitude(-secs))
	}
	return fmt.Sprintf("%s (+%s)", t.Format(time.RFC3339), formatDeltaMagnitude(secs))
}

// FormatDeltaOnly returns just the signed delta string: "-3247s",
// "-26h45m", "+5d9h".
//
// Magnitudes under an hour stay in exact seconds; from one hour the
// shape switches to hours+minutes, and from two days to days+hours.
// Models must not be made to divide second counts into human scales
// (docs/model-facing-context.md), and every emitted form round-trips
// through [ParseTimeOrDelta], so tool arguments can echo these values
// back verbatim.
func FormatDeltaOnly(t time.Time, now time.Time) string {
	secs := int64(t.Sub(now).Truncate(time.Second) / time.Second)

	if secs <= 0 {
		return "-" + formatDeltaMagnitude(-secs)
	}
	return "+" + formatDeltaMagnitude(secs)
}

// formatDeltaMagnitude renders an absolute second count in the tiered
// unit shape shared by FormatDelta and FormatDeltaOnly: exact seconds
// under an hour, hours+minutes under two days, then days+hours. At
// most two units appear and zero-valued trailing units are dropped, so
// output stays compact and unambiguous ("26h", "5d9h"). Sub-unit
// remainders below the second term are deliberately dropped — at those
// magnitudes they carry no reasoning value for a model.
func formatDeltaMagnitude(secs int64) string {
	switch {
	case secs < 3600:
		return strconv.FormatInt(secs, 10) + "s"
	case secs < 48*3600:
		h, m := secs/3600, (secs%3600)/60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		d, h := secs/86400, (secs%86400)/3600
		if h == 0 {
			return fmt.Sprintf("%dd", d)
		}
		return fmt.Sprintf("%dd%dh", d, h)
	}
}

// FormatDuration renders a duration as a compact Go duration literal:
// "30s", "15m", "1h", "1h30m", "24h". Zero-valued terms are dropped, so
// nothing reads "1h0m0s" the way [time.Duration.String] would.
//
// Unlike [FormatDeltaOnly] this is for configured intervals rather than
// offsets from now, so it carries no sign and never reaches for a day
// term: every value it emits parses back through [time.ParseDuration],
// which is what tool parameters that take a duration accept. A duration
// the model reads here can be passed straight back verbatim.
//
// A magnitude below one second falls through to [time.Duration.String],
// which already renders those compactly ("400ms") and stays parseable.
// Rounding them to "0s" instead would report a real interval as no
// interval at all — and the values this formats are bounds a caller is
// meant to choose within, so a zero there is worse than untidy.
func FormatDuration(d time.Duration) string {
	if d != 0 && d > -time.Second && d < time.Second {
		return d.String()
	}
	d = d.Truncate(time.Second)
	var b strings.Builder
	if d < 0 {
		b.WriteByte('-')
		d = -d
	}
	h, m, s := d/time.Hour, (d%time.Hour)/time.Minute, (d%time.Minute)/time.Second
	if h > 0 {
		fmt.Fprintf(&b, "%dh", h)
	}
	if m > 0 {
		fmt.Fprintf(&b, "%dm", m)
	}
	// The seconds term is unconditional only when nothing else was
	// written, so a whole-minute duration stays "15m" while a zero one
	// still renders a parseable "0s" instead of the empty string.
	if s > 0 || (h == 0 && m == 0) {
		fmt.Fprintf(&b, "%ds", s)
	}
	return b.String()
}

// deltaUnits maps the single-character suffix of a signed-offset term
// to its duration. All five units are accepted on input; output uses
// s, m, h, and d (see [formatDeltaMagnitude]).
var deltaUnits = map[byte]time.Duration{
	's': time.Second,
	'm': time.Minute,
	'h': time.Hour,
	'd': 24 * time.Hour,
	'w': 7 * 24 * time.Hour,
}

// ParseTimeOrDelta parses either an absolute RFC3339 timestamp or a signed
// offset relative to now. Offsets are "<sign><integer><unit>..." where unit
// is s (seconds), m (minutes), h (hours), d (days), or w (weeks), and
// multiple integer+unit terms compound under one leading sign — e.g.
// "+3600s", "-30m", "-24h", "-7d", "-5d9h", "-26h45m". Any tool parameter
// that accepts a timestamp should use this so it accepts every delta shape
// the prompt formatters emit.
func ParseTimeOrDelta(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}

	// A leading sign unambiguously marks a signed offset (RFC3339 never
	// starts with + or -), so parse the unit-suffixed delta form here.
	if s[0] == '+' || s[0] == '-' {
		total, err := parseDeltaTerms(s)
		if err != nil {
			return time.Time{}, err
		}
		if s[0] == '-' {
			return now.Add(-total), nil
		}
		return now.Add(total), nil
	}

	// Fall back to RFC3339 absolute timestamp.
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp %q: %w", s, err)
	}
	return t, nil
}

// parseDeltaTerms sums the integer+unit terms of a signed offset string
// ("-5d9h" → 5 days + 9 hours). The magnitude is returned unsigned; the
// caller applies the leading sign.
func parseDeltaTerms(s string) (time.Duration, error) {
	body := s[1:]
	if body == "" {
		return 0, fmt.Errorf("invalid offset %q: want <sign><number><unit>... (s, m, h, d, or w)", s)
	}
	var total time.Duration
	for len(body) > 0 {
		i := 0
		for i < len(body) && body[i] >= '0' && body[i] <= '9' {
			i++
		}
		if i == 0 {
			return 0, fmt.Errorf("invalid offset %q: want <sign><number><unit>... (s, m, h, d, or w)", s)
		}
		if i == len(body) {
			return 0, fmt.Errorf("invalid offset %q: term %q is missing its unit (s, m, h, d, or w)", s, body)
		}
		unit, ok := deltaUnits[body[i]]
		if !ok {
			return 0, fmt.Errorf("invalid offset %q: unit must be s, m, h, d, or w", s)
		}
		n, err := strconv.ParseInt(body[:i], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid offset %q: %w", s, err)
		}
		// Terms are model-supplied; reject magnitudes that would wrap
		// time.Duration instead of silently returning garbage.
		if n > int64(math.MaxInt64)/int64(unit) {
			return 0, fmt.Errorf("invalid offset %q: term %s%c is too large to represent", s, body[:i], body[i])
		}
		term := time.Duration(n) * unit
		if total > time.Duration(math.MaxInt64)-term {
			return 0, fmt.Errorf("invalid offset %q: terms sum past the representable range", s)
		}
		total += term
		body = body[i+1:]
	}
	return total, nil
}

// FormatDayDelta renders the distance between two calendar days as the
// word a reader would use, falling back to a signed day count past the
// range words have: "today", "tomorrow", "yesterday", "+5d", "-3d".
//
// Unlike [FormatDeltaOnly] this takes whole days rather than an instant
// offset, because its subject is a date rather than a moment. An all-day
// calendar event occupies a day, not a point on the clock; saying it
// starts in "+14h29m" invents a precision the source never had and reads
// as though there were a time to be early or late for.
//
// Both arguments are interpreted in their own locations, so callers must
// pass times already converted to the frame the reader thinks in.
func FormatDayDelta(day, today time.Time) string {
	d := daysBetween(today, day)
	switch d {
	case 0:
		return "today"
	case 1:
		return "tomorrow"
	case -1:
		return "yesterday"
	}
	if d > 0 {
		return fmt.Sprintf("+%dd", d)
	}
	return fmt.Sprintf("-%dd", -d)
}

// daysBetween counts whole calendar days from one date to another,
// ignoring both clock times and the locations the two carry. Truncating
// each to midnight UTC before subtracting keeps the count immune to the
// 23- and 25-hour days a DST transition produces: a plain Sub on two
// local midnights either side of a spring-forward reports 23h, which
// integer-divides to zero days and would render an event "today" on the
// morning it is actually tomorrow.
func daysBetween(from, to time.Time) int {
	a := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	b := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	return int(b.Sub(a) / (24 * time.Hour))
}
