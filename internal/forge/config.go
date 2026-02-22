package forge

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/httpkit"
	"golang.org/x/oauth2"

	gogithub "github.com/google/go-github/v69/github"
)

// Config holds all forge account configurations. It is embedded in the
// top-level Thane config under the "forge" YAML key.
type Config struct {
	// Accounts lists the configured forge accounts.
	Accounts []AccountConfig `yaml:"accounts"`
}

// AccountConfig describes a single forge account connection.
type AccountConfig struct {
	// Name is a short identifier used in tool parameters and logging
	// (e.g., "github-primary"). Required.
	Name string `yaml:"name"`

	// Provider identifies the forge type. Currently supported: "github".
	// "gitea" is reserved for future use.
	Provider string `yaml:"provider"`

	// Token is the personal access token or app installation token for
	// authenticating to the forge API. Required.
	Token string `yaml:"token"`

	// URL is the base API URL for self-hosted instances (e.g. Gitea).
	// For GitHub this defaults to https://api.github.com and may be
	// omitted.
	URL string `yaml:"url"`

	// Owner is the default repository owner (org or user) to use when
	// a repo is specified without an owner prefix. Optional.
	Owner string `yaml:"owner"`

	// Username is the authenticated user's login name. Used for
	// display and logging purposes.
	Username string `yaml:"username"`
}

// Configured reports whether at least one forge account is configured.
func (c Config) Configured() bool {
	return len(c.Accounts) > 0
}

// ApplyDefaults fills zero-value fields with sensible defaults.
// Called by the parent config's applyDefaults method.
func (c *Config) ApplyDefaults() {
	// No defaults required at this time.
}

// Validate checks that the forge configuration is internally consistent.
// Returns the first error found.
func (c Config) Validate() error {
	names := make(map[string]bool, len(c.Accounts))
	for i, a := range c.Accounts {
		if a.Name == "" {
			return fmt.Errorf("forge.accounts[%d].name must not be empty", i)
		}
		if names[a.Name] {
			return fmt.Errorf("forge.accounts[%d].name %q is a duplicate", i, a.Name)
		}
		names[a.Name] = true

		if a.Provider != "github" && a.Provider != "gitea" {
			return fmt.Errorf("forge.accounts[%d] (%s): provider must be \"github\" or \"gitea\"", i, a.Name)
		}
		if a.Token == "" {
			return fmt.Errorf("forge.accounts[%d] (%s): token is required", i, a.Name)
		}
	}
	return nil
}

// Registry holds the ForgeProvider instances indexed by account name.
type Registry struct {
	providers   map[string]ForgeProvider
	configs     map[string]AccountConfig
	defaultName string
}

// NewRegistry creates a Registry from the supplied configuration.
// The httpClient parameter is reserved for testing injection; pass nil for
// production use (each provider constructs its own transport).
func NewRegistry(cfg Config, _ *http.Client) (*Registry, error) {
	r := &Registry{
		providers: make(map[string]ForgeProvider, len(cfg.Accounts)),
		configs:   make(map[string]AccountConfig, len(cfg.Accounts)),
	}

	for _, acfg := range cfg.Accounts {
		switch acfg.Provider {
		case "github":
			p, err := newGitHubProvider(acfg)
			if err != nil {
				return nil, fmt.Errorf("forge: initialising github account %q: %w", acfg.Name, err)
			}
			r.providers[acfg.Name] = p
			r.configs[acfg.Name] = acfg
			if r.defaultName == "" {
				r.defaultName = acfg.Name
			}
		case "gitea":
			slog.Warn("forge: gitea provider not yet implemented, skipping", "account", acfg.Name)
			continue
		default:
			slog.Warn("forge: unknown provider, skipping", "account", acfg.Name, "provider", acfg.Provider)
			continue
		}
	}

	return r, nil
}

// Account returns the provider and config for the named account.
// If name is empty the default account is used.
func (r *Registry) Account(name string) (ForgeProvider, AccountConfig, error) {
	if name == "" {
		name = r.defaultName
	}
	p, ok := r.providers[name]
	if !ok {
		return nil, AccountConfig{}, fmt.Errorf("forge: no account named %q", name)
	}
	return p, r.configs[name], nil
}

// ResolveRepo splits repo into owner and name. If repo already contains a
// "/" it is split there; otherwise cfg.Owner is prepended.
func (r *Registry) ResolveRepo(cfg AccountConfig, repo string) (string, string) {
	if idx := strings.Index(repo, "/"); idx >= 0 {
		return repo[:idx], repo[idx+1:]
	}
	return cfg.Owner, repo
}

// newGitHubProvider constructs a GitHub provider using an oauth2 transport
// layered on top of the shared httpkit base transport.
func newGitHubProvider(cfg AccountConfig) (*githubProvider, error) {
	transport := &oauth2.Transport{
		Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: cfg.Token}),
		Base:   httpkit.NewTransport(),
	}
	ghClient := gogithub.NewClient(&http.Client{Transport: transport})
	return &githubProvider{client: ghClient, owner: cfg.Owner}, nil
}
