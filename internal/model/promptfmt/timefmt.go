package promptfmt

import (
	"fmt"
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
		total += time.Duration(n) * unit
		body = body[i+1:]
	}
	return total, nil
}
