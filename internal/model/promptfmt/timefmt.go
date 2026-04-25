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

// ParseTimeOrDelta parses either an absolute RFC3339 timestamp or a signed
// offset ("+3600s", "-300s") relative to now. Any tool parameter that
// accepts a timestamp should use this for backwards-compatible offset
// support.
func ParseTimeOrDelta(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}

	// Check for signed offset format: +Ns or -Ns
	if len(s) >= 3 && (s[0] == '+' || s[0] == '-') && s[len(s)-1] == 's' {
		secsStr := s[1 : len(s)-1]
		secs, err := strconv.ParseInt(secsStr, 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid offset %q: %w", s, err)
		}
		d := time.Duration(secs) * time.Second
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
