package config

import (
	"strings"
	"testing"
	"time"
)

func boolPtr(v bool) *bool { return &v }
func intPtr(v int) *int    { return &v }

func TestEmailConfig_Configured(t *testing.T) {
	tests := []struct {
		name string
		cfg  EmailConfig
		want bool
	}{
		{"no accounts", EmailConfig{}, false},
		{"empty account", EmailConfig{Accounts: []EmailAccountConfig{{Name: "test"}}}, false},
		{"host only", EmailConfig{Accounts: []EmailAccountConfig{{Name: "test", IMAP: EmailIMAPConfig{Host: "imap.example.com"}}}}, false},
		{"host and username", EmailConfig{Accounts: []EmailAccountConfig{{
			Name: "test",
			IMAP: EmailIMAPConfig{Host: "imap.example.com", Username: "alice@example.com"},
		}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Configured(); got != tt.want {
				t.Errorf("Configured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEmailConfig_ApplyDefaults(t *testing.T) {
	cfg := EmailConfig{Accounts: []EmailAccountConfig{
		{Name: "test", IMAP: EmailIMAPConfig{Host: "imap.example.com", Username: "alice"}},
	}}
	cfg.ApplyDefaults()

	acct := cfg.Accounts[0]
	if acct.IMAP.Port != 993 {
		t.Errorf("default port = %d, want 993", acct.IMAP.Port)
	}
	if acct.IMAP.TLS == nil || !*acct.IMAP.TLS || !acct.IMAP.TLSEnabled() {
		t.Error("default TLS should resolve to true")
	}
	if cfg.PollInterval == nil || *cfg.PollInterval != 300 {
		t.Errorf("default poll interval = %v, want 300", cfg.PollInterval)
	}
	if cfg.PollingInterval() != 300*time.Second {
		t.Errorf("PollingInterval() = %v, want 5m", cfg.PollingInterval())
	}
}

func TestEmailConfig_PollInterval(t *testing.T) {
	configured := []EmailAccountConfig{{Name: "test", IMAP: EmailIMAPConfig{Host: "imap.example.com", Username: "alice"}}}
	tests := []struct {
		name string
		cfg  EmailConfig
		want time.Duration
	}{
		{"explicit value is kept", EmailConfig{PollInterval: intPtr(60), Accounts: configured}, 60 * time.Second},
		{"explicit zero disables", EmailConfig{PollInterval: intPtr(0), Accounts: configured}, 0},
		{"unset defaults when configured", EmailConfig{Accounts: configured}, 300 * time.Second},
		{"unconfigured never polls", EmailConfig{}, 0},
		{"unconfigured with value never polls", EmailConfig{PollInterval: intPtr(60)}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			cfg.ApplyDefaults()
			if got := cfg.PollingInterval(); got != tt.want {
				t.Errorf("PollingInterval() = %v, want %v", got, tt.want)
			}
		})
	}
	// ApplyDefaults must not resurrect an explicit zero.
	cfg := EmailConfig{PollInterval: intPtr(0), Accounts: configured}
	cfg.ApplyDefaults()
	if *cfg.PollInterval != 0 {
		t.Errorf("explicit 0 became %d", *cfg.PollInterval)
	}
	// Unconfigured email leaves the field alone so the example does not
	// render a default for a subsystem that is off.
	off := EmailConfig{}
	off.ApplyDefaults()
	if off.PollInterval != nil {
		t.Errorf("unconfigured email should not default poll_interval, got %d", *off.PollInterval)
	}
}

func TestEmailIMAPConfig_TLSByPort(t *testing.T) {
	tests := []struct {
		name string
		cfg  EmailIMAPConfig
		want bool
	}{
		{"993 defaults on", EmailIMAPConfig{Port: 993}, true},
		{"143 defaults off", EmailIMAPConfig{Port: 143}, false},
		{"explicit false on 993 is honored", EmailIMAPConfig{Port: 993, TLS: boolPtr(false)}, false},
		{"explicit true on 143 is honored", EmailIMAPConfig{Port: 143, TLS: boolPtr(true)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.TLSEnabled(); got != tt.want {
				t.Errorf("TLSEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEmailSMTPConfig_StartTLSByPort(t *testing.T) {
	tests := []struct {
		name string
		cfg  EmailSMTPConfig
		want bool
	}{
		{"587 defaults to STARTTLS", EmailSMTPConfig{Port: 587}, true},
		{"465 defaults to implicit TLS", EmailSMTPConfig{Port: 465}, false},
		{"explicit false on 587 is honored", EmailSMTPConfig{Port: 587, StartTLS: boolPtr(false)}, false},
		{"explicit true on 465 is honored", EmailSMTPConfig{Port: 465, StartTLS: boolPtr(true)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.StartTLSEnabled(); got != tt.want {
				t.Errorf("StartTLSEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEmailConfig_ApplyDefaults_SMTP(t *testing.T) {
	cfg := EmailConfig{Accounts: []EmailAccountConfig{{
		Name: "test",
		IMAP: EmailIMAPConfig{Host: "imap.example.com", Username: "alice"},
		SMTP: EmailSMTPConfig{Host: "smtp.example.com", Username: "alice"},
	}}}
	cfg.ApplyDefaults()
	acct := cfg.Accounts[0]
	if acct.SMTP.Port != 587 {
		t.Errorf("default SMTP port = %d, want 587", acct.SMTP.Port)
	}
	if acct.SMTP.StartTLS == nil || !*acct.SMTP.StartTLS {
		t.Error("default SMTP StartTLS should resolve to true")
	}

	implicit := EmailConfig{Accounts: []EmailAccountConfig{{
		Name: "test",
		IMAP: EmailIMAPConfig{Host: "imap.example.com", Username: "alice"},
		SMTP: EmailSMTPConfig{Host: "smtp.example.com", Username: "alice", Port: 465},
	}}}
	implicit.ApplyDefaults()
	if *implicit.Accounts[0].SMTP.StartTLS {
		t.Error("SMTP StartTLS should default to false for port 465 (implicit TLS)")
	}

	none := EmailConfig{Accounts: []EmailAccountConfig{{
		Name: "test",
		IMAP: EmailIMAPConfig{Host: "imap.example.com", Username: "alice"},
	}}}
	none.ApplyDefaults()
	if none.Accounts[0].SMTP.Port != 0 || none.Accounts[0].SMTP.StartTLS != nil {
		t.Error("SMTP defaults must not be applied when smtp.host is empty")
	}
}

func TestEmailConfig_Validate(t *testing.T) {
	imap := EmailIMAPConfig{Host: "imap.example.com", Port: 993, Username: "alice@example.com"}
	smtp := EmailSMTPConfig{Host: "smtp.example.com", Port: 587, Username: "alice", Password: "pass"}
	tests := []struct {
		name    string
		cfg     EmailConfig
		wantErr string
	}{
		{"valid", EmailConfig{Accounts: []EmailAccountConfig{{Name: "personal", IMAP: imap}}}, ""},
		{"missing name", EmailConfig{Accounts: []EmailAccountConfig{{IMAP: imap}}}, "email.accounts[0].name"},
		{"duplicate names", EmailConfig{Accounts: []EmailAccountConfig{{Name: "work", IMAP: imap}, {Name: "work", IMAP: imap}}}, "duplicate"},
		{"missing host", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: EmailIMAPConfig{Port: 993, Username: "u"}}}}, "imap.host"},
		{"missing username", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: EmailIMAPConfig{Host: "h", Port: 993}}}}, "imap.username"},
		{"invalid port", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: EmailIMAPConfig{Host: "h", Port: 0, Username: "u"}}}}, "imap.port"},
		{"valid with smtp", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap, SMTP: smtp, DefaultFrom: "Alice <alice@example.com>"}}}, ""},
		{"smtp missing password", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap, SMTP: EmailSMTPConfig{Host: "h", Port: 587, Username: "u"}, DefaultFrom: "alice@example.com"}}}, "smtp.password"},
		{"smtp missing username", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap, SMTP: EmailSMTPConfig{Host: "h", Port: 587}, DefaultFrom: "alice@example.com"}}}, "smtp.username"},
		{"smtp missing default_from", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap, SMTP: smtp}}}, "default_from is required"},
		{"smtp invalid port", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap, SMTP: EmailSMTPConfig{Host: "h", Port: 0, Username: "u", Password: "p"}, DefaultFrom: "alice@example.com"}}}, "smtp.port"},
		{"default_from must parse", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap, SMTP: smtp, DefaultFrom: "not an address"}}}, "not a valid email address"},
		{"bcc_owner must parse", EmailConfig{BccOwner: "owner", Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap}}}, "bcc_owner"},
		{"bcc_owner with display name is fine", EmailConfig{BccOwner: "Owner <owner@example.com>", Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap}}}, ""},
		{"negative poll interval", EmailConfig{PollInterval: intPtr(-1), Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap}}}, "poll_interval"},
		{"zero poll interval disables", EmailConfig{PollInterval: intPtr(0), Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap}}}, ""},
		{"unknown access", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap, Policy: EmailPolicyConfig{Access: "write"}}}}, "policy.access"},
		{"unknown delivery", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap, SMTP: smtp, DefaultFrom: "alice@example.com", Policy: EmailPolicyConfig{Delivery: "queue"}}}}, "policy.delivery"},
		{"send without smtp must draft", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap, DefaultFrom: "alice@example.com", Policy: EmailPolicyConfig{Access: EmailAccessSend}}}}, "can only draft"},
		{"send without smtp drafting needs a from", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap, Policy: EmailPolicyConfig{Access: EmailAccessSend, Delivery: EmailDeliveryDrafts}}}}, "default_from is required when policy.access"},
		{"send without smtp drafting is fine", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap, DefaultFrom: "alice@example.com", Policy: EmailPolicyConfig{Access: EmailAccessSend, Delivery: EmailDeliveryDrafts}}}}, ""},
		{"organize with smtp is fine", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap, SMTP: smtp, DefaultFrom: "alice@example.com", Policy: EmailPolicyConfig{Access: EmailAccessOrganize}}}}, ""},
		{"domain entries are domains", EmailConfig{Accounts: []EmailAccountConfig{{Name: "t", IMAP: imap, Policy: EmailPolicyConfig{DeniedRecipientDomains: []string{"alice@example.com"}}}}}, "not a domain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

// TestEmailAccountConfig_PolicyDefaults pins the documented defaults:
// send when smtp is configured, organize otherwise, by_trust_zone
// delivery, and normalized domain lists.
func TestEmailAccountConfig_PolicyDefaults(t *testing.T) {
	withSMTP := EmailAccountConfig{Name: "a", IMAP: EmailIMAPConfig{Host: "h", Username: "u"}, SMTP: EmailSMTPConfig{Host: "s", Username: "u"}}
	if withSMTP.AccessLevel() != EmailAccessSend || !withSMTP.CanDraft() || !withSMTP.CanDeliver() || withSMTP.DeliveryMode() != EmailDeliveryByTrustZone {
		t.Errorf("smtp account defaults: access=%s draft=%v deliver=%v delivery=%s", withSMTP.AccessLevel(), withSMTP.CanDraft(), withSMTP.CanDeliver(), withSMTP.DeliveryMode())
	}
	readOnly := EmailAccountConfig{Name: "b", IMAP: EmailIMAPConfig{Host: "h", Username: "u"}}
	if readOnly.AccessLevel() != EmailAccessOrganize || readOnly.CanDraft() || readOnly.CanDeliver() {
		t.Errorf("imap-only account defaults: access=%s draft=%v deliver=%v", readOnly.AccessLevel(), readOnly.CanDraft(), readOnly.CanDeliver())
	}
	held := withSMTP
	held.Policy.Access = EmailAccessOrganize
	if held.CanDraft() || held.CanDeliver() {
		t.Error("an explicit organize must win over configured smtp")
	}

	cfg := EmailConfig{Accounts: []EmailAccountConfig{{Name: "a", IMAP: EmailIMAPConfig{Host: "h", Username: "u"}, Policy: EmailPolicyConfig{DeniedRecipientDomains: []string{" .Example.COM ", ""}}}}}
	cfg.ApplyDefaults()
	got := cfg.Accounts[0].Policy
	if got.Access != EmailAccessOrganize || got.Delivery != EmailDeliveryByTrustZone || len(got.DeniedRecipientDomains) != 1 || got.DeniedRecipientDomains[0] != "example.com" || got.AllowedRecipientDomains != nil {
		t.Errorf("ApplyDefaults policy = %+v", got)
	}
}

func TestEmailAccountConfig_SMTPConfigured(t *testing.T) {
	tests := []struct {
		name string
		acct EmailAccountConfig
		want bool
	}{
		{"no smtp", EmailAccountConfig{}, false},
		{"host only", EmailAccountConfig{SMTP: EmailSMTPConfig{Host: "smtp.example.com"}}, false},
		{"host and username", EmailAccountConfig{SMTP: EmailSMTPConfig{Host: "smtp.example.com", Username: "alice"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.acct.SMTPConfigured(); got != tt.want {
				t.Errorf("SMTPConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestExampleEmailConfigMatchesDefaults guards the generated example:
// the values an operator copies from config.example.yaml must be the
// values ApplyDefaults would have chosen for the same ports, so the
// example never documents behavior the binary does not have (the
// example once rendered starttls: false beside a 587 default of true).
func TestExampleEmailConfigMatchesDefaults(t *testing.T) {
	example := ExampleConfig().Email
	if len(example.Accounts) < 2 {
		t.Fatalf("example email config should show a sending account and a read-only account, got %d accounts", len(example.Accounts))
	}
	for _, acct := range example.Accounts {
		fresh := EmailAccountConfig{Name: acct.Name, IMAP: EmailIMAPConfig{Host: acct.IMAP.Host, Port: acct.IMAP.Port, Username: acct.IMAP.Username}, SMTP: EmailSMTPConfig{Host: acct.SMTP.Host, Port: acct.SMTP.Port, Username: acct.SMTP.Username}}
		defaults := EmailConfig{Accounts: []EmailAccountConfig{fresh}}
		defaults.ApplyDefaults()
		want := defaults.Accounts[0]
		if acct.IMAP.TLS == nil || *acct.IMAP.TLS != *want.IMAP.TLS {
			t.Errorf("%s: example imap.tls = %v, defaults give %v", acct.Name, acct.IMAP.TLS, *want.IMAP.TLS)
		}
		if acct.SMTP.Host != "" && (acct.SMTP.StartTLS == nil || *acct.SMTP.StartTLS != *want.SMTP.StartTLS) {
			t.Errorf("%s: example smtp.starttls = %v, defaults give %v", acct.Name, acct.SMTP.StartTLS, *want.SMTP.StartTLS)
		}
		if acct.Description == "" {
			t.Errorf("%s: the example should show an operator description", acct.Name)
		}
	}
	if example.PollInterval == nil || *example.PollInterval != defaultEmailPollIntervalSec {
		t.Errorf("example poll_interval = %v, want the default %d", example.PollInterval, defaultEmailPollIntervalSec)
	}
	if err := example.Validate(); err != nil {
		t.Errorf("example email config does not validate: %v", err)
	}
}
