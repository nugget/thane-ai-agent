package email

import (
	"fmt"
	"testing"
)

// mockResolver implements ContactResolver for testing.
type mockResolver struct {
	zones map[string]string // email → trust zone
}

func (m *mockResolver) ResolveTrustZone(email string) (string, bool, error) {
	zone, ok := m.zones[email]
	if !ok {
		return "", false, nil
	}
	if zone == "error" {
		return "", false, fmt.Errorf("database error")
	}
	return zone, true, nil
}

func TestCheckRecipientTrust_NilResolver(t *testing.T) {
	result := CheckRecipientTrust(nil, []string{"a@example.com", "b@example.com"})

	if len(result.Allowed) != 2 {
		t.Errorf("nil resolver should allow all, got %d allowed", len(result.Allowed))
	}
	if result.HasIssues() {
		t.Error("nil resolver should not have issues")
	}
}

func TestCheckRecipientTrust(t *testing.T) {
	resolver := &mockResolver{
		zones: map[string]string{
			"owner@example.com":   "owner",
			"trusted@example.com": "trusted",
			"known@example.com":   "known",
		},
	}

	tests := []struct {
		name         string
		addresses    []string
		wantAllowed  int
		wantWarnings int
		wantBlocked  int
	}{
		{
			name:        "owner allowed",
			addresses:   []string{"owner@example.com"},
			wantAllowed: 1,
		},
		{
			name:        "trusted allowed",
			addresses:   []string{"trusted@example.com"},
			wantAllowed: 1,
		},
		{
			name:         "known warns",
			addresses:    []string{"known@example.com"},
			wantWarnings: 1,
		},
		{
			name:        "unknown blocked",
			addresses:   []string{"stranger@example.com"},
			wantBlocked: 1,
		},
		{
			name:         "mixed recipients",
			addresses:    []string{"owner@example.com", "known@example.com", "stranger@example.com"},
			wantAllowed:  1,
			wantWarnings: 1,
			wantBlocked:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckRecipientTrust(resolver, tt.addresses)

			if len(result.Allowed) != tt.wantAllowed {
				t.Errorf("Allowed = %d, want %d", len(result.Allowed), tt.wantAllowed)
			}
			if len(result.Warnings) != tt.wantWarnings {
				t.Errorf("Warnings = %d, want %d", len(result.Warnings), tt.wantWarnings)
			}
			if len(result.Blocked) != tt.wantBlocked {
				t.Errorf("Blocked = %d, want %d", len(result.Blocked), tt.wantBlocked)
			}
		})
	}
}

func TestCheckRecipientTrust_ExtractsAddress(t *testing.T) {
	resolver := &mockResolver{
		zones: map[string]string{
			"user@example.com": "trusted",
		},
	}

	// "Name <addr>" format should extract the bare address.
	result := CheckRecipientTrust(resolver, []string{"Alice <user@example.com>"})

	if len(result.Allowed) != 1 {
		t.Errorf("should extract and match bare address, got %d allowed", len(result.Allowed))
	}
}

func TestCheckRecipientTrust_Error(t *testing.T) {
	resolver := &mockResolver{
		zones: map[string]string{
			"error@example.com": "error",
		},
	}

	result := CheckRecipientTrust(resolver, []string{"error@example.com"})

	if len(result.Blocked) != 1 {
		t.Errorf("error should block, got %d blocked", len(result.Blocked))
	}
}

func TestTrustResult_HasIssues(t *testing.T) {
	clean := TrustResult{Allowed: []string{"a@test.com"}}
	if clean.HasIssues() {
		t.Error("clean result should not have issues")
	}

	warned := TrustResult{Warnings: []string{"warning"}}
	if !warned.HasIssues() {
		t.Error("warned result should have issues")
	}

	blocked := TrustResult{Blocked: []string{"blocked"}}
	if !blocked.HasIssues() {
		t.Error("blocked result should have issues")
	}
}

func TestTrustResult_FormatIssues(t *testing.T) {
	result := TrustResult{
		Warnings: []string{"warn message"},
		Blocked:  []string{"block message"},
	}

	formatted := result.FormatIssues()

	if formatted == "" {
		t.Error("FormatIssues should return non-empty string")
	}
}
