package forge

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	platformconfig "github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/httpkit"
)

// Config is the serialized forge configuration contract.
type Config = platformconfig.ForgeConfig

// AccountConfig is the serialized configuration for one forge account.
type AccountConfig = platformconfig.ForgeAccountConfig

// Manager holds configured forge providers and routes operations to
// the appropriate account. The first account is the primary (default).
type Manager struct {
	providers map[string]ForgeProvider
	configs   map[string]AccountConfig
	order     []string // preserves config order; order[0] is primary
	logger    *slog.Logger
}

// ResolvedAccount is the provider and configuration selected for one forge
// operation. Name is always explicit, including when the caller selected the
// primary account by omitting an account name.
type ResolvedAccount struct {
	Name     string        `json:"-"`
	Provider ForgeProvider `json:"-"`
	Config   AccountConfig `json:"-"`
}

// NewManager creates a forge manager from the given configuration.
// Each account is instantiated with its provider-specific implementation.
func NewManager(cfg Config, logger *slog.Logger) (*Manager, error) {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		providers: make(map[string]ForgeProvider, len(cfg.Accounts)),
		configs:   make(map[string]AccountConfig, len(cfg.Accounts)),
		logger:    logger,
	}

	for _, acct := range cfg.Accounts {
		var provider ForgeProvider
		var err error

		switch acct.Provider {
		case "github":
			httpClient := httpkit.NewClient(
				httpkit.WithTimeout(30*time.Second),
				httpkit.WithTruthfulUserAgent(httpkit.AgentSurfaceForge),
			)
			provider, err = NewGitHub(httpClient, acct.Name, acct.Token, acct.URL, logger)
			if err != nil {
				return nil, fmt.Errorf("forge account %q: %w", acct.Name, err)
			}
		default:
			return nil, fmt.Errorf("forge account %q: unsupported provider %q", acct.Name, acct.Provider)
		}

		m.providers[acct.Name] = provider
		m.configs[acct.Name] = acct
		m.order = append(m.order, acct.Name)

		logger.Info("forge account configured",
			"name", acct.Name,
			"provider", acct.Provider,
			"owner", acct.Owner,
		)
	}

	return m, nil
}

// ResolveAccount selects a configured account. If name is empty, the primary
// (first configured) account is used. It is the account-selection choke point
// for tools, subscriptions, and future provider-specific policy.
func (m *Manager) ResolveAccount(name string) (ResolvedAccount, error) {
	if len(m.order) == 0 {
		return ResolvedAccount{}, fmt.Errorf("no forge accounts configured")
	}
	if name == "" {
		name = m.order[0]
	}
	p, ok := m.providers[name]
	if !ok {
		return ResolvedAccount{}, fmt.Errorf("forge account %q not found; available accounts: %s", name, strings.Join(m.order, ", "))
	}
	return ResolvedAccount{
		Name:     name,
		Provider: p,
		Config:   m.configs[name],
	}, nil
}

// Account returns the forge provider for the named account. If name is
// empty, the primary (first configured) account is used.
func (m *Manager) Account(name string) (ForgeProvider, error) {
	resolved, err := m.ResolveAccount(name)
	if err != nil {
		return nil, err
	}
	return resolved.Provider, nil
}

// AccountConfig returns the configuration for the named account.
func (m *Manager) AccountConfig(name string) (AccountConfig, error) {
	resolved, err := m.ResolveAccount(name)
	if err != nil {
		return AccountConfig{}, err
	}
	return resolved.Config, nil
}

// accountView is the JSON-serializable representation of a forge
// account injected into the system prompt. It deliberately omits
// secrets (tokens).
type accountView struct {
	Account      string `json:"account"`
	Type         string `json:"type"`
	URL          string `json:"url"`
	DefaultOwner string `json:"default_owner,omitempty"`
	Description  string `json:"description,omitempty"`

	// Bound marks the account as the one this caller is restricted to,
	// so the model reads the narrowed list as a boundary rather than as
	// the whole of what the site has configured.
	Bound bool `json:"bound,omitempty"`
}

// Context returns a markdown block describing the configured forge
// accounts for injection into a system prompt. The output is structured
// JSON wrapped in a fenced code block so the model can immediately
// identify available accounts, their types, default owners, and any
// operator-authored description without guessing. Returns an empty
// string when no accounts are configured.
// Tokens are never included.
func (m *Manager) Context() string {
	if len(m.order) == 0 {
		return ""
	}

	views := make([]accountView, 0, len(m.order))
	for _, name := range m.order {
		cfg := m.configs[name]
		views = append(views, accountView{
			Account:      cfg.Name,
			Type:         cfg.Provider,
			URL:          cfg.URL,
			DefaultOwner: cfg.Owner,
			Description:  cfg.Description,
		})
	}

	data := map[string]any{"forges": views}
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		// Shouldn't happen with simple structs, but degrade gracefully.
		return ""
	}

	var sb strings.Builder
	sb.WriteString("### Forge Accounts\n```json\n")
	sb.Write(jsonBytes)
	sb.WriteString("\n```\n")
	return sb.String()
}

// ResolveRepo converts a repo parameter into "owner/repo" format. If
// repo already contains a slash it is returned as-is. Otherwise the
// account's default owner is prepended.
func (m *Manager) ResolveRepo(accountName, repo string) (string, error) {
	if strings.Contains(repo, "/") {
		return repo, nil
	}

	account, err := m.ResolveAccount(accountName)
	if err != nil {
		return "", err
	}
	if account.Config.Owner == "" {
		return "", fmt.Errorf("repo %q requires an owner but account %q has no default owner configured", repo, account.Name)
	}
	return account.Config.Owner + "/" + repo, nil
}
