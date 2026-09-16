package toolargs

import (
	"sort"
	"strings"
)

// RejectedArgumentsError marks a tool error as a refusal of specific
// arguments: the values the call sent under Fields are what the tool
// refused, so resending them unchanged is refused the same way. It is
// how a caller learns which arguments an error is about without parsing
// the prose, which cannot tell a validation error that names an argument
// from a store failure whose text happens to contain the same word.
//
// Fields are top-level argument keys exactly as the tool receives them.
// A tool whose values arrive nested inside another argument, or parsed
// out of a body, marks nothing for them. Errors that are not about the
// values at all — store, IO, lookup, lock, or network failures — are
// never marked, whatever their text mentions.
type RejectedArgumentsError struct {
	// Fields names the refused top-level arguments.
	Fields []string
	// Err is the refusal itself, unchanged; its text is the error's.
	Err error
}

// Error returns the wrapped refusal's text, so marking an error never
// changes what a reader sees.
func (e *RejectedArgumentsError) Error() string {
	if e.Err == nil {
		return "rejected arguments: " + strings.Join(e.Fields, ", ")
	}
	return e.Err.Error()
}

// Unwrap returns the wrapped refusal.
func (e *RejectedArgumentsError) Unwrap() error {
	return e.Err
}

// Rejected marks err as a refusal of the named top-level arguments. A nil
// err yields nil, and no fields yields err unchanged, so a call site can
// mark unconditionally.
func Rejected(err error, fields ...string) error {
	if err == nil || len(fields) == 0 {
		return err
	}
	return &RejectedArgumentsError{Fields: append([]string(nil), fields...), Err: err}
}

// RejectedArguments returns the sorted, de-duplicated argument keys that
// err marks as refused, gathered from every [RejectedArgumentsError] in
// its tree. It follows both single wrapping (fmt.Errorf with one %w) and
// multiple wrapping (errors.Join, fmt.Errorf with several %w), so the
// marks on every violation of a joined multi-field refusal are found —
// errors.As would stop at the first. A nil error, or one with no marks,
// yields nil.
func RejectedArguments(err error) []string {
	seen := make(map[string]struct{})
	collectRejected(err, seen)
	if len(seen) == 0 {
		return nil
	}
	fields := make([]string, 0, len(seen))
	for field := range seen {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	return fields
}

// collectRejected adds the fields of every mark in err's tree to seen.
func collectRejected(err error, seen map[string]struct{}) {
	for err != nil {
		if marked, ok := err.(*RejectedArgumentsError); ok {
			for _, field := range marked.Fields {
				if field != "" {
					seen[field] = struct{}{}
				}
			}
		}
		switch wrapper := err.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range wrapper.Unwrap() {
				collectRejected(child, seen)
			}
			return
		case interface{ Unwrap() error }:
			err = wrapper.Unwrap()
		default:
			return
		}
	}
}
