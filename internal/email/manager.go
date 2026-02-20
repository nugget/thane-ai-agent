package email

import (
	"fmt"
	"log/slog"
)

// Manager holds multiple named IMAP email clients and routes requests
// to the appropriate account. The first configured account becomes the
// primary (default) account.
type Manager struct {
	clients  map[string]*Client
	configs  map[string]AccountConfig
	bccOwner string
	primary  string
	logger   *slog.Logger
}

// NewManager creates a manager from the email configuration. Each
// configured account gets a lazily-connected Client. The first account
// becomes the primary.
func NewManager(cfg Config, logger *slog.Logger) *Manager {
	m := &Manager{
		clients:  make(map[string]*Client, len(cfg.Accounts)),
		configs:  make(map[string]AccountConfig, len(cfg.Accounts)),
		bccOwner: cfg.BccOwner,
		logger:   logger,
	}

	for i, acct := range cfg.Accounts {
		client := NewClient(acct.IMAP, logger.With("email_account", acct.Name))
		m.clients[acct.Name] = client
		m.configs[acct.Name] = acct
		if i == 0 {
			m.primary = acct.Name
		}
	}

	return m
}

// Account returns the named client, or the primary client if name is
// empty. Returns an error if the account is not found.
func (m *Manager) Account(name string) (*Client, error) {
	if name == "" {
		name = m.primary
	}
	client, ok := m.clients[name]
	if !ok {
		return nil, fmt.Errorf("email account %q not found", name)
	}
	return client, nil
}

// AccountConfig returns the full configuration for the named account,
// or the primary account if name is empty. This includes SMTP settings
// and the default From address needed for sending.
func (m *Manager) AccountConfig(name string) (AccountConfig, error) {
	if name == "" {
		name = m.primary
	}
	cfg, ok := m.configs[name]
	if !ok {
		return AccountConfig{}, fmt.Errorf("email account %q not found", name)
	}
	return cfg, nil
}

// BccOwner returns the configured auto-Bcc address for outbound email.
// Returns empty if no Bcc owner is configured.
func (m *Manager) BccOwner() string {
	return m.bccOwner
}

// Primary returns the default account name.
func (m *Manager) Primary() string {
	return m.primary
}

// AccountNames returns all configured account names in no particular order.
func (m *Manager) AccountNames() []string {
	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	return names
}

// Close closes all client connections.
func (m *Manager) Close() {
	for name, client := range m.clients {
		if err := client.Close(); err != nil {
			m.logger.Warn("error closing email client", "account", name, "error", err)
		}
	}
}
