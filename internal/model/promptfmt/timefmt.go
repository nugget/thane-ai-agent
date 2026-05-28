package promptfmt

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FormatDelta returns a delta-annotated timestamp string relative to now.
// Past timestamps produce just the delta: "(-3247s)".
// Future timestamps keep the absolute time and annotate: "2026-03-07T18:00-06:00 (+52143s)".
func FormatDelta(t time.Time, now time.Time) string {
	secs := int64(t.Sub(now).Truncate(time.Second) / time.Second)

	if secs <= 0 {
		return fmt.Sprintf("(-%ds)", -secs)
	}
	return fmt.Sprintf("%s (+%ds)", t.Format(time.RFC3339), secs)
}

// FormatDeltaOnly returns just the signed delta string: "-3247s" or "+3600s".
func FormatDeltaOnly(t time.Time, now time.Time) string {
	secs := int64(t.Sub(now).Truncate(time.Second) / time.Second)

	if secs <= 0 {
		return fmt.Sprintf("-%ds", -secs)
	}
	return fmt.Sprintf("+%ds", secs)
}

// deltaUnits maps the single-character suffix of a signed offset to its
// duration. Seconds remain the canonical form [FormatDeltaOnly] emits;
// minutes/hours/days/weeks are accepted on input so model-facing tools
// can advertise human-scale windows ("-7d") instead of large second
// counts ("-604800s").
var deltaUnits = map[byte]time.Duration{
	's': time.Second,
	'm': time.Minute,
	'h': time.Hour,
	'd': 24 * time.Hour,
	'w': 7 * 24 * time.Hour,
}

// ParseTimeOrDelta parses either an absolute RFC3339 timestamp or a signed
// offset relative to now. Offsets are "<sign><integer><unit>" where unit is
// s (seconds), m (minutes), h (hours), d (days), or w (weeks) — e.g.
// "+3600s", "-300s", "-30m", "-24h", "-7d". Any tool parameter that accepts
// a timestamp should use this for backwards-compatible offset support.
func ParseTimeOrDelta(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}

	// A leading sign unambiguously marks a signed offset (RFC3339 never
	// starts with + or -), so parse the unit-suffixed delta form here.
	if s[0] == '+' || s[0] == '-' {
		if len(s) < 3 {
			return time.Time{}, fmt.Errorf("invalid offset %q: want <sign><number><unit> (s, m, h, d, or w)", s)
		}
		unit, ok := deltaUnits[s[len(s)-1]]
		if !ok {
			return time.Time{}, fmt.Errorf("invalid offset %q: unit must be s, m, h, d, or w", s)
		}
		n, err := strconv.ParseInt(s[1:len(s)-1], 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid offset %q: %w", s, err)
		}
		d := time.Duration(n) * unit
		if s[0] == '-' {
			return now.Add(-d), nil
		}
		return now.Add(d), nil
	}

	// Fall back to RFC3339 absolute timestamp.
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp %q: %w", s, err)
	}
	return t, nil
}
