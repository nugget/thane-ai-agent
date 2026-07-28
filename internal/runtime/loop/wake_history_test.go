package loop

import (
	"testing"
	"time"
)

func TestWakeHistoryCountsTheTrailingDay(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	l := &Loop{}
	for _, at := range []time.Time{
		now.Add(-30 * time.Hour), // outside the window
		now.Add(-25 * time.Hour), // outside the window
		now.Add(-23 * time.Hour),
		now.Add(-2 * time.Hour),
		now,
	} {
		l.recordWakeLocked(at)
	}
	if got := l.wakesInWindowLocked(now); got != 3 {
		t.Errorf("wakes in window = %d, want 3", got)
	}
	if len(l.wakeTimes) != 3 {
		t.Errorf("retained %d instants, want the pruned 3", len(l.wakeTimes))
	}
}

// TestWakeHistoryAgesOutWithoutANewWake is why the count is computed
// rather than read off len(). Pruning only happens on append, so a loop
// that has been asleep for a day would otherwise keep reporting the
// wakes it had before that sleep as if they were recent.
func TestWakeHistoryAgesOutWithoutANewWake(t *testing.T) {
	start := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	l := &Loop{}
	l.recordWakeLocked(start)
	l.recordWakeLocked(start.Add(time.Hour))

	if got := l.wakesInWindowLocked(start.Add(2 * time.Hour)); got != 2 {
		t.Fatalf("wakes shortly after = %d, want 2", got)
	}
	if got := l.wakesInWindowLocked(start.Add(48 * time.Hour)); got != 0 {
		t.Errorf("wakes two days later = %d, want 0", got)
	}
}

func TestWakeHistoryDropsOldestPastTheCap(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	l := &Loop{}
	// Every instant is inside the window, so only the hard cap can bound
	// the slice — this is the sub-minute-poller case.
	for i := range maxWakeHistory + 100 {
		l.recordWakeLocked(now.Add(time.Duration(i) * time.Second))
	}
	if len(l.wakeTimes) != maxWakeHistory {
		t.Fatalf("retained %d instants, want the cap %d", len(l.wakeTimes), maxWakeHistory)
	}
	// The newest survive: a truncated history should still describe the
	// recent past rather than a stale prefix of it.
	newest := now.Add(time.Duration(maxWakeHistory+99) * time.Second)
	if got := l.wakeTimes[len(l.wakeTimes)-1]; !got.Equal(newest) {
		t.Errorf("newest retained instant = %v, want %v", got, newest)
	}
}
