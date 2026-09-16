package phasetrace

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestUntracedContextIsANoOp pins that annotation sites never have to
// know whether anyone is listening, and cost nothing when nobody is.
func TestUntracedContextIsANoOp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	if From(ctx) != nil {
		t.Fatal("From returned a trace for an untraced context")
	}
	// Must not panic, and must be safe to defer.
	done := Phase(ctx, "anything")
	done()

	var absent *Trace
	if got := absent.Summary(); got != "" {
		t.Errorf("nil trace summarised as %q, want empty so the caller omits the field", got)
	}
}

// TestPhasesAreOrderedLongestFirst pins the shape a reader needs: the
// expensive phase is the answer, so it goes first.
func TestPhasesAreOrderedLongestFirst(t *testing.T) {
	t.Parallel()

	ctx, trace := New(context.Background())
	trace.add("cheap", 5*time.Millisecond)
	trace.add("expensive", 900*time.Millisecond)
	trace.add("middling", 40*time.Millisecond)
	_ = ctx

	got := trace.Summary()
	if !strings.HasPrefix(got, "expensive=900ms") {
		t.Errorf("summary = %q, want the costliest phase first", got)
	}
	for _, want := range []string{"middling=40ms", "cheap=5ms"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q missing %q", got, want)
		}
	}
}

// TestRepeatedPhasesAccumulate pins that a phase inside a loop reports
// the total spent there rather than the last pass — thirteen roots
// walked once each is one number, not the cost of the thirteenth.
func TestRepeatedPhasesAccumulate(t *testing.T) {
	t.Parallel()

	_, trace := New(context.Background())
	for range 3 {
		trace.add("per_root", 10*time.Millisecond)
	}
	if got := trace.Summary(); !strings.Contains(got, "per_root=30ms") {
		t.Errorf("summary = %q, want the phases summed", got)
	}
}

// TestPhaseMeasuresThroughTheContext pins the end-to-end path an
// annotation site actually uses.
func TestPhaseMeasuresThroughTheContext(t *testing.T) {
	t.Parallel()

	ctx, trace := New(context.Background())
	func() {
		defer Phase(ctx, "work")()
		time.Sleep(12 * time.Millisecond)
	}()

	got := trace.Summary()
	if !strings.HasPrefix(got, "work=") {
		t.Fatalf("summary = %q, want the phase recorded", got)
	}
	if strings.Contains(got, "work=0s") {
		t.Errorf("summary = %q, want a measured duration", got)
	}
}
