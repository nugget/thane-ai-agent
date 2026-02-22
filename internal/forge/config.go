package forge

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/httpkit"
)

// Config holds all forge account configurations.
type Config struct {
	Accounts []AccountConfig `yaml:"accounts"`
}

// AccountConfig describes a single forge account.
type AccountConfig struct {
	// Name is a short identifier (e.g., "github-primary").
	Name string `yaml:"name"`

	// Provider selects the forge backend: "github" or "gitea".
	Provider string `yaml:"provider"`

	// Token is the API authentication token.
	Token string `yaml:"token"`

	// Owner is the default repository owner for unqualified repo references.
	Owner string `yaml:"owner"`

	// Username is the forge username for commit attribution.
	Username string `yaml:"username"`

	// URL is the API base URL. Required for gitea. Optional for GitHub
	// (defaults to https://api.github.com).
	URL string `yaml:"url"`
}

// Configured reports whether at least one forge account is configured
// with a provider and token.
func (c Config) Configured() bool {
	for _, acct := range c.Accounts {
		if acct.Provider != "" && acct.Token != "" {
			return true
		}
	}
	return false
}

// Validate checks that the configuration is internally consistent.
func (c Config) Validate() error {
	seen := make(map[string]bool, len(c.Accounts))
	for i, acct := range c.Accounts {
		if acct.Name == "" {
			return fmt.Errorf("forge account %d: name is required", i)
		}
		if seen[acct.Name] {
			return fmt.Errorf("forge account %q: duplicate name", acct.Name)
		}
		seen[acct.Name] = true

		if acct.Provider == "" {
			return fmt.Errorf("forge account %q: provider is required", acct.Name)
		}
		if acct.Token == "" {
			return fmt.Errorf("forge account %q: token is required", acct.Name)
		}
		if acct.Provider == "gitea" && acct.URL == "" {
			return fmt.Errorf("forge account %q: url is required for gitea provider", acct.Name)
		}
	}
	return nil
}

// ApplyDefaults fills in missing optional fields with sensible values.
func (c *Config) ApplyDefaults() {
	for i := range c.Accounts {
		if c.Accounts[i].Provider == "github" && c.Accounts[i].URL == "" {
			c.Accounts[i].URL = "https://api.github.com"
		}
	}
}

// Manager holds configured forge providers and routes operations to
// the appropriate account. The first account is the primary (default).
type Manager struct {
	providers map[string]ForgeProvider
	configs   map[string]AccountConfig
	order     []string // preserves config order; order[0] is primary
	logger    *slog.Logger
}

// NewManager creates a forge manager from the given configuration.
// Each account is instantiated with its provider-specific implementation.
func NewManager(cfg Config, logger *slog.Logger) (*Manager, error) {
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
				httpkit.WithUserAgent("thane-forge/1.0"),
			)
			provider, err = NewGitHub(httpClient, acct.Token, acct.URL, logger)
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

// Account returns the forge provider for the named account. If name is
// empty, the primary (first configured) account is used.
func (m *Manager) Account(name string) (ForgeProvider, error) {
	if name == "" {
		if len(m.order) == 0 {
			return nil, fmt.Errorf("no forge accounts configured")
		}
		name = m.order[0]
	}
	p, ok := m.providers[name]
	if !ok {
		return nil, fmt.Errorf("forge account %q not found", name)
	}
	return p, nil
}

// AccountConfig returns the configuration for the named account.
func (m *Manager) AccountConfig(name string) (AccountConfig, error) {
	if name == "" {
		if len(m.order) == 0 {
			return AccountConfig{}, fmt.Errorf("no forge accounts configured")
		}
		name = m.order[0]
	}
	cfg, ok := m.configs[name]
	if !ok {
		return AccountConfig{}, fmt.Errorf("forge account %q not found", name)
	}
	return cfg, nil
}

// ResolveRepo converts a repo parameter into "owner/repo" format. If
// repo already contains a slash it is returned as-is. Otherwise the
// account's default owner is prepended.
func (m *Manager) ResolveRepo(accountName, repo string) (string, error) {
	if strings.Contains(repo, "/") {
		return repo, nil
	}

	cfg, err := m.AccountConfig(accountName)
	if err != nil {
		return "", err
	}
	if cfg.Owner == "" {
		return "", fmt.Errorf("repo %q requires an owner but account %q has no default owner configured", repo, cfg.Name)
	}
	return cfg.Owner + "/" + repo, nil
}
