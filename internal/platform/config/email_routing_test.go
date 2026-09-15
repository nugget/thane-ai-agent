package config

import (
	"strings"
	"testing"
	"time"
)

// TestEmailRoutingDefaults pins where an account's mail goes when the
// routing keys are unset: an operator mailbox wakes the owner triage
// pass, every other account the default handler, no account has a
// review pass, and the review wake waits 15m within 2h.
func TestEmailRoutingDefaults(t *testing.T) {
	tests := []struct {
		name       string
		mailbox    EmailMailboxConfig
		wantWake   string
		wantReview string
		wantDelay  time.Duration
		wantMax    time.Duration
	}{
		{"assistant", EmailMailboxConfig{}, EmailWakeLoopDefaultHandler, "", 15 * time.Minute, 2 * time.Hour},
		{"operator", EmailMailboxConfig{Owner: EmailMailboxOwnerOperator}, EmailWakeLoopOwnerTriage, "", 15 * time.Minute, 2 * time.Hour},
		{"operator kept on the default handler", EmailMailboxConfig{Owner: EmailMailboxOwnerOperator, WakeLoop: EmailWakeLoopDefaultHandler}, EmailWakeLoopDefaultHandler, "", 15 * time.Minute, 2 * time.Hour},
		{"names are trimmed", EmailMailboxConfig{WakeLoop: "  inbox-sorter ", ReviewLoop: " email-draft-review "}, "inbox-sorter", EmailReviewLoopDraftReview, 15 * time.Minute, 2 * time.Hour},
		{"explicit timing", EmailMailboxConfig{ReviewLoop: "reviewer", ReviewDelay: time.Minute, ReviewMaxWait: time.Hour}, EmailWakeLoopDefaultHandler, "reviewer", time.Minute, time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acct := EmailAccountConfig{Name: "a", Mailbox: tt.mailbox}
			if got := acct.WakeLoopName(); got != tt.wantWake {
				t.Errorf("WakeLoopName() = %q, want %q", got, tt.wantWake)
			}
			if got := acct.ReviewLoopName(); got != tt.wantReview {
				t.Errorf("ReviewLoopName() = %q, want %q", got, tt.wantReview)
			}
			if got := acct.ReviewDelay(); got != tt.wantDelay {
				t.Errorf("ReviewDelay() = %s, want %s", got, tt.wantDelay)
			}
			if got := acct.ReviewMaxWait(); got != tt.wantMax {
				t.Errorf("ReviewMaxWait() = %s, want %s", got, tt.wantMax)
			}

			cfg := EmailConfig{Accounts: []EmailAccountConfig{{Name: "a", IMAP: EmailIMAPConfig{Host: "imap.example.com", Username: "alice@example.com"}, Mailbox: tt.mailbox}}}
			cfg.ApplyDefaults()
			got := cfg.Accounts[0].Mailbox
			if got.WakeLoop != tt.wantWake || got.ReviewLoop != tt.wantReview || got.ReviewDelay != tt.wantDelay || got.ReviewMaxWait != tt.wantMax {
				t.Errorf("ApplyDefaults mailbox = %+v", got)
			}
		})
	}
}

// TestEmailRoutingValidate pins the routing refusals config can make on
// its own; whether a named loop exists is checked at hydration.
func TestEmailRoutingValidate(t *testing.T) {
	imap := EmailIMAPConfig{Host: "imap.example.com", Port: 993, Username: "alice@example.com"}
	tests := []struct {
		name    string
		mailbox EmailMailboxConfig
		wantErr string
	}{
		{"defaults", EmailMailboxConfig{}, ""},
		{"review pass", EmailMailboxConfig{Owner: EmailMailboxOwnerOperator, ReviewLoop: EmailReviewLoopDraftReview}, ""},
		{"wait equal to delay", EmailMailboxConfig{ReviewDelay: time.Hour, ReviewMaxWait: time.Hour}, ""},
		{"negative delay", EmailMailboxConfig{ReviewDelay: -time.Minute}, "mailbox.review_delay -1m0s is negative"},
		{"negative wait", EmailMailboxConfig{ReviewMaxWait: -time.Minute}, "mailbox.review_max_wait -1m0s is negative"},
		{"wait shorter than delay", EmailMailboxConfig{ReviewDelay: time.Hour, ReviewMaxWait: time.Minute}, "is shorter than review_delay"},
		{"wait shorter than the default delay", EmailMailboxConfig{ReviewMaxWait: time.Minute}, "is shorter than review_delay 15m0s"},
		{"review loop is the wake loop", EmailMailboxConfig{WakeLoop: "triage", ReviewLoop: "triage"}, `mailbox.review_loop "triage" is also the account's wake_loop`},
		{"review loop is the default wake loop", EmailMailboxConfig{Owner: EmailMailboxOwnerOperator, ReviewLoop: EmailWakeLoopOwnerTriage}, "is also the account's wake_loop"},
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
