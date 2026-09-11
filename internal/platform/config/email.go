package config

import (
	"fmt"
	"net/mail"
	"strings"
	"time"
)

// defaultEmailPollIntervalSec is the poll interval applied when email is
// configured and the operator did not set one.
const defaultEmailPollIntervalSec = 300

// EmailConfig configures native IMAP and SMTP email. When at least one
// account has an IMAP host and username, Thane lists, reads, searches,
// files, and (for accounts with smtp configured) sends mail directly,
// and polls each account's INBOX for new messages that wake the
// email-default-handler loop.
type EmailConfig struct {
	// BccOwner is an address that receives a blind copy of every message
	// the agent sends, unless it is already a recipient. It is an audit
	// copy for the operator, not a recipient the agent chose, and it
	// does not need a contact record. Leave empty to disable.
	BccOwner string `yaml:"bcc_owner"`

	// PollInterval is how often, in seconds, every account's INBOX is
	// checked for new mail. Default: 300 when at least one account is
	// configured. Set to 0 to disable polling; the built-in email-poller
	// and email-default-handler loops are then not created, and mail is
	// only ever read when a tool asks for it. Negative values are
	// rejected.
	PollInterval *int `yaml:"poll_interval"`

	// Accounts lists the mailboxes Thane connects to. The first account is
	// the primary: tools use it when no account is named and no loop
	// binding selects one.
	Accounts []EmailAccountConfig `yaml:"accounts"`
}

// EmailAccountConfig describes one mailbox: its IMAP connection, an
// optional SMTP connection for sending, and the folders Thane writes
// copies into.
type EmailAccountConfig struct {
	// Name is the short identifier tools and loop bindings use for this
	// account (for example "personal" or "packages"). Required and unique.
	Name string `yaml:"name"`

	// Description is an operator-authored note about what this mailbox is
	// for, shown to the model beside the account name so it learns the
	// account's purpose and limits before acting rather than by being
	// refused. For example: "parcel and delivery notifications; never
	// sends".
	Description string `yaml:"description"`

	// IMAP configures the connection used to read and organize mail.
	IMAP EmailIMAPConfig `yaml:"imap"`

	// SMTP configures the connection used to send mail. Omit to make the
	// account read-and-organize only.
	SMTP EmailSMTPConfig `yaml:"smtp"`

	// DefaultFrom is the From address for mail sent from this account,
	// for example "Thane <thane@example.com>". Required when smtp is
	// configured. It is also how the poller recognises the account's own
	// outbound copies in INBOX and skips them.
	DefaultFrom string `yaml:"default_from"`

	// SentFolder is the IMAP folder that receives a copy of every message
	// sent from this account, for example "Sent" or "[Gmail]/Sent Mail".
	// Leave empty to keep no server-side copy.
	SentFolder string `yaml:"sent_folder"`
}

// EmailIMAPConfig holds IMAP server connection parameters.
type EmailIMAPConfig struct {
	// Host is the IMAP server hostname, for example "imap.example.com".
	Host string `yaml:"host"`

	// Port is the IMAP server port. Default: 993.
	Port int `yaml:"port"`

	// Username is the IMAP login, typically the mailbox address.
	Username string `yaml:"username"`

	// Password is the IMAP login password. Environment variable
	// expansion such as ${IMAP_PASSWORD} is available.
	Password string `yaml:"password"`

	// TLS controls whether the connection is TLS from the first byte.
	// Default: true on every port except 143. When false, the connection
	// starts in plaintext and is upgraded with STARTTLS before the login;
	// a server that does not offer STARTTLS is refused rather than sent
	// credentials in the clear.
	TLS *bool `yaml:"tls"`
}

// TLSEnabled reports whether the IMAP connection uses TLS, applying the
// port-based default when the field was not set.
func (c EmailIMAPConfig) TLSEnabled() bool {
	if c.TLS != nil {
		return *c.TLS
	}
	return c.Port != 143
}

// EmailSMTPConfig holds SMTP server connection parameters for sending.
type EmailSMTPConfig struct {
	// Host is the SMTP submission server hostname, for example
	// "smtp.example.com". Setting it enables sending from the account.
	Host string `yaml:"host"`

	// Port is the SMTP server port. Default: 587.
	Port int `yaml:"port"`

	// Username is the SMTP login, typically the mailbox address.
	Username string `yaml:"username"`

	// Password is the SMTP login password. Environment variable
	// expansion such as ${SMTP_PASSWORD} is available.
	Password string `yaml:"password"`

	// StartTLS controls whether the connection starts in plaintext and is
	// upgraded with STARTTLS (submission on 587) rather than being TLS
	// from the first byte (465). Default: true on every port except 465.
	// A server that does not offer STARTTLS is refused rather than sent
	// credentials in the clear.
	StartTLS *bool `yaml:"starttls"`
}

// StartTLSEnabled reports whether the SMTP connection upgrades with
// STARTTLS, applying the port-based default when the field was not set.
func (c EmailSMTPConfig) StartTLSEnabled() bool {
	if c.StartTLS != nil {
		return *c.StartTLS
	}
	return c.Port != 465
}

// Configured reports whether at least one account has the minimum IMAP
// configuration (host and username).
func (c EmailConfig) Configured() bool {
	for _, a := range c.Accounts {
		if a.IMAP.Host != "" && a.IMAP.Username != "" {
			return true
		}
	}
	return false
}

// PollingInterval returns how often INBOX is checked, or zero when
// polling is disabled or email is not configured.
func (c EmailConfig) PollingInterval() time.Duration {
	if !c.Configured() {
		return 0
	}
	seconds := defaultEmailPollIntervalSec
	if c.PollInterval != nil {
		seconds = *c.PollInterval
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// SMTPConfigured reports whether this account can send mail.
func (a EmailAccountConfig) SMTPConfigured() bool {
	return a.SMTP.Host != "" && a.SMTP.Username != ""
}

// ApplyDefaults fills unset fields with their documented defaults.
// Called by the parent config's applyDefaults method.
func (c *EmailConfig) ApplyDefaults() {
	if c.PollInterval == nil && c.Configured() {
		v := defaultEmailPollIntervalSec
		c.PollInterval = &v
	}

	for i := range c.Accounts {
		acct := &c.Accounts[i]
		if acct.IMAP.Port == 0 {
			acct.IMAP.Port = 993
		}
		if acct.IMAP.TLS == nil {
			v := acct.IMAP.TLSEnabled()
			acct.IMAP.TLS = &v
		}
		if acct.SMTP.Host != "" {
			if acct.SMTP.Port == 0 {
				acct.SMTP.Port = 587
			}
			if acct.SMTP.StartTLS == nil {
				v := acct.SMTP.StartTLSEnabled()
				acct.SMTP.StartTLS = &v
			}
		}
	}
}

// Validate checks that the email configuration is internally
// consistent. Errors name the YAML path of the first problem found.
func (c EmailConfig) Validate() error {
	if c.PollInterval != nil && *c.PollInterval < 0 {
		return fmt.Errorf("email.poll_interval must be >= 0 (0 disables polling), got %d", *c.PollInterval)
	}
	if c.BccOwner != "" {
		if _, err := mail.ParseAddress(strings.TrimSpace(c.BccOwner)); err != nil {
			return fmt.Errorf("email.bcc_owner %q is not a valid email address: %w", c.BccOwner, err)
		}
	}

	names := make(map[string]bool, len(c.Accounts))
	for i, a := range c.Accounts {
		if a.Name == "" {
			return fmt.Errorf("email.accounts[%d].name must not be empty", i)
		}
		if names[a.Name] {
			return fmt.Errorf("email.accounts[%d].name %q is a duplicate", i, a.Name)
		}
		names[a.Name] = true

		if a.IMAP.Host == "" {
			return fmt.Errorf("email.accounts[%d] (%s): imap.host is required", i, a.Name)
		}
		if a.IMAP.Username == "" {
			return fmt.Errorf("email.accounts[%d] (%s): imap.username is required", i, a.Name)
		}
		if a.IMAP.Port < 1 || a.IMAP.Port > 65535 {
			return fmt.Errorf("email.accounts[%d] (%s): imap.port %d out of range (1-65535)", i, a.Name, a.IMAP.Port)
		}

		if a.SMTP.Host != "" {
			if a.SMTP.Username == "" {
				return fmt.Errorf("email.accounts[%d] (%s): smtp.username is required when smtp.host is set", i, a.Name)
			}
			if a.SMTP.Password == "" {
				return fmt.Errorf("email.accounts[%d] (%s): smtp.password is required when smtp.host is set", i, a.Name)
			}
			if a.SMTP.Port < 1 || a.SMTP.Port > 65535 {
				return fmt.Errorf("email.accounts[%d] (%s): smtp.port %d out of range (1-65535)", i, a.Name, a.SMTP.Port)
			}
			if a.DefaultFrom == "" {
				return fmt.Errorf("email.accounts[%d] (%s): default_from is required when smtp is configured", i, a.Name)
			}
		}
		if a.DefaultFrom != "" {
			if _, err := mail.ParseAddress(strings.TrimSpace(a.DefaultFrom)); err != nil {
				return fmt.Errorf("email.accounts[%d] (%s): default_from %q is not a valid email address: %w", i, a.Name, a.DefaultFrom, err)
			}
		}
	}
	return nil
}
