package awareness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// mockLoopSource returns a fixed set of loop snapshots.
type mockLoopSource struct {
	loops []LoopSnapshot
}

func (m *mockLoopSource) ChannelLoops() []LoopSnapshot { return m.loops }

// mockPhoneResolver maps phones to names.
type mockPhoneResolver struct {
	names map[string]string
}

func (m *mockPhoneResolver) ResolvePhone(phone string) (string, string, bool) {
	name, ok := m.names[phone]
	return name, "admin", ok
}

func TestChannelOverview_Empty(t *testing.T) {
	t.Parallel()
	p := NewChannelOverviewProvider(ChannelOverviewConfig{
		Loops: &mockLoopSource{},
	})
	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty string for no loops, got %q", got)
	}
}

func TestChannelOverview_SignalAndOWU(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 29, 15, 0, 0, 0, time.UTC)
	p := NewChannelOverviewProvider(ChannelOverviewConfig{
		Loops: &mockLoopSource{loops: []LoopSnapshot{
			{
				ID: "019d-signal-abc12345", Name: "signal/+15551234567",
				State: "waiting", LastWakeAt: now.Add(-2 * time.Minute),
				Metadata:      map[string]string{"subsystem": "signal", "category": "channel", "sender": "+15551234567", "trust_zone": "admin"},
				RecentConvIDs: []string{"signal-15551234567"},
			},
			{
				ID: "019d-owu-def67890", Name: "owu/home-automation",
				State: "sleeping", LastWakeAt: now.Add(-4 * time.Hour),
				Metadata: map[string]string{"subsystem": "owu", "category": "channel", "conversation_id": "owu-xyz"},
			},
		}},
		Phones: &mockPhoneResolver{names: map[string]string{"+15551234567": "nugget"}},
	})
	p.nowFunc = func() time.Time { return now }

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "### Channel Overview") {
		t.Error("missing section heading")
	}

	// Parse the JSON portion.
	jsonStr := strings.TrimPrefix(got, "### Channel Overview\n\n")
	jsonStr = strings.TrimSpace(jsonStr)
	var entries []channelEntry
	if err := json.Unmarshal([]byte(jsonStr), &entries); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, jsonStr)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Signal entry.
	sig := entries[0]
	if sig.Channel != "signal" {
		t.Errorf("entry[0].channel = %q, want signal", sig.Channel)
	}
	if sig.Contact != "nugget" {
		t.Errorf("entry[0].contact = %q, want nugget", sig.Contact)
	}
	if sig.Sender != "+15551234567" {
		t.Errorf("entry[0].sender = %q, want +15551234567", sig.Sender)
	}
	if sig.State != "waiting" {
		t.Errorf("entry[0].state = %q, want waiting", sig.State)
	}
	if sig.LastActivity != "-120s" {
		t.Errorf("entry[0].last_activity = %q, want -120s", sig.LastActivity)
	}
	if sig.ConvID != "signal-15551234567" {
		t.Errorf("entry[0].conv_id = %q, want signal-15551234567", sig.ConvID)
	}

	// OWU entry.
	owu := entries[1]
	if owu.Channel != "owu" {
		t.Errorf("entry[1].channel = %q, want owu", owu.Channel)
	}
	if owu.DisplayName != "home-automation" {
		t.Errorf("entry[1].display_name = %q, want home-automation", owu.DisplayName)
	}
	if owu.ConvID != "owu-xyz" {
		t.Errorf("entry[1].conv_id = %q, want owu-xyz", owu.ConvID)
	}
}

// testHintsKey is a test-specific context key used by the injected HintsFunc.
type testHintsKey struct{}

func TestChannelOverview_YouAreHere(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 29, 15, 0, 0, 0, time.UTC)

	// Test hints func that reads from a test-specific context key.
	testHints := map[string]string{
		"source":      "signal",
		"sender_name": "nugget",
	}
	hintsFunc := func(ctx context.Context) map[string]string {
		if h, ok := ctx.Value(testHintsKey{}).(map[string]string); ok {
			return h
		}
		return nil
	}

	p := NewChannelOverviewProvider(ChannelOverviewConfig{
		Loops: &mockLoopSource{loops: []LoopSnapshot{
			{
				ID: "019d-signal-abc", Name: "signal/+15551234567",
				State: "processing", LastWakeAt: now,
				Metadata:      map[string]string{"subsystem": "signal", "category": "channel", "sender": "+15551234567"},
				RecentConvIDs: []string{"signal-15551234567"},
			},
		}},
		Phones: &mockPhoneResolver{names: map[string]string{"+15551234567": "nugget"}},
		Hints:  hintsFunc,
	})
	p.nowFunc = func() time.Time { return now }

	ctx := context.WithValue(context.Background(), testHintsKey{}, testHints)

	got, err := p.GetContext(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	const header = "### Channel Overview\n\n"
	if !strings.HasPrefix(got, header) {
		t.Fatalf("output missing expected header, got: %q", got)
	}
	var entries []channelEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(got, header))), &entries); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if !entries[0].YouAreHere {
		t.Error("signal entry should be marked as you_are_here")
	}
}

func TestChannelOverview_NilPhoneResolver(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 29, 15, 0, 0, 0, time.UTC)
	p := NewChannelOverviewProvider(ChannelOverviewConfig{
		Loops: &mockLoopSource{loops: []LoopSnapshot{
			{
				ID: "019d-signal-abc", Name: "signal/+15559999999",
				State: "waiting", LastWakeAt: now.Add(-30 * time.Second),
				Metadata: map[string]string{"subsystem": "signal", "category": "channel", "sender": "+15559999999"},
			},
		}},
		// No phone resolver — contact should be empty.
	})
	p.nowFunc = func() time.Time { return now }

	got, err := p.GetContext(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	const header = "### Channel Overview\n\n"
	if !strings.HasPrefix(got, header) {
		t.Fatalf("output missing expected header, got: %q", got)
	}
	var entries []channelEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(got, header))), &entries); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Contact != "" {
		t.Errorf("contact should be empty without resolver, got %q", entries[0].Contact)
	}
	if entries[0].Sender != "+15559999999" {
		t.Errorf("sender should still be set: got %q", entries[0].Sender)
	}
}
