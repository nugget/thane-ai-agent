package email

import "fmt"

// Config holds all email account configurations. It is embedded in the
// top-level Thane config under the "email" YAML key.
type Config struct {
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
	}
	return nil
}

// AccountConfig describes a single email account with its IMAP
// connection parameters.
type AccountConfig struct {
	// Name is a short identifier used in tool parameters and logging
	// (e.g., "personal", "work"). Required.
	Name string `yaml:"name"`

	// IMAP configures the IMAP connection for reading email.
	IMAP IMAPConfig `yaml:"imap"`
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
