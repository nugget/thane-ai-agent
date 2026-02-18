package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestChannelProvider_SignalSource(t *testing.T) {
	p := NewChannelProvider()
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source": "signal",
	})

	got, err := p.GetContext(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Signal") {
		t.Errorf("expected Signal mention, got %q", got)
	}
	if !strings.Contains(got, "Nugget") {
		t.Errorf("expected Nugget mention, got %q", got)
	}
	if !strings.Contains(got, "mobile") {
		t.Errorf("expected mobile mention, got %q", got)
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
