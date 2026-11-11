package facets

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
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

// WordCount counts text's words as a writer counts its own line: the runs
// of characters between whitespace ([strings.Fields]) that hold a letter
// or a digit. So "14:30" and "kb:notes/a.md" are one word each, and a
// spaced dash, bullet or ampersand is no word at all. Text in a script
// written without spaces between words counts one word per run, so only a
// field's character limit bounds it.
func WordCount(text string) int {
	return len(wordLengths(text))
}

// wordLengths returns the length in characters of each of text's words, as
// [WordCount] counts them.
func wordLengths(text string) []int {
	var lengths []int
	for _, field := range strings.Fields(text) {
		if strings.IndexFunc(field, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) >= 0 {
			lengths = append(lengths, utf8.RuneCountInString(field))
		}
	}
	return lengths
}

// wordsToShed returns how many of text's words must go for it to lose over
// characters whichever words are removed. It counts the shortest first,
// each with the one separator removing it frees, so taking out any that
// many whole words loses at least over.
func wordsToShed(text string, over int) int {
	lengths := wordLengths(text)
	slices.Sort(lengths)
	shed := 0
	for _, n := range lengths {
		if over <= 0 {
			break
		}
		over -= n + 1
		shed++
	}
	return shed
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

// AnyFormatBudgetClause is [Field.BudgetClause] for a description written
// before the projection's format is known, such as a tool schema shared by
// every document. A projection declared json has no word limit
// ([Contract.Fields]), so a word budget here also names the character limit
// a json projection is held to instead.
func (f Field) AnyFormatBudgetClause() string {
	clause := f.BudgetClause()
	if f.MaxWords > 0 && f.MaxRunes > 0 {
		clause += " A projection declared format json is held to " + unitCharacters.count(f.MaxRunes) + " instead."
	}
	return clause
}

// overBudget returns the violation to report when value breaks one of f's
// limits, or nil when it fits them all. A value over its word limit is
// reported in words alone: cutting words is the repair a writer can count,
// and naming the character backstop beside it would hand back the
// character target the word limit replaces. When it is past the backstop
// too, cutting any of the words it is told to cut clears both. The
// backstop is reported only for a value within its words.
func (f Field) overBudget(value string) *overBudget {
	words, runes := WordCount(value), utf8.RuneCountInString(value)
	switch {
	case f.MaxWords > 0 && words > f.MaxWords:
		return newOverBudget(f, unitWords, value, words, runes)
	case f.MaxRunes > 0 && runes > f.MaxRunes:
		return newOverBudget(f, unitCharacters, value, words, runes)
	}
	return nil
}
