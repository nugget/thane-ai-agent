package loop

import "time"

// wakeHistoryWindow is the span [Loop.wakeTimes] retains. Twenty-four
// hours is the window a loop reasons in when it asks whether its current
// cadence is proportionate — long enough to cover a full day/night cycle
// of household activity, short enough that a rate change shows up.
const wakeHistoryWindow = 24 * time.Hour

// maxWakeHistory caps the retained instants so the ring cannot grow
// without bound. A loop with a cognitive cadence never approaches it: at
// the tightest core envelope (a 15m floor) a day holds 96 wakes. Only a
// sub-minute handler poller saturates it, and those loops run no model
// turn, so the undercount it would cause never reaches a prompt — it
// would only clip the operator-facing count in loop_status.
const maxWakeHistory = 2048

// recordWakeLocked appends an iteration start and drops everything that
// has fallen out of the window. Called with l.mu held.
//
// It records the iteration that is beginning, so a loop reading the count
// mid-turn sees itself included — "this is my 48th turn today", not "I
// had 47 before this one". The former is the number a loop is actually
// deciding against.
func (l *Loop) recordWakeLocked(at time.Time) {
	l.wakeTimes = append(l.wakeTimes, at)
	l.pruneWakeHistoryLocked(at)
}

// pruneWakeHistoryLocked drops instants older than the window, then
// enforces the hard cap. Called with l.mu held.
func (l *Loop) pruneWakeHistoryLocked(now time.Time) {
	cutoff := now.Add(-wakeHistoryWindow)
	keep := 0
	for keep < len(l.wakeTimes) && l.wakeTimes[keep].Before(cutoff) {
		keep++
	}
	if keep > 0 {
		l.wakeTimes = append(l.wakeTimes[:0], l.wakeTimes[keep:]...)
	}
	if over := len(l.wakeTimes) - maxWakeHistory; over > 0 {
		l.wakeTimes = append(l.wakeTimes[:0], l.wakeTimes[over:]...)
	}
}

// wakesInWindowLocked counts the retained instants inside the window as
// of now. Called with l.mu held.
//
// It counts rather than trusting len(l.wakeTimes) because the slice is
// only pruned when a wake is appended: a loop that has been sleeping for
// a day would otherwise report the wakes it had before that sleep.
func (l *Loop) wakesInWindowLocked(now time.Time) int {
	cutoff := now.Add(-wakeHistoryWindow)
	count := 0
	for i := len(l.wakeTimes) - 1; i >= 0; i-- {
		if l.wakeTimes[i].Before(cutoff) {
			break
		}
		count++
	}
	return count
}
