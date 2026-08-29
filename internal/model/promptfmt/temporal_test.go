package promptfmt

import (
	"testing"
	"time"
)

// temporalTestNow is a fixed household-zone instant late enough in the
// local evening that the UTC date has already rolled to the next day
// (2026-08-29T22:00-05:00 is 2026-08-30T03:00Z). Cases that pivot on
// "today" vs "tomorrow" only prove the zone contract because of that
// straddle.
var temporalTestNow = time.Date(2026, 8, 29, 22, 0, 0, 0, time.FixedZone("CDT", -5*3600))

func TestExpandTemporalTemplates(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bare date today",
			in:   "{{delta:2026-08-29}}",
			want: "today",
		},
		{
			// The UTC date is already Aug 30 at temporalTestNow; only a
			// household-zone now keeps this rendering "tomorrow".
			name: "bare date tomorrow across the UTC midnight straddle",
			in:   "{{delta:2026-08-30}}",
			want: "tomorrow",
		},
		{
			name: "bare date yesterday",
			in:   "{{delta:2026-08-28}}",
			want: "yesterday",
		},
		{
			name: "bare date future day count",
			in:   "{{delta:2026-09-18}}",
			want: "+20d",
		},
		{
			name: "bare date past day count",
			in:   "{{delta:2026-08-26}}",
			want: "-3d",
		},
		{
			name: "rfc3339 future instant",
			in:   "{{delta:2026-09-02T14:00:00-05:00}}",
			want: "+3d16h",
		},
		{
			name: "rfc3339 past instant stays exact seconds",
			in:   "{{delta:2026-08-29T21:30:00-05:00}}",
			want: "-1800s",
		},
		{
			name: "rfc3339 zulu instant equal to now",
			in:   "{{delta:2026-08-30T03:00:00Z}}",
			want: "-0s",
		},
		{
			name: "multiple templates with adjacent text byte-exact",
			in:   "Trip window Sep 18–30 ({{delta:2026-09-18}}, ends {{delta:2026-09-30}}).",
			want: "Trip window Sep 18–30 (+20d, ends +32d).",
		},
		{
			name: "template at start of string",
			in:   "{{delta:2026-08-30}} the window opens",
			want: "tomorrow the window opens",
		},
		{
			name: "template at end of string",
			in:   "the window closed {{delta:2026-08-28}}",
			want: "the window closed yesterday",
		},
		{
			name: "template is the whole string plus trailing braces",
			in:   "{{delta:2026-08-29}}}}",
			want: "today}}",
		},
		{
			name: "malformed value renders verbatim",
			in:   "departs {{delta:not-a-date}} sharp",
			want: "departs {{delta:not-a-date}} sharp",
		},
		{
			name: "out of range date renders verbatim",
			in:   "{{delta:2026-02-31}}",
			want: "{{delta:2026-02-31}}",
		},
		{
			name: "unknown tag renders verbatim",
			in:   "{{until:2026-09-18}}",
			want: "{{until:2026-09-18}}",
		},
		{
			name: "empty value renders verbatim",
			in:   "{{delta:}}",
			want: "{{delta:}}",
		},
		{
			name: "empty braces render verbatim",
			in:   "a {{}} b",
			want: "a {{}} b",
		},
		{
			name: "unterminated open renders verbatim",
			in:   "text {{delta:2026-09-18",
			want: "text {{delta:2026-09-18",
		},
		{
			name: "bare open braces render verbatim",
			in:   "a {{ b",
			want: "a {{ b",
		},
		{
			// The stray "{{" is emitted literally and scanning resumes
			// just past it, so it cannot swallow the valid template
			// that follows — malformed noise stays visible without
			// disabling substitution downstream of it.
			name: "stray open braces do not swallow a following template",
			in:   "{{ x {{delta:2026-09-18}}",
			want: "{{ x +20d",
		},
		{
			// The outer token's value is not a date, so its bytes pass
			// through untouched; the inner well-formed template still
			// renders, per the same resume-past-the-braces rule.
			name: "nested template expands inner only",
			in:   "{{delta:{{delta:2026-09-18}}}}",
			want: "{{delta:+20d}}",
		},
		{
			name: "whitespace inside template renders verbatim",
			in:   "{{delta: 2026-09-18}}",
			want: "{{delta: 2026-09-18}}",
		},
		{
			name: "no templates returns input unchanged",
			in:   "plain prose, no braces at all",
			want: "plain prose, no braces at all",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExpandTemporalTemplates(tt.in, temporalTestNow)
			if got != tt.want {
				t.Errorf("ExpandTemporalTemplates(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestExpandTemporalTemplates_FastPathDoesNotAllocate pins the
// contract that template-free prose — the overwhelmingly common case
// on the per-turn injection path — costs nothing: the input string is
// returned as-is.
func TestExpandTemporalTemplates_FastPathDoesNotAllocate(t *testing.T) {
	in := "a perfectly ordinary article body with no templates in it"
	allocs := testing.AllocsPerRun(100, func() {
		if got := ExpandTemporalTemplates(in, temporalTestNow); got != in {
			t.Fatalf("fast path changed the string: %q", got)
		}
	})
	if allocs != 0 {
		t.Errorf("template-free expansion allocated %.1f times per run, want 0", allocs)
	}
}
