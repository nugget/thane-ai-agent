package database

import (
	"fmt"
	"strings"
	"time"
)

// SQLiteTimestampLayout is the canonical TEXT shape go-sqlite3 emits
// when binding a [time.Time] value: a space-separated date and time
// with nanosecond precision and an explicit zone offset. This is the
// on-disk form for any column whose values were inserted via bound
// parameters (the production write path), and it must be used as-is
// when building bounds for lexical comparisons against those columns.
//
// Mixing this with [time.RFC3339Nano] (which uses a T separator)
// produces silent miscompares: lexically " " (0x20) < "T" (0x54), so
// for the same instant the space-form is "less than" the T-form, and
// a `WHERE timestamp >= ?` clause silently excludes the lower edge of
// the window when the bound and the stored row disagree on shape.
const SQLiteTimestampLayout = "2006-01-02 15:04:05.999999999-07:00"

// timestampLayouts lists accepted timestamp formats in order of
// preference.  SQLite has no native timestamp type — values are TEXT.
// go-sqlite3 writes [SQLiteTimestampLayout] when binding time.Time;
// other Go code historically wrote RFC3339 / RFC3339Nano via Format;
// SQLite's own datetime() emits a space-separated form without zone.
// Parsing tries each layout in order and returns the first match.
// Formats without an explicit timezone are treated as UTC.
var timestampLayouts = []string{
	SQLiteTimestampLayout,       // go-sqlite3 native binding output
	time.RFC3339Nano,            // 2006-01-02T15:04:05.999999999Z07:00
	time.RFC3339,                // 2006-01-02T15:04:05Z07:00
	"2006-01-02T15:04:05",       // RFC3339 without timezone
	"2006-01-02 15:04:05Z07:00", // space-separated with timezone
	"2006-01-02 15:04:05",       // SQLite datetime() output
}

// ParseTimestamp parses a timestamp string from SQLite, accepting
// multiple common formats. This provides read-side defense against
// format mismatches — the write side should bind [time.Time] directly
// (driver-native [SQLiteTimestampLayout]) or use [FormatTimestamp]
// when a literal string is required.
func ParseTimestamp(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized timestamp format: %q", s)
}

// FormatTimestamp returns t in [SQLiteTimestampLayout] — the form
// go-sqlite3 emits when binding [time.Time] directly. Use this only
// when you need a literal string (raw SQL composition, migrations,
// log lines, structured-data emission). For normal parameterized
// queries, BIND time.Time directly so the driver and any stored rows
// share the same on-disk shape — that is the canonical write path
// and avoids the format-mixing trap described on [SQLiteTimestampLayout].
func FormatTimestamp(t time.Time) string {
	return t.Format(SQLiteTimestampLayout)
}
