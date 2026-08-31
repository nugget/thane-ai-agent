package logging

import "time"

// TimestampLayout is the fixed-width UTC shape for every TEXT timestamp
// this package writes and every literal bound it compares against them
// (log_entries, retained request/tool content, live requests).
//
// The columns are TEXT and every window clause is a lexicographic
// compare, so ordering correctness is a property of the string shape.
// The previous shape, time.RFC3339Nano, trims trailing fractional
// zeros — and trimmed fractions do not sort: ".1Z" > ".15Z" because
// 'Z' (0x5A) outranks every digit, so a row stamped at .100s fell
// outside a window ending at .150s. That inversion made
// TestQuery_ExtendedLogEntry flake and silently clips real query
// windows at sub-second edges. A zero-padded 9-digit fraction with a
// forced-UTC zone is constant-width, so lexicographic order equals
// time order.
//
// This is deliberately not [database.SQLiteTimestampLayout]: that is
// the space-separated shape the driver emits when binding time.Time,
// and these columns have always been the T-separated island written
// via explicit Format calls. Mixing separators is its own documented
// trap (space sorts before 'T'); staying T-form keeps new rows and
// bounds ordering correctly against the years of existing rows, whose
// only residual fuzz is the sub-second edge within their own trimmed
// fractions — and those rows age out with retention.
//
// Always format through [FormatTimestamp]: the fixed width holds only
// in UTC, where the zone renders as the single byte "Z".
const TimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

// FormatTimestamp returns t in [TimestampLayout], normalized to UTC so
// the width — and therefore lexicographic ordering — is guaranteed.
func FormatTimestamp(t time.Time) string {
	return t.UTC().Format(TimestampLayout)
}
