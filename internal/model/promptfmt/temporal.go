package promptfmt

import (
	"strings"
	"time"
)

// Temporal template delimiters and the one tag the v1 vocabulary
// defines. The vocabulary is deliberately closed — see
// [ExpandTemporalTemplates].
const (
	temporalOpen  = "{{"
	temporalClose = "}}"
	temporalDelta = "delta:"
)

// ExpandTemporalTemplates renders the temporal-substitution vocabulary
// in curated prose: each {{delta:VALUE}} template becomes the
// now-relative form of VALUE, computed with this package's own delta
// formatters. VALUE is either a bare date (2026-09-18) or an RFC3339
// timestamp — that is the entire v1 vocabulary. Sugar forms like
// {{until:...}}/{{ago:...}} and references to entities or events are
// deliberately parked: a date literal cannot dangle, while the general
// data-reference form waits on a stable-identity substrate (issue
// #1431 records both decisions).
//
// A bare date names a day, not a moment, so it renders through
// [FormatDayDelta] ("today", "tomorrow", "+20d"); an RFC3339 instant
// renders through [FormatDeltaOnly] ("+3d16h"). Day distance is a
// calendar comparison, so callers must pass now already in the zone
// the reader thinks in — the household timezone — or a late evening
// there will render tomorrow's date as "today" the moment UTC rolls
// over.
//
// Templates render values, never claims. Prose whose truth changes
// with data is a wake concern, not a substitution concern:
// substitution keeps true prose fresh, while waking the authoring
// loop is how prose that stopped being true gets fixed (#1431). That
// boundary is why the vocabulary stays closed.
//
// Anything brace-delimited that is not a well-formed delta template —
// an unknown tag, a malformed or empty value, an unterminated open —
// passes through byte-for-byte, unexpanded. A visible bug beats a
// silent drop: a verbatim template in the prompt is something the
// authoring loop or its operator can see and fix, where a silently
// dropped phrase reads as nothing at all. A string containing no "{{"
// is returned unchanged without allocating.
func ExpandTemporalTemplates(s string, now time.Time) string {
	i := strings.Index(s, temporalOpen)
	if i < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i >= 0 {
		b.WriteString(s[:i])
		s = s[i:]
		if expanded, consumed, ok := expandLeadingTemporal(s, now); ok {
			b.WriteString(expanded)
			s = s[consumed:]
		} else {
			// Not a well-formed template. Emit the opening braces
			// literally and rescan just past them, so a stray "{{"
			// earlier in the prose cannot swallow a valid template
			// that follows it. A fully malformed {{...}} still lands
			// verbatim: its close braces carry through as plain text.
			b.WriteString(temporalOpen)
			s = s[len(temporalOpen):]
		}
		i = strings.Index(s, temporalOpen)
	}
	b.WriteString(s)
	return b.String()
}

// expandLeadingTemporal attempts to expand the template opening at the
// start of s (the caller guarantees s begins with "{{"). It returns
// the rendered replacement, the byte length of the template consumed,
// and whether the leading token was a well-formed {{delta:...}}
// template. The grammar is strict — no interior whitespace, no case
// folding — because the templates are machine-taught, not hand-typed,
// and leniency here would just widen the set of near-misses that
// silently half-work.
func expandLeadingTemporal(s string, now time.Time) (string, int, bool) {
	// The tag check comes before any search, and the search for the
	// close is bounded at the next opening. Order matters for cost, not
	// correctness: a malformed document with many openings and one
	// distant close would otherwise rescan nearly the whole suffix per
	// opening — quadratic on a per-turn path. A well-formed value never
	// contains "{{", so the bound rejects exactly the strings the date
	// parsers were going to reject anyway.
	rest, ok := strings.CutPrefix(s[len(temporalOpen):], temporalDelta)
	if !ok {
		return "", 0, false
	}
	searchIn := rest
	if next := strings.Index(rest, temporalOpen); next >= 0 {
		searchIn = rest[:next]
	}
	closeAt := strings.Index(searchIn, temporalClose)
	if closeAt < 0 {
		return "", 0, false
	}
	value := rest[:closeAt]
	end := len(temporalOpen) + len(temporalDelta) + closeAt
	// A bare date first: RFC3339 rejects it anyway, and the two forms
	// deliberately render differently — a date occupies a day, so day
	// words; an instant is a moment, so a signed compact delta. The
	// zone a bare date parses in is irrelevant: FormatDayDelta reads
	// each argument's calendar fields in its own location.
	if day, err := time.Parse(time.DateOnly, value); err == nil {
		return FormatDayDelta(day, now), end + len(temporalClose), true
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return FormatDeltaOnly(t, now), end + len(temporalClose), true
	}
	return "", 0, false
}
