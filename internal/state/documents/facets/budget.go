package facets

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// unit names what a projection budget counts.
type unit string

const (
	unitWords      unit = "word"
	unitCharacters unit = "character"
)

// count renders n of u for a model to read: "1 word", "20 words".
func (u unit) count(n int) string {
	if n == 1 {
		return "1 " + string(u)
	}
	return fmt.Sprintf("%d %ss", n, u)
}

// measure returns text's length in u.
func (u unit) measure(text string) int {
	if u == unitWords {
		return WordCount(text)
	}
	return utf8.RuneCountInString(text)
}

// WordCount counts text's words as [strings.Fields] splits them: runs of
// characters between whitespace, so "14:30" and "kb:notes/a.md" are one
// word each. That is how a writer counts its own line, so the count never
// exceeds the words a model believes it wrote.
func WordCount(text string) int {
	return len(strings.Fields(text))
}

// Budget names the limit a writer is told for f, as "20 words" or "500
// characters", or "" when f has none. A word limit is named over the
// character limit beside it: a model can keep count of words while it
// writes but not of characters, and the character limit only refuses text
// no ordinary line reaches.
func (f Field) Budget() string {
	switch {
	case f.MaxWords > 0:
		return unitWords.count(f.MaxWords)
	case f.MaxRunes > 0:
		return unitCharacters.count(f.MaxRunes)
	}
	return ""
}

// BudgetClause is the sentence every writer appends to a projection's
// parameter description, with its leading space, or "" when f has no
// budget. Every writer uses it, so every surface teaches a budget in the
// same words.
func (f Field) BudgetClause() string {
	budget := f.Budget()
	if budget == "" {
		return ""
	}
	return " Maximum " + budget + " — a ceiling, not a target; compose comfortably under it."
}

// overBudget returns the violation to report when value breaks one of f's
// limits, or nil when it fits them all. A value over its word limit is
// reported in words alone: cutting words is the repair a writer can count,
// and naming the character backstop beside it would hand back the
// character target the word limit replaces. The backstop is reported only
// for a value within its words.
func (f Field) overBudget(value string) *overBudget {
	words, runes := WordCount(value), utf8.RuneCountInString(value)
	switch {
	case f.MaxWords > 0 && words > f.MaxWords:
		return newOverBudget(f, unitWords, words, runes)
	case f.MaxRunes > 0 && runes > f.MaxRunes:
		return newOverBudget(f, unitCharacters, words, runes)
	}
	return nil
}
