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
	// tightened sentence recovers: on a single-line projection a tenth of the
	// limit is a dozen characters, and a gap of a few dozen still closes by
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
	// count of unit against limit.
	unit         unit
	count, limit int
	// field is the projection's field, and words and runes the value's
	// length in each unit, so a shrink can weigh every limit.
	field        Field
	words, runes int
}

// newOverBudget reports field's value, words and runes long, over its limit
// counted in u.
func newOverBudget(field Field, u unit, words, runes int) *overBudget {
	count, limit := runes, field.MaxRunes
	if u == unitWords {
		count, limit = words, field.MaxWords
	}
	return &overBudget{error: overBudgetText(field.Key, u, count, limit), key: field.Key, unit: u, count: count, limit: limit, field: field, words: words, runes: runes}
}

// overBudgetText reports one projection that exceeds a limit counted in u:
// the observed count, the limit, the overage, and the lever sized to that
// overage. Naming only the limit teaches the wrong repair for a large
// overage — the model rewords, lands still over, and resends — so a gap
// rewording rarely closes says so and points at removing whole items
// instead.
func overBudgetText(key string, u unit, count, limit int) error {
	over := count - limit
	if rewordable(u, over, limit) {
		return fmt.Errorf("%s is %s and the limit is %d (%d over); reword to cut at least %s — a gap this small closes by tightening the phrasing without dropping content", key, u.count(count), limit, over, u.count(over))
	}
	return fmt.Errorf("%s is %s and the limit is %d (%d over); rewording alone rarely closes a gap this large — remove whole items totalling at least %s: anything resolved, superseded, or already said elsewhere leaves this field entirely", key, u.count(count), limit, over, u.count(over))
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
