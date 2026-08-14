package config

import "fmt"

// ForgeConfig holds all code forge account and subscription configuration.
type ForgeConfig struct {
	Accounts []ForgeAccountConfig `yaml:"accounts"`

	// SubscriptionCheckInterval is how often (in seconds) to poll followed
	// repositories for releases and commits. Zero disables polling.
	SubscriptionCheckInterval int `yaml:"subscription_check_interval"`

	// MaxSubscriptions limits runtime-managed repository event subscriptions.
	MaxSubscriptions int `yaml:"max_subscriptions"`
}

// ForgeAccountConfig describes a single code forge account.
type ForgeAccountConfig struct {
	// Name is a short identifier (e.g., "github-primary").
	Name string `yaml:"name"`

	// Provider selects the forge backend: "github" or "gitea".
	Provider string `yaml:"provider"`

	// Token is the API authentication token.
	Token string `yaml:"token"`

	// Owner is the default repository owner for unqualified repo references.
	Owner string `yaml:"owner"`

	// URL is the API base URL. Required for gitea. Optional for GitHub
	// (defaults to https://api.github.com).
	URL string `yaml:"url"`

	// Description is an operator-authored note about what this account is for,
	// surfaced to the model alongside the account name. It lets the operator
	// explain an account's intended boundary before the model discovers it by
	// being refused; for example, "read-only observation token; writes are
	// denied" avoids a wasted turn and a misread failure.
	Description string `yaml:"description"`
}

// Configured reports whether at least one forge account is configured with a
// provider and token.
func (c ForgeConfig) Configured() bool {
	for _, acct := range c.Accounts {
		if acct.Provider != "" && acct.Token != "" {
			return true
		}
	}
	return false
}

// Validate checks that the forge configuration is internally consistent.
func (c ForgeConfig) Validate() error {
	if c.SubscriptionCheckInterval < 0 {
		return fmt.Errorf("forge.subscription_check_interval must be >= 0")
	}
	if c.MaxSubscriptions < 0 {
		return fmt.Errorf("forge.max_subscriptions must be >= 0")
	}
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
func (c *ForgeConfig) ApplyDefaults() {
	if c.MaxSubscriptions == 0 {
		c.MaxSubscriptions = 50
	}
	for i := range c.Accounts {
		if c.Accounts[i].Provider == "github" && c.Accounts[i].URL == "" {
			c.Accounts[i].URL = "https://api.github.com"
		}
	}
}
