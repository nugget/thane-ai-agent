package toolargs

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// TestRejectedArguments pins the read side of the marks: every mark in the
// tree counts, however the refusal was wrapped or joined, and an error
// without marks names no argument whatever its text says.
func TestRejectedArguments(t *testing.T) {
	t.Parallel()

	statusLine := Rejected(errors.New("status_line is 190 characters and the limit is 120"), "status_line")
	digest := Rejected(errors.New("digest is required"), "digest")
	teaser := Rejected(errors.New("teaser must be a single line"), "teaser")
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{name: "nil error", err: nil},
		{
			// The text names the argument, but nothing marked it: a
			// lookup that failed is not a refusal of the name it looked up.
			name: "unmarked error that mentions an argument",
			err:  errors.New(`resolve contact: find by name or nickname "Alice": sql: database is closed`),
		},
		{name: "one mark", err: digest, want: []string{"digest"}},
		{name: "one mark naming two fields", err: Rejected(errors.New("repeat the name"), "teaser", "status_line"), want: []string{"status_line", "teaser"}},
		{name: "wrapped with %w", err: fmt.Errorf("publish: %w", digest), want: []string{"digest"}},
		{name: "wrapped twice", err: fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", digest)), want: []string{"digest"}},
		{
			name: "joined violations give the union",
			err:  errors.Join(statusLine, digest, teaser),
			want: []string{"digest", "status_line", "teaser"},
		},
		{
			// The shape every structured writer returns: a frame wrapping
			// a join of per-field violations, one of them plain.
			name: "frame around a join with an unmarked sibling",
			err:  fmt.Errorf("projections are invalid: %w", errors.Join(statusLine, errors.New("the title is Go-derived"), digest)),
			want: []string{"digest", "status_line"},
		},
		{
			name: "join nested inside a join",
			err:  errors.Join(fmt.Errorf("contract: %w", errors.Join(statusLine, digest)), fmt.Errorf("identity: %w", teaser)),
			want: []string{"digest", "status_line", "teaser"},
		},
		{name: "several %w verbs in one frame", err: fmt.Errorf("%w; %w", statusLine, teaser), want: []string{"status_line", "teaser"}},
		{name: "a field marked twice is listed once", err: errors.Join(digest, fmt.Errorf("again: %w", digest)), want: []string{"digest"}},
		{name: "a mark nested inside another mark", err: Rejected(fmt.Errorf("outer: %w", digest), "full"), want: []string{"digest", "full"}},
		{name: "empty field names are dropped", err: &RejectedArgumentsError{Fields: []string{"", "full"}, Err: errors.New("full is required")}, want: []string{"full"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := RejectedArguments(tt.err); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RejectedArguments() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestRejectedLeavesTheErrorAsItWas pins that marking changes nothing a
// reader of the error sees: the text is the refusal's, the refusal stays
// reachable through errors.Is, and the degenerate calls mark nothing.
func TestRejectedLeavesTheErrorAsItWas(t *testing.T) {
	t.Parallel()

	refusal := errors.New("digest is required")
	marked := Rejected(refusal, "digest")
	if marked.Error() != refusal.Error() {
		t.Errorf("marked text = %q, want %q", marked.Error(), refusal.Error())
	}
	if !errors.Is(marked, refusal) {
		t.Error("marked error does not wrap the refusal")
	}
	var typed *RejectedArgumentsError
	if !errors.As(marked, &typed) || !reflect.DeepEqual(typed.Fields, []string{"digest"}) {
		t.Errorf("errors.As found %#v, want a mark naming digest", typed)
	}
	if got := Rejected(nil, "digest"); got != nil {
		t.Errorf("Rejected(nil) = %v, want nil", got)
	}
	if got := Rejected(refusal); got != refusal {
		t.Errorf("Rejected with no fields = %#v, want the error unchanged", got)
	}

	fields := []string{"digest"}
	aliased := Rejected(refusal, fields...)
	fields[0] = "teaser"
	if got := RejectedArguments(aliased); !reflect.DeepEqual(got, []string{"digest"}) {
		t.Errorf("mark follows the caller's slice after construction: %v", got)
	}
	if got := (&RejectedArgumentsError{Fields: []string{"full"}}).Error(); got != "rejected arguments: full" {
		t.Errorf("zero-Err text = %q", got)
	}
}
