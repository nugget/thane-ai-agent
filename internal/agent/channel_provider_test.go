package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// mockContactLookup implements ContactLookup for testing.
type mockContactLookup struct {
	contacts map[string]*ContactSummary
}

func (m *mockContactLookup) LookupContactByName(name string) *ContactSummary {
	if m == nil || m.contacts == nil {
		return nil
	}
	return m.contacts[name]
}

func TestChannelProvider_SignalKnownContact(t *testing.T) {
	lookup := &mockContactLookup{
		contacts: map[string]*ContactSummary{
			"Nugget": {
				Name:         "Nugget (David McNett)",
				Relationship: "owner",
				Summary:      "Night owl, prefers explicit explanations, 24h time format",
				Facts: map[string][]string{
					"timezone": {"America/Chicago"},
				},
			},
		},
	}
	p := NewChannelProvider(lookup)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source":      "signal",
		"sender":      "+15551234567",
		"sender_name": "Nugget",
	})

	got, err := p.GetContext(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		label string
		want  string
	}{
		{"header", "### Channel Context"},
		{"source", "Signal"},
		{"name", "Nugget (David McNett)"},
		{"relationship", "owner"},
		{"summary", "Night owl"},
		{"timezone", "America/Chicago"},
		{"channel note", "Terse input is normal"},
	}
	for _, tt := range tests {
		if !strings.Contains(got, tt.want) {
			t.Errorf("%s: expected %q in output:\n%s", tt.label, tt.want, got)
		}
	}
}

func TestChannelProvider_SignalUnknownContact(t *testing.T) {
	lookup := &mockContactLookup{contacts: map[string]*ContactSummary{}}
	p := NewChannelProvider(lookup)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source":      "signal",
		"sender":      "+15551234567",
		"sender_name": "Unknown Person",
	})

	got, err := p.GetContext(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got, "Unknown Person") {
		t.Errorf("expected sender name, got:\n%s", got)
	}
	if !strings.Contains(got, "unknown contact") {
		t.Errorf("expected 'unknown contact' marker, got:\n%s", got)
	}
	if !strings.Contains(got, "Signal") {
		t.Errorf("expected Signal source, got:\n%s", got)
	}
}

func TestChannelProvider_SignalFallbackToPhone(t *testing.T) {
	p := NewChannelProvider(nil)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source": "signal",
		"sender": "+15551234567",
	})

	got, err := p.GetContext(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "+15551234567") {
		t.Errorf("expected phone number fallback, got:\n%s", got)
	}
	if !strings.Contains(got, "unknown contact") {
		t.Errorf("expected 'unknown contact' marker, got:\n%s", got)
	}
}

func TestChannelProvider_SignalNoSenderInfo(t *testing.T) {
	p := NewChannelProvider(nil)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source": "signal",
	})

	got, err := p.GetContext(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "unknown sender") {
		t.Errorf("expected 'unknown sender' fallback, got:\n%s", got)
	}
}

func TestChannelProvider_NilContactLookup(t *testing.T) {
	p := NewChannelProvider(nil)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source":      "signal",
		"sender":      "+15551234567",
		"sender_name": "Nugget",
	})

	got, err := p.GetContext(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	// With nil lookup, should still produce output with the sender name
	// but marked as unknown contact.
	if !strings.Contains(got, "Nugget") {
		t.Errorf("expected sender name, got:\n%s", got)
	}
	if !strings.Contains(got, "unknown contact") {
		t.Errorf("expected 'unknown contact' marker, got:\n%s", got)
	}
}

func TestChannelProvider_ContactWithMultipleFacts(t *testing.T) {
	lookup := &mockContactLookup{
		contacts: map[string]*ContactSummary{
			"Alice": {
				Name:         "Alice",
				Relationship: "friend",
				Facts: map[string][]string{
					"email":    {"alice@example.com", "alice@work.com"},
					"timezone": {"Europe/London"},
				},
			},
		},
	}
	p := NewChannelProvider(lookup)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source":      "signal",
		"sender_name": "Alice",
	})

	got, err := p.GetContext(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "alice@example.com, alice@work.com") {
		t.Errorf("expected multiple email values, got:\n%s", got)
	}
	if !strings.Contains(got, "Europe/London") {
		t.Errorf("expected timezone fact, got:\n%s", got)
	}
}

func TestChannelProvider_ContactNoSummary(t *testing.T) {
	lookup := &mockContactLookup{
		contacts: map[string]*ContactSummary{
			"Bob": {
				Name: "Bob",
			},
		},
	}
	p := NewChannelProvider(lookup)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source":      "signal",
		"sender_name": "Bob",
	})

	got, err := p.GetContext(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Bob") {
		t.Errorf("expected name, got:\n%s", got)
	}
	if strings.Contains(got, "Context:") {
		t.Errorf("expected no Context line for empty summary, got:\n%s", got)
	}
}

func TestChannelProvider_UnknownSource(t *testing.T) {
	p := NewChannelProvider(nil)
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
	p := NewChannelProvider(nil)

	got, err := p.GetContext(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty string without hints, got %q", got)
	}
}

func TestChannelProvider_NilHintsMap(t *testing.T) {
	p := NewChannelProvider(nil)
	ctx := tools.WithHints(context.Background(), nil)

	got, err := p.GetContext(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty string with nil hints, got %q", got)
	}
}

func TestChannelProvider_EmptySource(t *testing.T) {
	p := NewChannelProvider(nil)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source": "",
	})

	got, err := p.GetContext(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty string for empty source, got %q", got)
	}
}

func TestSortedFactKeys(t *testing.T) {
	tests := []struct {
		name  string
		facts map[string][]string
		want  []string
	}{
		{"nil", nil, nil},
		{"empty", map[string][]string{}, nil},
		{"single", map[string][]string{"a": {"1"}}, []string{"a"}},
		{"sorted", map[string][]string{"a": {"1"}, "b": {"2"}, "c": {"3"}}, []string{"a", "b", "c"}},
		{"unsorted", map[string][]string{"z": {"1"}, "a": {"2"}, "m": {"3"}}, []string{"a", "m", "z"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sortedFactKeys(tt.facts)
			if len(got) != len(tt.want) {
				t.Fatalf("length mismatch: got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
