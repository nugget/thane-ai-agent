package loop

import "time"

// wakeHistoryWindow is the span [Loop.wakeHistory] retains. Twenty-four
// hours is the window a loop reasons in when it asks whether its current
// cadence is proportionate — long enough to cover a full day/night cycle
// of household activity, short enough that a rate change shows up.
const wakeHistoryWindow = 24 * time.Hour

// maxWakeHistory caps the retained records so the ring cannot grow
// without bound. A loop with a cognitive cadence never approaches it: at
// the tightest core envelope (a 15m floor) a day holds 96 wakes. Only a
// sub-minute handler poller saturates it, and those loops run no model
// turn, so the undercount it would cause never reaches a prompt — it
// would only clip the operator-facing count in loop_status.
const maxWakeHistory = 2048

// wakeRecord is one retained iteration start: when it began and what
// woke it. The reason rides the ring so the trailing-day histogram in
// [Loop.wakeReasonsInWindowLocked] answers "who has been waking this
// loop" without consulting the persistent event journal.
type wakeRecord struct {
	at     time.Time
	reason WakeReason
}

// recordWakeLocked appends an iteration start and drops everything that
// has fallen out of the window. Called with l.mu held.
//
// It records the iteration that is beginning, so a loop reading the count
// mid-turn sees itself included — "this is my 48th turn today", not "I
// had 47 before this one". The former is the number a loop is actually
// deciding against.
func (l *Loop) recordWakeLocked(at time.Time, reason WakeReason) {
	l.wakeHistory = append(l.wakeHistory, wakeRecord{at: at, reason: reason})
	l.pruneWakeHistoryLocked(at)
}

// pruneWakeHistoryLocked drops records older than the window, then
// enforces the hard cap. Called with l.mu held.
func (l *Loop) pruneWakeHistoryLocked(now time.Time) {
	cutoff := now.Add(-wakeHistoryWindow)
	keep := 0
	for keep < len(l.wakeHistory) && l.wakeHistory[keep].at.Before(cutoff) {
		keep++
	}
	if keep > 0 {
		l.wakeHistory = append(l.wakeHistory[:0], l.wakeHistory[keep:]...)
	}
	if over := len(l.wakeHistory) - maxWakeHistory; over > 0 {
		l.wakeHistory = append(l.wakeHistory[:0], l.wakeHistory[over:]...)
	}
}

// wakesInWindowLocked counts the retained records inside the window as
// of now. Called with l.mu held.
//
// It counts rather than trusting len(l.wakeHistory) because the slice is
// only pruned when a wake is appended: a loop that has been sleeping for
// a day would otherwise report the wakes it had before that sleep.
func (l *Loop) wakesInWindowLocked(now time.Time) int {
	cutoff := now.Add(-wakeHistoryWindow)
	count := 0
	for i := len(l.wakeHistory) - 1; i >= 0; i-- {
		if l.wakeHistory[i].at.Before(cutoff) {
			break
		}
		count++
	}
	return count
}

// wakeReasonsInWindowLocked histograms the retained records inside the
// window by reason, as of now. Returns nil when the window is empty so
// callers can omit the field entirely. String-keyed for direct JSON
// serialization on [Status]. Called with l.mu held.
func (l *Loop) wakeReasonsInWindowLocked(now time.Time) map[string]int {
	cutoff := now.Add(-wakeHistoryWindow)
	var counts map[string]int
	for i := len(l.wakeHistory) - 1; i >= 0; i-- {
		if l.wakeHistory[i].at.Before(cutoff) {
			break
		}
		if counts == nil {
			counts = make(map[string]int)
		}
		counts[string(l.wakeHistory[i].reason)]++
	}
	return counts
}
