package facets

import (
	"strings"
	"testing"
)

// TestWordCountSplitsOnWhitespaceOnly pins how a word budget counts: runs
// between whitespace, so the punctuation-joined times, identifiers and
// ranges a status line is full of each count once, as a writer counts them,
// and a run with no letter or digit, such as a spaced dash, counts not at
// all.
func TestWordCountSplitsOnWhitespaceOnly(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		text string
		want int
	}{
		{text: "", want: 0},
		{text: " \t\n ", want: 0},
		{text: "one", want: 1},
		{text: "one two", want: 2},
		{text: "  leading,  doubled\tand trailing \n", want: 4},
		{text: "14:30–15:00 at kb:notes/a.md", want: 3},
		{text: "hyphen-joined em—dashed words", want: 3},
		{text: "no-break space and narrow space", want: 5},
		{text: "café naïve 東京", want: 3},
		{text: "panel clean — baselines steady", want: 4},
		{text: "printer · toner | desk & lobby -> done / later", want: 6},
		{text: "🚀 launch — #1630 v0.10.4", want: 3},
	} {
		if got := WordCount(tt.text); got != tt.want {
			t.Errorf("WordCount(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}

// TestEachProjectionNamesItsBudget pins the budget each projection teaches
// and enforces: status_line in words with a character backstop, the others
// in characters. Every word limit sits beside a character limit, because a
// word count alone does not bound length and the shrink call's output
// bound is drawn from the character limit.
func TestEachProjectionNamesItsBudget(t *testing.T) {
	t.Parallel()

	const tail = " — a ceiling, not a target; compose comfortably under it."
	want := map[string]struct {
		words, runes int
		budget       string
	}{
		"status_line": {words: 20, runes: 180, budget: "20 words"},
		"teaser":      {runes: 500, budget: "500 characters"},
		"digest":      {runes: 2048, budget: "2048 characters"},
		"full":        {},
	}
	for _, key := range Keys() {
		field, _ := FieldByKey(key)
		w := want[key]
		if field.MaxWords != w.words || field.MaxRunes != w.runes || field.Budget() != w.budget {
			t.Errorf("%s = %d words, %d characters, budget %q; want %d, %d, %q", key, field.MaxWords, field.MaxRunes, field.Budget(), w.words, w.runes, w.budget)
		}
		wantClause := ""
		if w.budget != "" {
			wantClause = " Maximum " + w.budget + tail
		}
		if got := field.BudgetClause(); got != wantClause {
			t.Errorf("%s BudgetClause() = %q, want %q", key, got, wantClause)
		}
		if field.MaxWords > 0 && field.MaxRunes == 0 {
			t.Errorf("%s has a word limit and no character limit", key)
		}
	}
	if scaffold := DefaultContract().RenderScaffold(); !strings.Contains(scaffold, "one standalone line of what is true right now, <=20 words)") ||
		!strings.Contains(scaffold, "<=500 characters)") || strings.Contains(scaffold, "<=180") {
		t.Errorf("scaffold = %q, want each placeholder to name the budget a writer is told", scaffold)
	}
}

// TestWordCutClearsBothLimits generates status lines over their words and
// past the backstop, of every word length from short to long, and checks
// the refusal names a word cut that, taken from the line, fits both limits:
// the word count, and the characters at the line's own word length.
func TestWordCutClearsBothLimits(t *testing.T) {
	t.Parallel()

	field, _ := FieldByKey(string(StatusLine))
	for words := statusLineMaxWords + 1; words <= statusLineMaxWords+10; words++ {
		for length := 5; length <= 16; length++ {
			line := wordsOf(words, strings.Repeat("w", length))
			over := field.overBudget(line)
			if over == nil || over.unit != unitWords {
				t.Fatalf("%d words of %d: overBudget = %+v, want a word violation", words, length, over)
			}
			if over.cut < words-statusLineMaxWords {
				t.Fatalf("%d words of %d: cut %d is less than the word overage", words, length, over.cut)
			}
			if fits := wordsOf(words-over.cut, strings.Repeat("w", length)); field.overBudget(fits) != nil {
				t.Errorf("%d words of %d: cutting %d leaves %d characters, still over", words, length, over.cut, len(fits))
			}
			if want := "at least " + unitWords.count(over.cut); !strings.Contains(over.Error(), want) {
				t.Errorf("%d words of %d: refusal %q, want it to say %q", words, length, over, want)
			}
			if long := over.cut > words-statusLineMaxWords; long != strings.Contains(over.Error(), "its words run long") {
				t.Errorf("%d words of %d: refusal %q; says its words run long = %v, want %v", words, length, over, !long, long)
			}
		}
	}
}

// TestValidateEnforcesBothStatusLineLimitsAtTheEdge pins the boundaries:
// twenty words and 180 characters fit, one more of either is refused, and
// the refusal names the unit it counted.
func TestValidateEnforcesBothStatusLineLimitsAtTheEdge(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name, line, want string
	}{
		{name: "twenty words", line: wordsOf(statusLineMaxWords, "word")},
		{name: "twenty words in 180 characters", line: wordsOf(statusLineMaxWords-1, "eightchr") + " ninechars"},
		{name: "180 characters in one word", line: strings.Repeat("é", statusLineMaxRunes)},
		{name: "twenty-one words", line: wordsOf(statusLineMaxWords+1, "word"), want: "status_line is 21 words"},
		{name: "twenty-one words split by a no-break space", line: wordsOf(statusLineMaxWords, "word") + " more", want: "status_line is 21 words"},
		{name: "181 characters in one word", line: strings.Repeat("é", statusLineMaxRunes+1), want: "status_line is 181 characters"},
	} {
		if tt.want == "" && (WordCount(tt.line) > statusLineMaxWords || len([]rune(tt.line)) > statusLineMaxRunes) {
			t.Fatalf("%s: %q is not within both limits", tt.name, tt.line)
		}
		payload := validShrinkPayload()
		payload.StatusLine = tt.line
		err := DefaultContract().Validate(payload)
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%s: Validate() = %v, want it accepted", tt.name, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("%s: Validate() = %v, want a refusal containing %q", tt.name, err, tt.want)
		}
	}
}
