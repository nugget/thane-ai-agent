package promptfmt

import (
	"regexp"
	"strings"
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
	inFence := false
	inFrontmatter := len(lines) > 0 && strings.TrimRight(lines[0], "\r") == "---"

	for i, raw := range lines {
		line := strings.TrimRight(raw, "\r")
		lineNo := i + 1

		// Frontmatter runs from the opening --- to the next one. Its
		// contents are the writer's stamps, not the author's prose.
		if inFrontmatter {
			if i > 0 && (line == "---" || line == "...") {
				inFrontmatter = false
			}
			continue
		}

		if isFenceDelimiter(line) {
			inFence = !inFence
			continue
		}
		if inFence {
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

// isFenceDelimiter reports whether a line opens or closes a fenced code
// block. Both CommonMark fence characters count, and an info string
// ("```json") rides along on the opening line.
func isFenceDelimiter(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

// outsideCodeSpans splits a line into the runs that sit outside
// backtick-delimited spans. An unterminated backtick opens nothing —
// the rest of the line stays prose — because a stray tick in ordinary
// writing must not blind the scanner to everything after it.
func outsideCodeSpans(line string) []string {
	if !strings.Contains(line, "`") {
		return []string{line}
	}
	var out []string
	for {
		open := strings.IndexByte(line, '`')
		if open < 0 {
			out = append(out, line)
			return out
		}
		close := strings.IndexByte(line[open+1:], '`')
		if close < 0 {
			out = append(out, line)
			return out
		}
		out = append(out, line[:open])
		line = line[open+1+close+1:]
	}
}

// formattedDeltasIn finds this package's own delta vocabulary in a run
// of prose. Hand-rolled rather than a regexp because the boundary rule
// needs a look behind the candidate — RE2 has no lookaround, and
// without one "-98.41852298892312" (a longitude, which this corpus is
// full of) would need the surrounding bytes checked anyway.
//
// A candidate is confirmed by [parseDeltaTerms], the same parser
// [ParseTimeOrDelta] uses, so the matcher accepts exactly the grammar
// the formatters emit rather than a second, drifting definition of it.
func formattedDeltasIn(prose string) []string {
	var out []string
	for i := 0; i < len(prose); i++ {
		if prose[i] != '+' && prose[i] != '-' {
			continue
		}
		// A sign glued to a word or a number is part of that token
		// ("UTC-5", "29.83-98.46"), not the start of a delta.
		if i > 0 && isDeltaBoundaryByte(prose[i-1]) {
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
		if end < len(prose) && isDeltaBoundaryByte(prose[end]) {
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

// isDeltaBoundaryByte reports whether a byte glues a candidate to a
// neighbouring token. Letters and digits obviously do; so does '.',
// which is what keeps decimal coordinates out of the results.
func isDeltaBoundaryByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', isDigit(b), b == '.', b == '_':
		return true
	}
	return false
}
