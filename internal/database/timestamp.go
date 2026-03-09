package database

import (
	"fmt"
	"time"
)

// timestampLayouts lists accepted timestamp formats in order of
// preference.  SQLite has no native timestamp type — values are TEXT.
// Go code writes RFC3339 / RFC3339Nano, but manual SQL, migrations,
// and SQLite's own datetime() produce space-separated formats.
// Parsing tries each layout in order and returns the first match.
// Formats without an explicit timezone are treated as UTC.
var timestampLayouts = []string{
	time.RFC3339Nano,            // 2006-01-02T15:04:05.999999999Z07:00
	time.RFC3339,                // 2006-01-02T15:04:05Z07:00
	"2006-01-02T15:04:05",       // RFC3339 without timezone
	"2006-01-02 15:04:05Z07:00", // space-separated with timezone
	"2006-01-02 15:04:05",       // SQLite datetime() output
}

// ParseTimestamp parses a timestamp string from SQLite, accepting
// multiple common formats.  This provides read-side defense against
// format mismatches — the write side should continue using RFC3339.
func ParseTimestamp(s string) (time.Time, error) {
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp format: %q", s)
}
