package config

import (
	"strings"
	"testing"
)

// TestEmailMailboxDefaults pins what an account without a mailbox block
// is: the assistant's, with reads that mark mail seen; and what an
// operator mailbox changes.
func TestEmailMailboxDefaults(t *testing.T) {
	tests := []struct {
		name          string
		owner         string
		wantOwner     string
		wantOperator  bool
		wantMarksSeen bool
	}{
		{"unset is the assistant's", "", EmailMailboxOwnerAssistant, false, true},
		{"assistant", EmailMailboxOwnerAssistant, EmailMailboxOwnerAssistant, false, true},
		{"operator", EmailMailboxOwnerOperator, EmailMailboxOwnerOperator, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acct := EmailAccountConfig{Name: "a", Mailbox: EmailMailboxConfig{Owner: tt.owner}}
			if got := acct.MailboxOwner(); got != tt.wantOwner {
				t.Errorf("MailboxOwner() = %q, want %q", got, tt.wantOwner)
			}
			if got := acct.OperatorMailbox(); got != tt.wantOperator {
				t.Errorf("OperatorMailbox() = %v, want %v", got, tt.wantOperator)
			}
			if got := acct.ReadMarksSeenByDefault(); got != tt.wantMarksSeen {
				t.Errorf("ReadMarksSeenByDefault() = %v, want %v", got, tt.wantMarksSeen)
			}

			cfg := EmailConfig{Accounts: []EmailAccountConfig{{Name: "a", IMAP: EmailIMAPConfig{Host: "imap.example.com", Username: "alice@example.com"}, Mailbox: EmailMailboxConfig{Owner: tt.owner}}}}
			cfg.ApplyDefaults()
			if got := cfg.Accounts[0].Mailbox.Owner; got != tt.wantOwner {
				t.Errorf("ApplyDefaults mailbox.owner = %q, want %q", got, tt.wantOwner)
			}
		})
	}
}

// TestEmailMailboxValidate pins the two refusals: an owner outside the
// vocabulary, and a voice over its byte budget.
func TestEmailMailboxValidate(t *testing.T) {
	imap := EmailIMAPConfig{Host: "imap.example.com", Port: 993, Username: "alice@example.com"}
	tests := []struct {
		name    string
		mailbox EmailMailboxConfig
		wantErr string
	}{
		{"empty block", EmailMailboxConfig{}, ""},
		{"assistant", EmailMailboxConfig{Owner: EmailMailboxOwnerAssistant}, ""},
		{"operator with voice", EmailMailboxConfig{Owner: EmailMailboxOwnerOperator, Voice: "first person as Alice; brief"}, ""},
		{"owner is case-sensitive", EmailMailboxConfig{Owner: "Operator"}, `mailbox.owner "Operator" is not one of assistant, operator`},
		{"unknown owner", EmailMailboxConfig{Owner: "household"}, `mailbox.owner "household"`},
		{"voice at the limit", EmailMailboxConfig{Voice: strings.Repeat("a", maxEmailMailboxVoiceBytes)}, ""},
		{"voice over the limit", EmailMailboxConfig{Voice: strings.Repeat("a", maxEmailMailboxVoiceBytes+1)}, "mailbox.voice is 501 bytes, over the 500-byte limit"},
		{"voice counts bytes, not runes", EmailMailboxConfig{Voice: strings.Repeat("é", 251)}, "mailbox.voice is 502 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := EmailConfig{Accounts: []EmailAccountConfig{{Name: "personal", IMAP: imap, Mailbox: tt.mailbox}}}
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), "email.accounts[0] (personal)") {
				t.Fatalf("Validate() = %v, want an error naming the account and containing %q", err, tt.wantErr)
			}
		})
	}
}
