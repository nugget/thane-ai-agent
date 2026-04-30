package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// mockContactLookup implements ContactLookup for testing.
type mockContactLookup struct {
	contacts map[string]*ContactContext
	byID     map[string]*ContactContext
	policies map[string]*ContactOriginPolicy
}

func (m *mockContactLookup) LookupContact(name, _ string) *ContactContext {
	if m == nil || m.contacts == nil {
		return nil
	}
	return m.contacts[name]
}

func (m *mockContactLookup) LookupContactByID(id, _ string) *ContactContext {
	if m == nil || m.byID == nil {
		return nil
	}
	return m.byID[id]
}

func (m *mockContactLookup) LookupContactOriginPolicy(id, name, _ string) *ContactOriginPolicy {
	if m == nil || m.policies == nil {
		return nil
	}
	if id != "" {
		if policy := m.policies[id]; policy != nil {
			return policy
		}
	}
	return m.policies[name]
}

type parsedChannelContext struct {
	Source  string          `json:"source"`
	Note    string          `json:"note,omitempty"`
	Contact *ContactContext `json:"contact"`
}

// parseChannelJSON extracts the channel context JSON envelope from output.
func parseChannelJSON(t *testing.T, output string) parsedChannelContext {
	t.Helper()
	const header = "### Channel Context"
	if !strings.HasPrefix(output, header) {
		t.Fatalf("missing channel context header:\n%s", output)
	}
	jsonStr := strings.TrimSpace(strings.TrimPrefix(output, header))

	var envelope parsedChannelContext
	if err := json.Unmarshal([]byte(jsonStr), &envelope); err != nil {
		t.Fatalf("failed to parse JSON: %v\n%s", err, jsonStr)
	}
	return envelope
}

// parseContactJSON extracts the ContactContext from a channel context output string.
func parseContactJSON(t *testing.T, output string) *ContactContext {
	t.Helper()
	envelope := parseChannelJSON(t, output)
	if envelope.Contact == nil {
		t.Fatalf("contact missing from channel context:\n%s", output)
	}
	return envelope.Contact
}

func strPtr(s string) *string { return &s }

func TestChannelProvider_SignalKnownContact(t *testing.T) {
	lookup := &mockContactLookup{
		contacts: map[string]*ContactContext{
			"Nugget": {
				ID:        "test-uuid-1",
				Name:      "Nugget (David McNett)",
				GivenName: "David",
				TrustZone: "admin",
				TrustPolicy: &TrustPolicyView{
					FrontierModel:     true,
					ProactiveOutreach: "full",
					ToolAccess:        "unrestricted",
					SendGating:        "allowed",
				},
				Summary:      "Night owl, prefers explicit explanations, 24h time format",
				ContactSince: "2024-01-15",
				Channels: map[string]any{
					"signal": "+15551234567",
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

	got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	// Check markdown framing and JSON envelope.
	checks := []struct {
		label string
		want  string
	}{
		{"header", "### Channel Context"},
		{"channel note", "Terse input is normal"},
	}
	for _, tt := range checks {
		if !strings.Contains(got, tt.want) {
			t.Errorf("%s: expected %q in output:\n%s", tt.label, tt.want, got)
		}
	}
	for _, stale := range []string{"- **Source:**", "- **Note:**", "```json"} {
		if strings.Contains(got, stale) {
			t.Errorf("stale mixed-format marker %q in output:\n%s", stale, got)
		}
	}

	// Check JSON content.
	envelope := parseChannelJSON(t, got)
	if envelope.Source != "signal" {
		t.Errorf("source: got %q, want %q", envelope.Source, "signal")
	}
	if !strings.Contains(envelope.Note, "Terse input is normal") {
		t.Errorf("note: got %q", envelope.Note)
	}
	contact := envelope.Contact
	if contact.Name != "Nugget (David McNett)" {
		t.Errorf("name: got %q, want %q", contact.Name, "Nugget (David McNett)")
	}
	if contact.TrustZone != "admin" {
		t.Errorf("trust_zone: got %q, want %q", contact.TrustZone, "admin")
	}
	if contact.Summary != "Night owl, prefers explicit explanations, 24h time format" {
		t.Errorf("summary: got %q", contact.Summary)
	}
	if contact.TrustPolicy == nil || !contact.TrustPolicy.FrontierModel {
		t.Error("trust_policy.frontier_model should be true")
	}
}

func TestChannelProvider_SignalUnknownContact(t *testing.T) {
	lookup := &mockContactLookup{contacts: map[string]*ContactContext{}}
	p := NewChannelProvider(lookup)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source":      "signal",
		"sender":      "+15551234567",
		"sender_name": "Unknown Person",
	})

	got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	contact := parseContactJSON(t, got)
	if contact.Name != "Unknown Person" {
		t.Errorf("name: got %q, want %q", contact.Name, "Unknown Person")
	}
	if contact.TrustZone != "unknown" {
		t.Errorf("trust_zone: got %q, want %q", contact.TrustZone, "unknown")
	}
	if contact.TrustPolicy == nil || contact.TrustPolicy.SendGating != "blocked" {
		t.Error("unknown contact should have blocked send_gating")
	}
}

func TestChannelProvider_SignalFallbackToPhone(t *testing.T) {
	p := NewChannelProvider(nil)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source": "signal",
		"sender": "+15551234567",
	})

	got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	contact := parseContactJSON(t, got)
	if contact.Name != "+15551234567" {
		t.Errorf("name: got %q, want phone fallback", contact.Name)
	}
	if contact.TrustZone != "unknown" {
		t.Errorf("trust_zone: got %q, want %q", contact.TrustZone, "unknown")
	}
}

func TestChannelProvider_SignalNoSenderInfo(t *testing.T) {
	p := NewChannelProvider(nil)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source": "signal",
	})

	got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	contact := parseContactJSON(t, got)
	if contact.Name != "unknown sender" {
		t.Errorf("name: got %q, want %q", contact.Name, "unknown sender")
	}
}

func TestChannelProvider_UsesChannelBindingWhenHintsAreSparse(t *testing.T) {
	p := NewChannelProvider(nil)
	ctx := tools.WithChannelBinding(context.Background(), &memory.ChannelBinding{
		Channel:     "signal",
		Address:     "+15551234567",
		ContactID:   "contact-1",
		ContactName: "Alice Smith",
		TrustZone:   "known",
	})

	got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	contact := parseContactJSON(t, got)
	if contact.ID != "contact-1" {
		t.Fatalf("contact ID = %q, want contact-1", contact.ID)
	}
	if contact.Name != "Alice Smith" {
		t.Fatalf("name = %q, want Alice Smith", contact.Name)
	}
	if contact.TrustZone != "known" {
		t.Fatalf("trust_zone = %q, want known", contact.TrustZone)
	}
	if contact.TrustPolicy == nil || contact.TrustPolicy.SendGating == "" {
		t.Fatalf("trust_policy = %#v", contact.TrustPolicy)
	}
	if signal, ok := contact.Channels["signal"]; !ok || signal != "+15551234567" {
		t.Fatalf("channels = %#v", contact.Channels)
	}
}

func TestChannelProvider_NilContactLookup(t *testing.T) {
	p := NewChannelProvider(nil)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source":      "signal",
		"sender":      "+15551234567",
		"sender_name": "Nugget",
	})

	got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	contact := parseContactJSON(t, got)
	if contact.Name != "Nugget" {
		t.Errorf("name: got %q, want %q", contact.Name, "Nugget")
	}
	if contact.TrustZone != "unknown" {
		t.Errorf("trust_zone: got %q, want %q", contact.TrustZone, "unknown")
	}
}

func TestChannelProvider_AdminFullContext(t *testing.T) {
	lookup := &mockContactLookup{
		contacts: map[string]*ContactContext{
			"Admin": {
				ID:         "uuid-admin",
				Name:       "Admin User",
				GivenName:  "Admin",
				FamilyName: "User",
				TrustZone:  "admin",
				TrustPolicy: &TrustPolicyView{
					FrontierModel:     true,
					ProactiveOutreach: "full",
					ToolAccess:        "unrestricted",
					SendGating:        "allowed",
				},
				Summary:      "System administrator",
				Org:          strPtr("Acme Corp"),
				Title:        strPtr("CTO"),
				Role:         strPtr("Operations"),
				Groups:       []string{"family", "ops-team"},
				ContactSince: "2023-06-01",
				Related:      []RelatedContact{{Name: "Spouse", Type: "spouse"}},
				Channels: map[string]any{
					"signal": "+15551234567",
					"email":  []string{"admin@example.com"},
				},
				LastInteraction: &InteractionRef{
					Ago:       "-3600s",
					Channel:   "signal",
					SessionID: "sess-abc",
					Topics:    []string{"HVAC", "cameras"},
				},
			},
		},
	}
	p := NewChannelProvider(lookup)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source":      "signal",
		"sender_name": "Admin",
	})

	got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	contact := parseContactJSON(t, got)
	if contact.GivenName != "Admin" {
		t.Errorf("given_name: got %q", contact.GivenName)
	}
	if contact.FamilyName != "User" {
		t.Errorf("family_name: got %q", contact.FamilyName)
	}
	if contact.Org == nil || *contact.Org != "Acme Corp" {
		t.Errorf("org: got %v", contact.Org)
	}
	if contact.Title == nil || *contact.Title != "CTO" {
		t.Errorf("title: got %v", contact.Title)
	}
	if contact.Role == nil || *contact.Role != "Operations" {
		t.Errorf("role: got %v", contact.Role)
	}
	if len(contact.Groups) != 2 || contact.Groups[0] != "family" {
		t.Errorf("groups: got %v", contact.Groups)
	}
	if len(contact.Related) != 1 || contact.Related[0].Name != "Spouse" {
		t.Errorf("related: got %v", contact.Related)
	}
	if contact.LastInteraction == nil {
		t.Fatal("expected last_interaction")
	}
	if contact.LastInteraction.Ago != "-3600s" {
		t.Errorf("ago: got %q", contact.LastInteraction.Ago)
	}
	if contact.LastInteraction.Channel != "signal" {
		t.Errorf("interaction channel: got %q", contact.LastInteraction.Channel)
	}
}

func TestChannelProvider_KnownZoneMinimalFields(t *testing.T) {
	lookup := &mockContactLookup{
		contacts: map[string]*ContactContext{
			"Known": {
				ID:        "uuid-known",
				Name:      "Known Person",
				TrustZone: "known",
				TrustPolicy: &TrustPolicyView{
					FrontierModel:     false,
					ProactiveOutreach: "none",
					ToolAccess:        "readonly",
					SendGating:        "blocked",
				},
				ContactSince: "2025-03-01",
				Channels: map[string]any{
					"signal": "+15559876543",
				},
			},
		},
	}
	p := NewChannelProvider(lookup)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source":      "signal",
		"sender_name": "Known",
	})

	got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	contact := parseContactJSON(t, got)
	if contact.Name != "Known Person" {
		t.Errorf("name: got %q", contact.Name)
	}
	if contact.TrustZone != "known" {
		t.Errorf("trust_zone: got %q", contact.TrustZone)
	}
	if contact.TrustPolicy == nil || contact.TrustPolicy.FrontierModel {
		t.Error("known zone should not have frontier model access")
	}
	// Known zone should NOT have summary, org, title, role, groups, related.
	if contact.Summary != "" {
		t.Errorf("known zone should not include summary, got %q", contact.Summary)
	}
	if contact.GivenName != "" {
		t.Errorf("known zone should not include given_name, got %q", contact.GivenName)
	}
}

func TestChannelProvider_NullOrgTitleRole(t *testing.T) {
	lookup := &mockContactLookup{
		contacts: map[string]*ContactContext{
			"NullFields": {
				ID:        "uuid-nullfields",
				Name:      "NullFields Person",
				TrustZone: "trusted",
				TrustPolicy: &TrustPolicyView{
					FrontierModel:     true,
					ProactiveOutreach: "limited",
					ToolAccess:        "safe",
					SendGating:        "confirmation",
				},
			},
		},
	}
	p := NewChannelProvider(lookup)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source":      "signal",
		"sender_name": "NullFields",
	})

	got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	// With omitempty, nil *string fields are omitted entirely rather
	// than serialized as null. Verify org/title/role are absent.
	contact := parseContactJSON(t, got)
	if contact.Org != nil {
		t.Errorf("expected nil org, got %q", *contact.Org)
	}
	if contact.Title != nil {
		t.Errorf("expected nil title, got %q", *contact.Title)
	}
	if contact.Role != nil {
		t.Errorf("expected nil role, got %q", *contact.Role)
	}
	// Also verify they don't appear in the raw JSON.
	if strings.Contains(got, `"org"`) {
		t.Errorf("org should be omitted from JSON:\n%s", got)
	}
}

func TestChannelProvider_UnknownSource(t *testing.T) {
	p := NewChannelProvider(nil)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source": "api",
	})

	got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty string for unknown source, got %q", got)
	}
}

func TestChannelProvider_NoHints(t *testing.T) {
	p := NewChannelProvider(nil)

	got, err := p.TagContext(context.Background(), agentctx.ContextRequest{UserMessage: "hello"})
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

	got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: "hello"})
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

	got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected empty string for empty source, got %q", got)
	}
}

func TestChannelProvider_JSONStructure(t *testing.T) {
	lookup := &mockContactLookup{
		contacts: map[string]*ContactContext{
			"Structured": {
				ID:        "uuid-struct",
				Name:      "Structured Contact",
				TrustZone: "household",
				TrustPolicy: &TrustPolicyView{
					FrontierModel:     true,
					ProactiveOutreach: "full",
					ToolAccess:        "most",
					SendGating:        "allowed",
				},
				Channels: map[string]any{
					"email":  []string{"a@b.com", "c@d.com"},
					"signal": "+15550001234",
				},
				Groups: []string{"family"},
			},
		},
	}
	p := NewChannelProvider(lookup)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source":      "signal",
		"sender_name": "Structured",
	})

	got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	contact := parseContactJSON(t, got)
	if contact.TrustZone != "household" {
		t.Errorf("trust_zone: got %q", contact.TrustZone)
	}
	if contact.TrustPolicy == nil || contact.TrustPolicy.ToolAccess != "most" {
		t.Error("trust_policy.tool_access should be 'most'")
	}
	if len(contact.Groups) != 1 || contact.Groups[0] != "family" {
		t.Errorf("groups: got %v", contact.Groups)
	}
}

func TestChannelProvider_JSONFallbackDropsUnmarshalableChannels(t *testing.T) {
	lookup := &mockContactLookup{
		contacts: map[string]*ContactContext{
			"BadChannel": {
				Name:      "Bad Channel",
				TrustZone: "known",
				TrustPolicy: &TrustPolicyView{
					ToolAccess: "readonly",
					SendGating: "blocked",
				},
				Channels: map[string]any{
					"signal": func() {},
				},
			},
		},
	}
	p := NewChannelProvider(lookup)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source":      "signal",
		"sender_name": "BadChannel",
	})

	got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "[json-marshal-error") {
		t.Fatalf("channel context should recover to JSON envelope, got:\n%s", got)
	}
	contact := parseContactJSON(t, got)
	if contact.Name != "Bad Channel" {
		t.Fatalf("name = %q, want Bad Channel", contact.Name)
	}
	if contact.Channels != nil {
		t.Fatalf("channels = %#v, want dropped fallback channels", contact.Channels)
	}
}

func TestChannelProvider_InteractionRef(t *testing.T) {
	lookup := &mockContactLookup{
		contacts: map[string]*ContactContext{
			"Recent": {
				ID:        "uuid-recent",
				Name:      "Recent Contact",
				TrustZone: "trusted",
				TrustPolicy: &TrustPolicyView{
					FrontierModel:     true,
					ProactiveOutreach: "limited",
					ToolAccess:        "safe",
					SendGating:        "confirmation",
				},
				LastInteraction: &InteractionRef{
					Ago:       "-7200s",
					Channel:   "signal",
					SessionID: "sess-xyz",
					Topics:    []string{"weather", "schedule"},
				},
			},
		},
	}
	p := NewChannelProvider(lookup)
	ctx := tools.WithHints(context.Background(), map[string]string{
		"source":      "signal",
		"sender_name": "Recent",
	})

	got, err := p.TagContext(ctx, agentctx.ContextRequest{UserMessage: "hello"})
	if err != nil {
		t.Fatal(err)
	}

	contact := parseContactJSON(t, got)
	if contact.LastInteraction == nil {
		t.Fatal("expected last_interaction")
	}
	if contact.LastInteraction.Ago != "-7200s" {
		t.Errorf("ago: got %q, want -7200s", contact.LastInteraction.Ago)
	}
	if len(contact.LastInteraction.Topics) != 2 {
		t.Errorf("topics: got %v", contact.LastInteraction.Topics)
	}
}
