package mqtt

import (
	"sync"
	"time"
)

// DailyTokens tracks token usage that resets at local midnight. It is
// safe for concurrent use from multiple goroutines. The accumulator
// implements the TokenObserver interface so it can be wired directly
// into the API server's token recording path.
type DailyTokens struct {
	mu       sync.Mutex
	input    int64
	output   int64
	requests int64
	resetDay int // day-of-year of last reset
	loc      *time.Location
}

// NewDailyTokens creates a new accumulator using the given timezone for
// midnight detection. If loc is nil, [time.Local] is used.
func NewDailyTokens(loc *time.Location) *DailyTokens {
	if loc == nil {
		loc = time.Local
	}
	return &DailyTokens{
		resetDay: time.Now().In(loc).YearDay(),
		loc:      loc,
	}
}

// OnTokens records token counts from a completed LLM request. If the
// local date has changed since the last recording, counters are reset
// before the new values are added. This method satisfies the
// api.TokenObserver interface.
func (d *DailyTokens) OnTokens(inputTokens, outputTokens int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.maybeReset()
	d.input += int64(inputTokens)
	d.output += int64(outputTokens)
	d.requests++
}

// Snapshot returns the current accumulated totals after checking for
// midnight rollover. The returned values are input tokens, output
// tokens, and request count.
func (d *DailyTokens) Snapshot() (input, output, requests int64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.maybeReset()
	return d.input, d.output, d.requests
}

// maybeReset zeroes the accumulators if the local day-of-year has
// changed. Must be called with d.mu held.
func (d *DailyTokens) maybeReset() {
	today := time.Now().In(d.loc).YearDay()
	if today != d.resetDay {
		d.input = 0
		d.output = 0
		d.requests = 0
		d.resetDay = today
	}
}
