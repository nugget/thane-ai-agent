package database

import "strings"

// Placeholders renders n comma-separated SQL parameter placeholders:
// Placeholders(3) returns "?,?,?".
//
// It returns the empty string for n <= 0. Callers must treat that as
// "no query to run" rather than interpolating it, because `IN ()` is
// not valid SQL — which is the case every hand-rolled version of this
// had to remember separately.
func Placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

// InList renders both halves of a SQL IN clause: the placeholder string
// and the []any argument slice database/sql wants.
//
//	clause, args := database.InList(ids)
//	if clause == "" {
//	    return nil, nil
//	}
//	rows, err := db.Query(`SELECT ... WHERE id IN (`+clause+`)`, args...)
//
// The conversion is the reason this returns two values. Every call site
// that built placeholders by hand also wrote the same []T to []any loop
// immediately afterwards, so splitting them just moves the duplication.
//
// Returns "" and nil for an empty slice, matching [Placeholders].
func InList[T any](values []T) (string, []any) {
	if len(values) == 0 {
		return "", nil
	}
	args := make([]any, len(values))
	for i, v := range values {
		args[i] = v
	}
	return Placeholders(len(values)), args
}
