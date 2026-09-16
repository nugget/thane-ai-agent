package config

import (
	"strings"
	"testing"
)

// TestEmailDraftGateDefaults pins the effective gate: relaxed with
// delivery drafts, strict otherwise, and relaxed in effect only on an
// account that can write mail and only ever drafts.
func TestEmailDraftGateDefaults(t *testing.T) {
	tests := []struct {
		name        string
		access      string
		delivery    string
		gate        string
		wantMode    string
		wantRelaxed bool
	}{
		{"drafts delivery defaults to relaxed", EmailAccessSend, EmailDeliveryDrafts, "", EmailDraftGateRelaxed, true},
		{"strict drafts", EmailAccessSend, EmailDeliveryDrafts, EmailDraftGateStrict, EmailDraftGateStrict, false},
		{"unset delivery defaults to strict", EmailAccessSend, "", "", EmailDraftGateStrict, false},
		{"by_trust_zone defaults to strict", EmailAccessSend, EmailDeliveryByTrustZone, "", EmailDraftGateStrict, false},
		{"direct defaults to strict", EmailAccessSend, EmailDeliveryDirect, "", EmailDraftGateStrict, false},
		{"relaxed on an account that cannot write mail relaxes nothing", EmailAccessOrganize, EmailDeliveryDrafts, "", EmailDraftGateRelaxed, false},
		{"relaxed written beside direct relaxes nothing", EmailAccessSend, EmailDeliveryDirect, EmailDraftGateRelaxed, EmailDraftGateRelaxed, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acct := EmailAccountConfig{
				Name:        "a",
				IMAP:        EmailIMAPConfig{Host: "imap.example.com", Username: "alice@example.com"},
				SMTP:        EmailSMTPConfig{Host: "smtp.example.com", Username: "alice", Password: "pw"},
				DefaultFrom: "alice@example.com",
				Policy:      EmailPolicyConfig{Access: tt.access, Delivery: tt.delivery, DraftGate: tt.gate},
			}
			if got := acct.DraftGateMode(); got != tt.wantMode {
				t.Errorf("DraftGateMode() = %q, want %q", got, tt.wantMode)
			}
			if got := acct.RelaxedDraftGate(); got != tt.wantRelaxed {
				t.Errorf("RelaxedDraftGate() = %v, want %v", got, tt.wantRelaxed)
			}
			cfg := EmailConfig{Accounts: []EmailAccountConfig{acct}}
			cfg.ApplyDefaults()
			if got := cfg.Accounts[0].Policy.DraftGate; got != tt.wantMode {
				t.Errorf("ApplyDefaults policy.draft_gate = %q, want %q", got, tt.wantMode)
			}
		})
	}
}

// TestEmailDraftGateValidate pins the refusals: relaxed beside any
// delivery mode that can send, and a value outside the vocabulary.
func TestEmailDraftGateValidate(t *testing.T) {
	tests := []struct {
		name     string
		delivery string
		gate     string
		wantErr  string
	}{
		{"relaxed with drafts", EmailDeliveryDrafts, EmailDraftGateRelaxed, ""},
		{"default with drafts", EmailDeliveryDrafts, "", ""},
		{"strict with drafts", EmailDeliveryDrafts, EmailDraftGateStrict, ""},
		{"strict with by_trust_zone", EmailDeliveryByTrustZone, EmailDraftGateStrict, ""},
		{"strict with direct", EmailDeliveryDirect, EmailDraftGateStrict, ""},
		{"relaxed with by_trust_zone", EmailDeliveryByTrustZone, EmailDraftGateRelaxed, "policy.draft_gate relaxed needs policy.delivery drafts"},
		{"relaxed with direct", EmailDeliveryDirect, EmailDraftGateRelaxed, "with delivery direct"},
		{"relaxed with the default delivery", "", EmailDraftGateRelaxed, "with delivery by_trust_zone"},
		{"unknown gate", EmailDeliveryDrafts, "loose", `policy.draft_gate "loose" is not one of relaxed, strict`},
		{"gate is case-sensitive", EmailDeliveryDrafts, "Relaxed", `policy.draft_gate "Relaxed"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := EmailConfig{Accounts: []EmailAccountConfig{{
				Name:        "personal",
				IMAP:        EmailIMAPConfig{Host: "imap.example.com", Port: 993, Username: "alice@example.com"},
				SMTP:        EmailSMTPConfig{Host: "smtp.example.com", Port: 587, Username: "alice", Password: "pw"},
				DefaultFrom: "alice@example.com",
				Policy:      EmailPolicyConfig{Delivery: tt.delivery, DraftGate: tt.gate},
			}}}
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				cfg.ApplyDefaults()
				if err := cfg.Validate(); err != nil {
					t.Fatalf("Validate() after ApplyDefaults = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "email.accounts[0] (personal)") {
				t.Fatalf("Validate() = %v, want an error naming the account and containing %q", err, tt.wantErr)
			}
		})
	}
}
