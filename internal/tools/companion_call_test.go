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

// hangingCaller models the failure this bound exists for: a companion that
// is connected and never answers, the shape a Mac takes while it waits for
// someone to dismiss a permission prompt.
func hangingCaller(ctx context.Context, _ companion.CallRequest) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestCallCompanionBoundsAHangingProvider(t *testing.T) {
	// The real bound is 30s; drive it through an already-short parent so the
	// test proves the deadline is applied without waiting for one.
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := callCompanion(ctx, hangingCaller, companion.CallRequest{
		Capability: "macos.calendar",
		Method:     "list_events",
	})

	if err == nil {
		t.Fatal("expected a hanging companion to produce an error")
	}
	if elapsed := time.Since(start); elapsed > companionCallTimeout {
		t.Fatalf("call outlived the bound: %s", elapsed)
	}
}

func TestCallCompanionExplainsItsOwnDeadline(t *testing.T) {
	// A caller with no deadline of its own — an ordinary conversational
	// turn — is the case that previously hung outright.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deadlined := func(callCtx context.Context, _ companion.CallRequest) (json.RawMessage, error) {
		if _, ok := callCtx.Deadline(); !ok {
			t.Error("expected callCompanion to impose a deadline on a context that had none")
		}
		return nil, context.DeadlineExceeded
	}

	_, err := callCompanion(ctx, deadlined, companion.CallRequest{
		Capability: "macos.calendar",
		Method:     "list_events",
	})

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

func TestCallCompanionDoesNotClaimTheCallersDeadline(t *testing.T) {
	// When the turn's own context expired, blaming the companion would send
	// the model chasing the wrong fault.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := callCompanion(ctx, func(context.Context, companion.CallRequest) (json.RawMessage, error) {
		return nil, context.DeadlineExceeded
	}, companion.CallRequest{Capability: "macos.calendar", Method: "list_events"})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller's own deadline should pass through unchanged, got: %v", err)
	}
	if strings.Contains(err.Error(), "connected but not answering") {
		t.Errorf("should not blame the companion for the caller's deadline: %v", err)
	}
}

func TestCallCompanionPassesOtherErrorsThrough(t *testing.T) {
	sentinel := errors.New("no connected companion app supports macos.calendar/list_events")

	_, err := callCompanion(context.Background(), func(context.Context, companion.CallRequest) (json.RawMessage, error) {
		return nil, sentinel
	}, companion.CallRequest{Capability: "macos.calendar", Method: "list_events"})

	if !errors.Is(err, sentinel) {
		t.Fatalf("unrelated errors must pass through unchanged, got: %v", err)
	}
}

func TestCallCompanionReturnsResultUnchanged(t *testing.T) {
	want := json.RawMessage(`{"events":[]}`)

	got, err := callCompanion(context.Background(), func(context.Context, companion.CallRequest) (json.RawMessage, error) {
		return want, nil
	}, companion.CallRequest{Capability: "macos.calendar", Method: "list_events"})
	if err != nil {
		t.Fatalf("callCompanion: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("result = %s, want %s", got, want)
	}
}
