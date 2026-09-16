package config

import (
	"strings"
	"testing"
	"time"
)

func TestValidateListenAuth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr string
	}{
		{"no tokens is valid and open", func(c *Config) {}, ""},
		{"one token", func(c *Config) { c.Listen.Auth.Tokens = []APIToken{{Label: "alice", Token: "t1"}} }, ""},
		{"empty token", func(c *Config) { c.Listen.Auth.Tokens = []APIToken{{Label: "alice", Token: ""}} }, "empty token"},
		{"whitespace token", func(c *Config) { c.Listen.Auth.Tokens = []APIToken{{Label: "alice", Token: " t1 "}} }, "surrounding whitespace"},
		{"duplicate token", func(c *Config) {
			c.Listen.Auth.Tokens = []APIToken{{Label: "alice", Token: "t1"}, {Label: "bob", Token: "t1"}}
		}, "repeats the token at index 0"},
		{"token shared with a companion account", func(c *Config) {
			c.Listen.Auth.Tokens = []APIToken{{Label: "alice", Token: "shared"}}
			c.Companion.Providers = map[string]CompanionProviderConfig{"phone": {Tokens: []string{"shared"}}}
		}, `companion token for account "phone"`},
		{"negative session ttl", func(c *Config) { c.Listen.Auth.SessionTTL = -time.Second }, "session_ttl"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{}
			tc.mutate(cfg)
			err := cfg.validateListenAuth()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateListenAuth: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestListenAuthDefaultsAndIndex(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.applyDefaults()
	if cfg.Listen.Auth.SessionTTL != 168*time.Hour {
		t.Fatalf("session_ttl default = %s", cfg.Listen.Auth.SessionTTL)
	}
	if cfg.Listen.Auth.Configured() {
		t.Fatal("gate configured with no tokens")
	}
	cfg.Listen.Auth.Tokens = []APIToken{{Label: "alice", Token: "t1"}, {Label: "bob", Token: "t2"}}
	if !cfg.Listen.Auth.Configured() {
		t.Fatal("gate not configured with tokens")
	}
	idx := cfg.Listen.Auth.TokenIndex()
	if idx["t1"] != "alice" || idx["t2"] != "bob" || len(idx) != 2 {
		t.Fatalf("index = %v", idx)
	}
}
