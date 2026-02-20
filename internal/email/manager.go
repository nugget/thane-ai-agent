package email

import (
	"fmt"
	"log/slog"
)

// Manager holds multiple named IMAP email clients and routes requests
// to the appropriate account. The first configured account becomes the
// primary (default) account.
type Manager struct {
	clients map[string]*Client
	primary string
	logger  *slog.Logger
}

// NewManager creates a manager from the email configuration. Each
// configured account gets a lazily-connected Client. The first account
// becomes the primary.
func NewManager(cfg Config, logger *slog.Logger) *Manager {
	m := &Manager{
		clients: make(map[string]*Client, len(cfg.Accounts)),
		logger:  logger,
	}

	for i, acct := range cfg.Accounts {
		client := NewClient(acct.IMAP, logger.With("email_account", acct.Name))
		m.clients[acct.Name] = client
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
