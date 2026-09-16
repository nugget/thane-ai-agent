package promptfmt

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// BakedDeltaTier ranks how confident a finding is that stored prose
// carries a relative time that was true only at the moment it was
// written. The tiers exist because the two classes deserve different
// answers: one is unmistakably this project's own machine output and
// can be refused, the other is ordinary English that happens to be
// time-relative and can only be pointed at.
type BakedDeltaTier int

const (
	// BakedDeltaFormatted is this project's own delta vocabulary —
	// "-21m", "+3d16h", "-513s" — the exact shapes [FormatDeltaOnly]
	// emits. Prose containing one almost always got it by transcribing
	// a tool result, and it is wrong within the minute. Callers refuse
	// these.
	BakedDeltaFormatted BakedDeltaTier = iota + 1

	// BakedDeltaNarrative is natural-language relative time — "21
	// minutes ago", "yesterday", "in three days". Just as stale, but
	// the phrasing is ordinary English and a matcher cannot be sure it
	// is dating anything, so callers warn rather than refuse.
	BakedDeltaNarrative
)

// BakedDelta is one relative-time expression found in authored prose,
// carrying enough to name it back to the author: the text itself and
// the 1-indexed line it sits on.
type BakedDelta struct {
	Text string
	Tier BakedDeltaTier
	Line int
}

// narrativeDeltas matches the natural-language relative-time forms
// worth flagging. Deliberately narrow: every pattern here names a
// distance from now, so vague currency words ("recently", "currently")
// are left alone — they do not decay into a false statement the way a
// counted distance does. Day words are included because they are
// exactly as perishable as "-1d" despite reading as plain English.
var narrativeDeltas = regexp.MustCompile(`(?i)\b(` +
	narrativeQuantity + `\s+` + narrativeUnit + `\s+(?:ago|from\s+now)` +
	`|in\s+` + narrativeQuantity + `\s+` + narrativeUnit +
	`|(?:yesterday|tomorrow)` +
	`|(?:last|next)\s+(?:night|week|month|year|monday|tuesday|wednesday|thursday|friday|saturday|sunday)` +
	`|(?:this|earlier\s+this)\s+(?:morning|afternoon|evening)` +
	`|just\s+now` +
	`)\b`)

// narrativeQuantity and narrativeUnit spell the counted part of a
// relative phrase. The multi-word quantities lead the alternation
// because Go's regexp is leftmost-first at a given start position: with
// bare "a" first, "a few hours ago" reports itself as "few hours ago"
// and the warning quotes a phrase the author never wrote.
const (
	narrativeQuantity = `(?:a\s+few|a\s+couple\s+of|couple\s+of|several|a|an|one|two|three|four|five|six|seven|eight|nine|ten|few|\d+)`
	narrativeUnit     = `(?:second|minute|hour|day|week|month|year)s?`
)

// FindBakedDeltas reports relative-time expressions in authored prose,
// so a write path can refuse the ones that are unmistakably machine
// output and warn about the rest. The whole point of the
// {{delta:...}} template is that the value re-renders on every read;
// prose that hardcodes the rendered form is true once and false
// forever after.
//
// Three regions are exempt, and one structural rule covers all three:
// YAML frontmatter (where created_delta/updated_delta are stamped by
// the writer, not the author), fenced code blocks, and inline code
// spans. That single rule is why JSON facets need no special case —
// a faceted document stores a JSON facet as a ```json fence, so it is
// already skipped, and an author documenting the delta format writes
// it in backticks the way documentation does anyway.
//
// The scan is line-oriented so findings can name a line, and it never
// allocates when the prose contains no candidate.
func FindBakedDeltas(body string) []BakedDelta {
	var found []BakedDelta

	lines := strings.Split(body, "\n")
	skipTo := frontmatterEnd(lines)
	var fence fenceState

	for i, raw := range lines {
		if i < skipTo {
			continue
		}
		line := strings.TrimRight(raw, "\r")
		lineNo := i + 1

		if closed := fence.consume(line); closed || fence.open {
			continue
		}

		for _, prose := range outsideCodeSpans(line) {
			for _, text := range formattedDeltasIn(prose) {
				found = append(found, BakedDelta{Text: text, Tier: BakedDeltaFormatted, Line: lineNo})
			}
			for _, m := range narrativeDeltas.FindAllString(prose, -1) {
				found = append(found, BakedDelta{Text: m, Tier: BakedDeltaNarrative, Line: lineNo})
			}
		}
	}
	return found
}

// frontmatterEnd returns the first line index that is prose, skipping a
// leading YAML frontmatter block when there is one.
//
// A block counts only when its closing delimiter is actually present.
// The guarded inputs are document bodies, where a leading "---" is far
// more likely a thematic break than an envelope, and an unterminated
// block would otherwise swallow the entire document — turning a
// horizontal rule at the top of a page into a way to write anything at
// all past the guard.
func frontmatterEnd(lines []string) int {
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != "---" {
		return 0
	}
	for i := 1; i < len(lines); i++ {
		switch strings.TrimRight(lines[i], "\r") {
		case "---", "...":
			return i + 1
		}
	}
	return 0
}

// fenceState tracks an open fenced code block. CommonMark closes a
// fence only with the character it opened with, at least as long as the
// opening run, so a "```" line inside a "~~~" block is content — and
// treating it as a close would drop the rest of that block back into
// the scan and refuse a write over a documented example.
type fenceState struct {
	open   bool
	marker byte
	length int
}

// consume applies one line to the fence state and reports whether that
// line was the closing delimiter (which is itself not prose).
func (f *fenceState) consume(line string) bool {
	marker, length, ok := fenceDelimiter(line)
	if !ok {
		return false
	}
	if !f.open {
		f.open, f.marker, f.length = true, marker, length
		return true
	}
	if marker == f.marker && length >= f.length {
		f.open, f.marker, f.length = false, 0, 0
		return true
	}
	return false
}

// fenceDelimiter reports the marker byte and run length of a fence
// line: up to three leading spaces, then three or more backticks or
// tildes.
func fenceDelimiter(line string) (byte, int, bool) {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) || (line[i] != '`' && line[i] != '~') {
		return 0, 0, false
	}
	marker := line[i]
	run := 0
	for i+run < len(line) && line[i+run] == marker {
		run++
	}
	if run < 3 {
		return 0, 0, false
	}
	return marker, run, true
}

// outsideCodeSpans splits a line into the runs that sit outside inline
// code spans. CommonMark delimits a span with equal-length backtick
// runs, so "“-2h“" is code just as "`-2h`" is; pairing single
// backticks instead would leave the inner text exposed and refuse a
// write over the very escape hatch the refusal message offers.
//
// An unterminated run opens nothing — the rest of the line stays prose
// — because a stray tick in ordinary writing must not blind the scanner
// to everything after it.
func outsideCodeSpans(line string) []string {
	if !strings.Contains(line, "`") {
		return []string{line}
	}
	var out []string
	pos := 0
	for pos < len(line) {
		openStart, openLen := backtickRun(line, pos)
		if openLen == 0 {
			out = append(out, line[pos:])
			return out
		}
		closeStart, ok := matchingBacktickRun(line, openStart+openLen, openLen)
		if !ok {
			out = append(out, line[pos:])
			return out
		}
		out = append(out, line[pos:openStart])
		pos = closeStart + openLen
	}
	return out
}

// backtickRun finds the next run of backticks at or after from,
// returning its start and length (length 0 when there is none).
func backtickRun(line string, from int) (int, int) {
	start := strings.IndexByte(line[from:], '`')
	if start < 0 {
		return 0, 0
	}
	start += from
	run := 0
	for start+run < len(line) && line[start+run] == '`' {
		run++
	}
	return start, run
}

// matchingBacktickRun finds the next backtick run of exactly want
// length, which is what closes a span of that width.
func matchingBacktickRun(line string, from, want int) (int, bool) {
	for pos := from; pos < len(line); {
		start, run := backtickRun(line, pos)
		if run == 0 {
			return 0, false
		}
		if run == want {
			return start, true
		}
		pos = start + run
	}
	return 0, false
}

// formattedDeltasIn finds this package's delta vocabulary in a run of
// prose. Hand-rolled rather than a regexp because the boundary rule
// needs a look behind the candidate — RE2 has no lookaround, and
// without one "-98.41852298892312" (a longitude, which this corpus is
// full of) would need the surrounding bytes checked anyway.
//
// A candidate is confirmed by [parseDeltaTerms], which is the vocabulary
// [ParseTimeOrDelta] accepts rather than the narrower set
// [FormatDeltaOnly] emits. That is deliberate and slightly wider than
// "machine output": "-30m" and "-2w" never come off a formatter (which
// would render them "-1800s" and "-14d"), but a model that writes
// either has still hardcoded a relative time that decays, and a tool
// argument may echo one back verbatim. Matching the parser also keeps
// one definition of the grammar instead of a second that drifts.
func formattedDeltasIn(prose string) []string {
	var out []string
	for i := 0; i < len(prose); i++ {
		if prose[i] != '+' && prose[i] != '-' {
			continue
		}
		// A sign glued to a word or a number is part of that token
		// ("UTC-5", "29.83-98.46"), not the start of a delta.
		if gluedBefore(prose, i) {
			continue
		}
		end := i + 1
		for end < len(prose) && (isDigit(prose[end]) || isDeltaUnit(prose[end])) {
			end++
		}
		if end == i+1 {
			continue
		}
		// A trailing word character means the run was a fragment of
		// something longer ("+3days", "-5min"), which the formatters
		// never emit.
		if gluedAfter(prose, end) {
			continue
		}
		candidate := prose[i:end]
		if _, err := parseDeltaTerms(candidate); err != nil {
			continue
		}
		out = append(out, candidate)
		i = end - 1
	}
	return out
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isDeltaUnit(b byte) bool {
	_, ok := deltaUnits[b]
	return ok
}

// glued reports whether the rune ending at (or starting at) an offset
// joins a candidate to a neighbouring token. Letters and digits of any
// script count, not just ASCII: "café-1h" is one identifier the same
// way "retention-1h" is, and an ASCII-only check would refuse a write
// over the accented one while exempting the plain one. '.' and '_'
// join too, which is what keeps decimal coordinates out of the results.
func gluedRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_'
}

func gluedBefore(prose string, i int) bool {
	if i == 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(prose[:i])
	return gluedRune(r)
}

func gluedAfter(prose string, i int) bool {
	if i >= len(prose) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(prose[i:])
	return gluedRune(r)
}
