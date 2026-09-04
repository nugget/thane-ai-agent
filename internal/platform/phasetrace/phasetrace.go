// Package phasetrace records where time went inside a unit of work that
// is otherwise timed as a whole.
//
// It exists because a boundary timer answers "this took two seconds" and
// nothing else. Four separate investigations in one day stalled on that
// same shape — a queue that reported what it handed over but not what
// remained, an acknowledgement that reported running but not its
// outcome, an assembly that reported each provider's elapsed but not who
// spent the shared budget, and a context provider that reported a total
// across two halves it never separated. Each was diagnosable only by
// reading source and querying production by hand.
//
// The trace is carried on the context so a caller opts in by starting
// one, and any code beneath can annotate without knowing whether anyone
// is listening. Nothing is emitted here: the collector hands its phases
// back to whoever started it, to log at whatever threshold that caller
// already uses. Cost on an untraced path is one context lookup.
package phasetrace

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

type traceKey struct{}

// Trace accumulates named phase durations. A nil Trace is valid and
// records nothing, so annotation sites never branch.
type Trace struct {
	mu     sync.Mutex
	totals map[string]time.Duration
}

// New returns a context carrying a fresh trace, and the trace itself.
func New(ctx context.Context) (context.Context, *Trace) {
	t := &Trace{totals: make(map[string]time.Duration, 8)}
	return context.WithValue(ctx, traceKey{}, t), t
}

// From returns the trace carried by ctx, or nil when none is.
func From(ctx context.Context) *Trace {
	t, _ := ctx.Value(traceKey{}).(*Trace)
	return t
}

// Phase starts a named phase and returns the function that ends it.
// Intended to be deferred:
//
//	defer phasetrace.Phase(ctx, "health")()
//
// Repeated phases of the same name accumulate, so a phase inside a loop
// reports the total spent there rather than the last pass.
func Phase(ctx context.Context, name string) func() {
	t := From(ctx)
	if t == nil {
		return func() {}
	}
	start := time.Now()
	return func() { t.add(name, time.Since(start)) }
}

func (t *Trace) add(name string, d time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.totals[name] += d
}

// Summary renders the recorded phases longest-first, as a compact
// key=duration list suitable for a log attribute. Empty when nothing was
// recorded, so a caller can omit the field entirely.
func (t *Trace) Summary() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.totals) == 0 {
		return ""
	}

	names := make([]string, 0, len(t.totals))
	for name := range t.totals {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if t.totals[names[i]] != t.totals[names[j]] {
			return t.totals[names[i]] > t.totals[names[j]]
		}
		return names[i] < names[j]
	})

	var b strings.Builder
	for i, name := range names {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(name)
		b.WriteString("=")
		b.WriteString(t.totals[name].Round(time.Millisecond).String())
	}
	return b.String()
}
