package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestChannelProvider_SignalWithSenderName(t *testing.T) {
	p := NewChannelProvider()
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source":      "signal",
		"sender":      "+15551234567",
		"sender_name": "Nugget (David McNett)",
	})

	got, err := p.GetContext(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Signal") {
		t.Errorf("expected Signal mention, got %q", got)
	}
	if !strings.Contains(got, "Nugget (David McNett)") {
		t.Errorf("expected resolved sender name, got %q", got)
	}
	if !strings.Contains(got, "mobile") {
		t.Errorf("expected mobile mention, got %q", got)
	}
}

func TestChannelProvider_SignalFallbackToPhone(t *testing.T) {
	p := NewChannelProvider()
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source": "signal",
		"sender": "+15551234567",
	})

	got, err := p.GetContext(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "+15551234567") {
		t.Errorf("expected phone number fallback, got %q", got)
	}
	if !strings.Contains(got, "mobile") {
		t.Errorf("expected mobile mention, got %q", got)
	}
}

func TestChannelProvider_SignalNoSenderInfo(t *testing.T) {
	p := NewChannelProvider()
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source": "signal",
	})

	got, err := p.GetContext(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "unknown sender") {
		t.Errorf("expected 'unknown sender' fallback, got %q", got)
	}
}

func TestChannelProvider_UnknownSource(t *testing.T) {
	p := NewChannelProvider()
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source": "api",
	})

	got, err := p.GetContext(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty string for unknown source, got %q", got)
	}
}

func TestChannelProvider_NoHints(t *testing.T) {
	p := NewChannelProvider()

	got, err := p.GetContext(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty string without hints, got %q", got)
	}
}

func TestChannelProvider_NilHintsMap(t *testing.T) {
	p := NewChannelProvider()
	// WithHints with nil returns original context (no hints key set).
	ctx := tools.WithHints(context.Background(), nil)

	got, err := p.GetContext(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty string with nil hints, got %q", got)
	}
}
