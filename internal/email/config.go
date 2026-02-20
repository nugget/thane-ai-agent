package email

import "fmt"

// Config holds all email account configurations. It is embedded in the
// top-level Thane config under the "email" YAML key.
type Config struct {
	// BccOwner is an email address that receives a blind copy of every
	// outbound message (unless the owner is already a recipient). This
	// provides an audit trail of agent-sent email.
	BccOwner string `yaml:"bcc_owner"`

	// Accounts lists the email accounts to connect to at startup.
	Accounts []AccountConfig `yaml:"accounts"`
}

// Configured reports whether at least one account has the minimum
// required IMAP configuration (host and username).
func (c Config) Configured() bool {
	for _, a := range c.Accounts {
		if a.IMAP.Host != "" && a.IMAP.Username != "" {
			return true
		}
	}
	return false
}

// ApplyDefaults fills zero-value fields with sensible defaults.
// Called by the parent config's applyDefaults method.
func (c *Config) ApplyDefaults() {
	for i := range c.Accounts {
		if c.Accounts[i].IMAP.Port == 0 {
			c.Accounts[i].IMAP.Port = 993
		}
		// TLS defaults to true. Since bool zero-value is false, we use
		// a pointer in the YAML struct to distinguish "not set" from
		// "explicitly false". However, to keep the config simple we
		// default TLS=true unless the port is 143 (plaintext convention).
		if !c.Accounts[i].IMAP.TLS && c.Accounts[i].IMAP.Port != 143 {
			c.Accounts[i].IMAP.TLS = true
		}

		// SMTP defaults: port 587 with STARTTLS.
		if c.Accounts[i].SMTP.Host != "" {
			if c.Accounts[i].SMTP.Port == 0 {
				c.Accounts[i].SMTP.Port = 587
			}
			if !c.Accounts[i].SMTP.StartTLS && c.Accounts[i].SMTP.Port != 465 {
				c.Accounts[i].SMTP.StartTLS = true
			}
		}
	}
}

// Validate checks that the email configuration is internally consistent.
// Returns an error describing the first problem found.
func (c Config) Validate() error {
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

		// Validate SMTP if configured.
		if a.SMTP.Host != "" {
			if a.SMTP.Username == "" {
				return fmt.Errorf("email.accounts[%d] (%s): smtp.username is required when smtp.host is set", i, a.Name)
			}
			if a.SMTP.Port < 1 || a.SMTP.Port > 65535 {
				return fmt.Errorf("email.accounts[%d] (%s): smtp.port %d out of range (1-65535)", i, a.Name, a.SMTP.Port)
			}
			if a.DefaultFrom == "" {
				return fmt.Errorf("email.accounts[%d] (%s): default_from is required when smtp is configured", i, a.Name)
			}
		}
	}
	return nil
}

// AccountConfig describes a single email account with its IMAP
// and optional SMTP connection parameters.
type AccountConfig struct {
	// Name is a short identifier used in tool parameters and logging
	// (e.g., "personal", "work"). Required.
	Name string `yaml:"name"`

	// IMAP configures the IMAP connection for reading email.
	IMAP IMAPConfig `yaml:"imap"`

	// SMTP configures the SMTP connection for sending email.
	// Optional — omit to disable sending from this account.
	SMTP SMTPConfig `yaml:"smtp"`

	// DefaultFrom is the From address for outbound email from this
	// account (e.g., "Aimée <user@gmail.com>"). Required when SMTP
	// is configured.
	DefaultFrom string `yaml:"default_from"`
}

// SMTPConfigured reports whether this account has SMTP send capability.
func (a AccountConfig) SMTPConfigured() bool {
	return a.SMTP.Host != "" && a.SMTP.Username != ""
}

// IMAPConfig holds IMAP server connection parameters.
type IMAPConfig struct {
	// Host is the IMAP server hostname (e.g., "imap.gmail.com").
	Host string `yaml:"host"`

	// Port is the IMAP server port. Default: 993 (IMAPS).
	Port int `yaml:"port"`

	// Username is the IMAP login username (typically the email address).
	Username string `yaml:"username"`

	// Password is the IMAP login password. Supports environment variable
	// expansion via the config loader (e.g., ${IMAP_PASSWORD}).
	Password string `yaml:"password"`

	// TLS controls whether to use TLS for the connection. Default: true.
	// Set to false only for port 143 plaintext connections (not recommended).
	TLS bool `yaml:"tls"`
}

// SMTPConfig holds SMTP server connection parameters for outbound email.
type SMTPConfig struct {
	// Host is the SMTP server hostname (e.g., "smtp.gmail.com").
	Host string `yaml:"host"`

	// Port is the SMTP server port. Default: 587 (submission with STARTTLS).
	Port int `yaml:"port"`

	// Username is the SMTP login username (typically the email address).
	Username string `yaml:"username"`

	// Password is the SMTP login password. Supports environment variable
	// expansion via the config loader (e.g., ${SMTP_PASSWORD}).
	Password string `yaml:"password"`

	// StartTLS controls whether to upgrade the connection with STARTTLS.
	// Default: true. Set to false for port 465 (implicit TLS).
	StartTLS bool `yaml:"starttls"`
}
