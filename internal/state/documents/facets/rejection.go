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
)

// rewordable reports whether an overage is small enough that tightening the
// phrasing closes it: within a tenth of the limit or minRewordableOverage
// characters, whichever is larger.
func rewordable(over, limit int) bool {
	return over <= max(limit/rewordableOverageDivisor, minRewordableOverage)
}

// overBudgetError reports one projection that exceeds its rune budget: the
// observed count, the limit, the overage, and the lever sized to that overage.
// Naming only the limit teaches the wrong repair for a large overage — the
// model rewords, lands still over, and resends — so a gap rewording rarely
// closes says so and points at removing whole items instead.
func overBudgetError(key string, runes, limit int) error {
	over := runes - limit
	if rewordable(over, limit) {
		return fmt.Errorf("%s is %d characters and the limit is %d (%d over); reword to cut at least %s — a gap this small closes by tightening the phrasing without dropping content", key, runes, limit, over, characterCount(over))
	}
	return fmt.Errorf("%s is %d characters and the limit is %d (%d over); rewording alone rarely closes a gap this large — remove whole items totalling at least %s: anything resolved, superseded, or already said elsewhere leaves this field entirely", key, runes, limit, over, characterCount(over))
}

func characterCount(n int) string {
	if n == 1 {
		return "1 character"
	}
	return fmt.Sprintf("%d characters", n)
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
