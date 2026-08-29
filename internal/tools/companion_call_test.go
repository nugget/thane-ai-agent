package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
)

// testCompanionTimeout stands in for companionCallTimeout so a test can
// reach a genuine expiry without waiting thirty seconds for one.
const testCompanionTimeout = 40 * time.Millisecond

var testCalendarCall = companion.CallRequest{
	Capability: "macos.calendar",
	Method:     "list_events",
}

// hangingCaller models the failure this bound exists for: a companion that
// is connected and never answers, the shape a Mac takes while it waits for
// someone to dismiss a permission prompt.
func hangingCaller(ctx context.Context, _ companion.CallRequest) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCallCompanionBoundsAHangingProvider(t *testing.T) {
	start := time.Now()

	_, err := callCompanionWithin(context.Background(), testCompanionTimeout, hangingCaller, testCalendarCall)

	if err == nil {
		t.Fatal("expected a hanging companion to produce an error")
	}
	// The caller had no deadline of its own — an ordinary conversational
	// turn — which is the case that previously hung outright.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("call outlived its bound: %s", elapsed)
	}
}

func TestCallCompanionExplainsAnExpiredBound(t *testing.T) {
	// A real expiry, not a synthesized sentinel: the caller blocks until the
	// bound fires, so the message is only produced if the bound truly did.
	_, err := callCompanionWithin(context.Background(), testCompanionTimeout, hangingCaller, testCalendarCall)
	if err == nil {
		t.Fatal("expected an error")
	}

	for _, want := range []string{
		"macos.calendar/list_events",
		"connected but not answering",
		"permission prompt",
		"Report this rather than retrying",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

func TestCallCompanionImposesABoundOnAnUnboundedCaller(t *testing.T) {
	var hadDeadline bool

	_, _ = callCompanionWithin(context.Background(), testCompanionTimeout,
		func(callCtx context.Context, _ companion.CallRequest) (json.RawMessage, error) {
			_, hadDeadline = callCtx.Deadline()
			return json.RawMessage(`{}`), nil
		}, testCalendarCall)

	if !hadDeadline {
		t.Fatal("a caller with no deadline of its own should still be given one")
	}
}

func TestCallCompanionDoesNotInventATimeoutThatDidNotHappen(t *testing.T) {
	// A companion may hand back DeadlineExceeded from somewhere else
	// entirely — an inner bound of its own, a wrapped transport error —
	// while this bound still has almost all of its window left. Reporting
	// that as "silent for the full window" would state a fact that never
	// occurred.
	_, err := callCompanionWithin(context.Background(), time.Minute,
		func(context.Context, companion.CallRequest) (json.RawMessage, error) {
			return nil, context.DeadlineExceeded
		}, testCalendarCall)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the caller's own error should pass through, got: %v", err)
	}
	if strings.Contains(err.Error(), "connected but not answering") {
		t.Errorf("should not claim a timeout that never expired: %v", err)
	}
}

func TestCallCompanionDoesNotClaimTheCallersDeadline(t *testing.T) {
	// When the turn's own context expired, blaming the companion would send
	// the model chasing the wrong fault.
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)

	_, err := callCompanionWithin(ctx, time.Minute, hangingCaller, testCalendarCall)

	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "connected but not answering") {
		t.Errorf("should not blame the companion for the caller's deadline: %v", err)
	}
}

func TestCallCompanionReportsARacingDisconnectAsADisconnect(t *testing.T) {
	// If the provider drops at the moment the bound fires, the disconnect is
	// the more useful fact — it tells the model the Mac is gone rather than
	// merely slow.
	disconnected := errors.New("companion provider disconnected")

	_, err := callCompanionWithin(context.Background(), testCompanionTimeout,
		func(callCtx context.Context, _ companion.CallRequest) (json.RawMessage, error) {
			<-callCtx.Done()
			return nil, disconnected
		}, testCalendarCall)

	if !errors.Is(err, disconnected) {
		t.Fatalf("a racing disconnect should be reported as itself, got: %v", err)
	}
}

func TestCallCompanionPassesOtherErrorsThrough(t *testing.T) {
	sentinel := errors.New("no connected companion app supports macos.calendar/list_events")

	_, err := callCompanionWithin(context.Background(), time.Minute,
		func(context.Context, companion.CallRequest) (json.RawMessage, error) {
			return nil, sentinel
		}, testCalendarCall)

	if !errors.Is(err, sentinel) {
		t.Fatalf("unrelated errors must pass through unchanged, got: %v", err)
	}
}

func TestCallCompanionReturnsResultUnchanged(t *testing.T) {
	want := json.RawMessage(`{"events":[]}`)

	got, err := callCompanionWithin(context.Background(), time.Minute,
		func(context.Context, companion.CallRequest) (json.RawMessage, error) {
			return want, nil
		}, testCalendarCall)
	if err != nil {
		t.Fatalf("callCompanion: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("result = %s, want %s", got, want)
	}
}

func TestCallCompanionUsesTheProductionBound(t *testing.T) {
	// The exported path must carry the real constant, or every test above
	// verifies a bound nothing in production uses.
	var deadline time.Time

	_, _ = callCompanion(context.Background(),
		func(callCtx context.Context, _ companion.CallRequest) (json.RawMessage, error) {
			deadline, _ = callCtx.Deadline()
			return json.RawMessage(`{}`), nil
		}, testCalendarCall)

	remaining := time.Until(deadline)
	if remaining <= companionCallTimeout-time.Second || remaining > companionCallTimeout {
		t.Fatalf("expected roughly %s of budget, got %s", companionCallTimeout, remaining)
	}
}
