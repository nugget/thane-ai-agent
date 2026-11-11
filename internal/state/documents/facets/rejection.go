package facets

import (
	"errors"
	"fmt"
)

const (
	// rewordableOverageDivisor sets where rewording stops being the lever for
	// an over-budget projection. Tightening the phrasing of a value recovers
	// about a tenth of its length; past that the value usually carries more
	// items than its budget holds, and removing items is what closes the gap.
	rewordableOverageDivisor = 10
	// minRewordableOverage keeps the tenth from shrinking below what one
	// tightened sentence recovers: a tenth of status_line's 180-character
	// backstop is 18 characters, and a gap of a few dozen still closes by
	// rewording without dropping a fact.
	minRewordableOverage = 30
	// minRewordableWords is minRewordableOverage counted in words: a few
	// dozen characters of a status line are about five words.
	minRewordableWords = 5
)

// rewordable reports whether an overage of over u past limit is small
// enough that tightening the phrasing closes it: within a tenth of the
// limit or the floor for u, whichever is larger.
func rewordable(u unit, over, limit int) bool {
	floor := minRewordableOverage
	if u == unitWords {
		floor = minRewordableWords
	}
	return over <= max(limit/rewordableOverageDivisor, floor)
}

// overBudget is one projection over one of its limits. It is a type rather
// than text so [Shrinker.Fit] can tell a refusal made only of small overruns
// from one carrying any other violation.
type overBudget struct {
	error
	key string
	// unit, count and limit describe the limit reported: the value is
	// count of unit against limit. cut is how much of unit the value must
	// lose to fit every limit, at least count-limit.
	unit              unit
	count, limit, cut int
	// field is the projection's field, and words and runes the value's
	// length in each unit, so a shrink can weigh every limit.
	field        Field
	words, runes int
}

// newOverBudget reports field's value, words and runes long, over its limit
// counted in u. A value over its words and past its character limit too
// must lose enough words to clear both whichever words go, counted from its
// shortest ([wordsToShed]): a cut sized at its average word length leaves
// a line over when the words cut are its short ones.
func newOverBudget(field Field, u unit, value string, words, runes int) *overBudget {
	count, limit := runes, field.MaxRunes
	if u == unitWords {
		count, limit = words, field.MaxWords
	}
	cut := count - limit
	if u == unitWords && field.MaxRunes > 0 && runes > field.MaxRunes {
		cut = max(cut, wordsToShed(value, runes-field.MaxRunes))
	}
	return &overBudget{error: overBudgetText(field.Key, u, count, limit, cut), key: field.Key, unit: u, count: count, limit: limit, cut: cut, field: field, words: words, runes: runes}
}

// ask names the limits a shrink of the value must reach: the one reported,
// and the character limit too when the value is past that as well, since
// a reply that cuts only words can stay over it.
func (e *overBudget) ask() string {
	ask := e.unit.count(e.limit)
	if e.unit == unitWords && e.field.MaxRunes > 0 && e.runes > e.field.MaxRunes {
		ask += " and " + unitCharacters.count(e.field.MaxRunes)
	}
	return ask
}

// overBudgetText reports one projection that exceeds a limit counted in u:
// the observed count, the limit, the overage, and the lever sized to cut,
// what the value must lose to fit, which is the overage unless its words
// run long. Naming only the limit teaches the wrong repair for a large
// overage — the model rewords, lands still over, and resends — so a gap
// rewording rarely closes says so and points at removing whole items
// instead.
func overBudgetText(key string, u unit, count, limit, cut int) error {
	head := fmt.Sprintf("%s is %s and the limit is %d (%d over)", key, u.count(count), limit, count-limit)
	if cut > count-limit {
		head += ", and its words run long"
	}
	if rewordable(u, cut, limit) {
		return fmt.Errorf("%s; reword to cut at least %s — a gap this small closes by tightening the phrasing without dropping content", head, u.count(cut))
	}
	return fmt.Errorf("%s; rewording alone rarely closes a gap this large — remove whole items totalling at least %s: anything resolved, superseded, or already said elsewhere leaves this field entirely", head, u.count(cut))
}

// InvalidProjectionsError frames every violation one faceted write found,
// where subject names what was refused ("contact dossier projections").
// Every structured writer shares the frame so a refusal reads the same
// whichever tool raised it: nothing landed, and one call that corrects every
// listed field is the whole recovery. It names no retry count, because a
// count is a stopping rule unrelated to whether the correction is complete.
// Call it only with at least one violation.
func InvalidProjectionsError(subject string, violations ...error) error {
	return fmt.Errorf("%s are invalid and nothing was written; correct every listed field in your next call — a listed field resent unchanged is refused the same way: %w", subject, errors.Join(violations...))
}
