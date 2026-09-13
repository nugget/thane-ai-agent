package email

import (
	"fmt"
	"log/slog"
	"strings"
)

// Manager holds one [Client] per configured account and routes
// requests to them by name. The first configured account is the
// primary, which an empty account name selects.
type Manager struct {
	clients  map[string]*Client
	configs  map[string]AccountConfig
	order    []string // config order; order[0] is primary
	bccOwner string
	logger   *slog.Logger
}

// NewManager creates a manager from the email configuration. Each
// configured account gets a lazily connected Client.
func NewManager(cfg Config, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		clients:  make(map[string]*Client, len(cfg.Accounts)),
		configs:  make(map[string]AccountConfig, len(cfg.Accounts)),
		bccOwner: cfg.BccOwner,
		logger:   logger,
	}

	for _, acct := range cfg.Accounts {
		client := NewClient(acct.Name, acct.IMAP, logger.With("email_account", acct.Name))
		m.clients[acct.Name] = client
		m.configs[acct.Name] = acct
		m.order = append(m.order, acct.Name)
	}

	return m
}

// resolveName maps an account argument to a configured name: empty
// selects the primary, and an unknown name is refused with the list
// of names that exist so the caller can pick one.
func (m *Manager) resolveName(name string) (string, error) {
	if len(m.order) == 0 {
		return "", fmt.Errorf("no email accounts are configured")
	}
	if name == "" {
		return m.order[0], nil
	}
	if _, ok := m.clients[name]; !ok {
		return "", fmt.Errorf("email account %q not found; retry with account set to one of [%s]", name, strings.Join(m.order, ", "))
	}
	return name, nil
}

// Account returns the named client, or the primary client if name is
// empty.
func (m *Manager) Account(name string) (*Client, error) {
	resolved, err := m.resolveName(name)
	if err != nil {
		return nil, err
	}
	return m.clients[resolved], nil
}

// AccountConfig returns the full configuration for the named account,
// or the primary account if name is empty. This includes SMTP settings
// and the default From address needed for sending.
func (m *Manager) AccountConfig(name string) (AccountConfig, error) {
	resolved, err := m.resolveName(name)
	if err != nil {
		return AccountConfig{}, err
	}
	return m.configs[resolved], nil
}

// BccOwner returns the configured auto-Bcc address for outbound email,
// or empty when none is configured.
func (m *Manager) BccOwner() string {
	return m.bccOwner
}

// Primary returns the default account name, or empty when no account
// is configured.
func (m *Manager) Primary() string {
	if len(m.order) == 0 {
		return ""
	}
	return m.order[0]
}

// AccountNames returns all configured account names in configuration
// order, primary first.
func (m *Manager) AccountNames() []string {
	return append([]string(nil), m.order...)
}

// AccountsInConfigOrder returns the account configurations in
// declaration order, primary first, so renderers never reach into the
// manager's maps.
func (m *Manager) AccountsInConfigOrder() []AccountConfig {
	out := make([]AccountConfig, 0, len(m.order))
	for _, name := range m.order {
		out = append(out, m.configs[name])
	}
	return out
}

// Close closes all client connections.
func (m *Manager) Close() {
	for _, name := range m.order {
		if err := m.clients[name].Close(); err != nil {
			m.logger.Warn("error closing email client", "account", name, "error", err)
		}
	}
}
