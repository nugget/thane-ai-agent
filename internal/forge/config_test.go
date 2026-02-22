package forge

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestConfigured(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{
			name: "empty config",
			cfg:  Config{},
			want: false,
		},
		{
			name: "one complete account",
			cfg: Config{
				Accounts: []AccountConfig{
					{Name: "gh", Provider: "github", Token: "tok123"},
				},
			},
			want: true,
		},
		{
			name: "account missing token",
			cfg: Config{
				Accounts: []AccountConfig{
					{Name: "gh", Provider: "github"},
				},
			},
			want: false,
		},
		{
			name: "account missing provider",
			cfg: Config{
				Accounts: []AccountConfig{
					{Name: "gh", Token: "tok123"},
				},
			},
			want: false,
		},
		{
			name: "one incomplete and one complete",
			cfg: Config{
				Accounts: []AccountConfig{
					{Name: "bad", Provider: "github"},
					{Name: "good", Provider: "github", Token: "tok123"},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.cfg.Configured()
			if got != tt.want {
				t.Errorf("Configured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     Config
		wantErr string // empty means no error expected
	}{
		{
			name: "valid github config",
			cfg: Config{
				Accounts: []AccountConfig{
					{Name: "primary", Provider: "github", Token: "ghp_abc"},
				},
			},
		},
		{
			name: "valid multiple accounts",
			cfg: Config{
				Accounts: []AccountConfig{
					{Name: "primary", Provider: "github", Token: "ghp_abc"},
					{Name: "gitea-work", Provider: "gitea", Token: "tok", URL: "https://gitea.example.com"},
				},
			},
		},
		{
			name: "missing name",
			cfg: Config{
				Accounts: []AccountConfig{
					{Provider: "github", Token: "ghp_abc"},
				},
			},
			wantErr: "name is required",
		},
		{
			name: "duplicate name",
			cfg: Config{
				Accounts: []AccountConfig{
					{Name: "dup", Provider: "github", Token: "tok1"},
					{Name: "dup", Provider: "github", Token: "tok2"},
				},
			},
			wantErr: "duplicate name",
		},
		{
			name: "missing provider",
			cfg: Config{
				Accounts: []AccountConfig{
					{Name: "noprov", Token: "tok"},
				},
			},
			wantErr: "provider is required",
		},
		{
			name: "missing token",
			cfg: Config{
				Accounts: []AccountConfig{
					{Name: "notok", Provider: "github"},
				},
			},
			wantErr: "token is required",
		},
		{
			name: "gitea without URL",
			cfg: Config{
				Accounts: []AccountConfig{
					{Name: "gitea-bad", Provider: "gitea", Token: "tok"},
				},
			},
			wantErr: "url is required for gitea provider",
		},
		{
			name: "gitea with URL is ok",
			cfg: Config{
				Accounts: []AccountConfig{
					{Name: "gitea-ok", Provider: "gitea", Token: "tok", URL: "https://gitea.example.com"},
				},
			},
		},
		{
			name:    "empty config is valid",
			cfg:     Config{},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestApplyDefaults(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Accounts: []AccountConfig{
			{Name: "gh-no-url", Provider: "github", Token: "tok"},
			{Name: "gh-custom-url", Provider: "github", Token: "tok", URL: "https://github.corp.example.com"},
			{Name: "gitea-with-url", Provider: "gitea", Token: "tok", URL: "https://gitea.example.com"},
			{Name: "other-no-url", Provider: "other", Token: "tok"},
		},
	}

	cfg.ApplyDefaults()

	expectations := map[string]string{
		"gh-no-url":      "https://api.github.com",
		"gh-custom-url":  "https://github.corp.example.com",
		"gitea-with-url": "https://gitea.example.com",
		"other-no-url":   "",
	}

	for _, acct := range cfg.Accounts {
		want, ok := expectations[acct.Name]
		if !ok {
			t.Fatalf("unexpected account %q in config", acct.Name)
		}
		if acct.URL != want {
			t.Errorf("account %q: URL = %q, want %q", acct.Name, acct.URL, want)
		}
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewManager(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Accounts: []AccountConfig{
			{Name: "primary", Provider: "github", Token: "ghp_test", URL: "https://api.github.com", Owner: "myorg"},
			{Name: "secondary", Provider: "github", Token: "ghp_test2", URL: "https://api.github.com", Owner: "otherorg"},
		},
	}

	m, err := NewManager(cfg, discardLogger())
	if err != nil {
		t.Fatalf("NewManager() unexpected error: %v", err)
	}

	// Empty name returns primary account.
	p, err := m.Account("")
	if err != nil {
		t.Fatalf("Account(\"\") unexpected error: %v", err)
	}
	if p.Name() != "github" {
		t.Errorf("Account(\"\").Name() = %q, want %q", p.Name(), "github")
	}

	// Named account returns correct provider.
	p2, err := m.Account("secondary")
	if err != nil {
		t.Fatalf("Account(\"secondary\") unexpected error: %v", err)
	}
	if p2.Name() != "github" {
		t.Errorf("Account(\"secondary\").Name() = %q, want %q", p2.Name(), "github")
	}

	// Nonexistent account returns error.
	_, err = m.Account("nonexistent")
	if err == nil {
		t.Fatal("Account(\"nonexistent\") expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("Account(\"nonexistent\") error = %q, want substring %q", err.Error(), "not found")
	}
}

func TestNewManagerUnsupportedProvider(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Accounts: []AccountConfig{
			{Name: "bad", Provider: "unsupported", Token: "tok"},
		},
	}

	_, err := NewManager(cfg, discardLogger())
	if err == nil {
		t.Fatal("NewManager() expected error for unsupported provider, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("NewManager() error = %q, want substring %q", err.Error(), "unsupported provider")
	}
}

func TestNewManagerEmptyConfig(t *testing.T) {
	t.Parallel()

	m, err := NewManager(Config{}, discardLogger())
	if err != nil {
		t.Fatalf("NewManager() unexpected error: %v", err)
	}

	// Empty name on manager with no accounts should error.
	_, err = m.Account("")
	if err == nil {
		t.Fatal("Account(\"\") expected error on empty manager, got nil")
	}
	if !strings.Contains(err.Error(), "no forge accounts configured") {
		t.Errorf("Account(\"\") error = %q, want substring %q", err.Error(), "no forge accounts configured")
	}
}

func TestAccountConfig(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Accounts: []AccountConfig{
			{Name: "primary", Provider: "github", Token: "tok", URL: "https://api.github.com", Owner: "myorg", Username: "myuser"},
		},
	}

	m, err := NewManager(cfg, discardLogger())
	if err != nil {
		t.Fatalf("NewManager() unexpected error: %v", err)
	}

	// Empty name returns primary config.
	acctCfg, err := m.AccountConfig("")
	if err != nil {
		t.Fatalf("AccountConfig(\"\") unexpected error: %v", err)
	}
	if acctCfg.Name != "primary" {
		t.Errorf("AccountConfig(\"\").Name = %q, want %q", acctCfg.Name, "primary")
	}
	if acctCfg.Owner != "myorg" {
		t.Errorf("AccountConfig(\"\").Owner = %q, want %q", acctCfg.Owner, "myorg")
	}

	// Nonexistent returns error.
	_, err = m.AccountConfig("nope")
	if err == nil {
		t.Fatal("AccountConfig(\"nope\") expected error, got nil")
	}
}

func TestResolveRepo(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Accounts: []AccountConfig{
			{Name: "with-owner", Provider: "github", Token: "tok", URL: "https://api.github.com", Owner: "myorg"},
			{Name: "no-owner", Provider: "github", Token: "tok", URL: "https://api.github.com"},
		},
	}

	m, err := NewManager(cfg, discardLogger())
	if err != nil {
		t.Fatalf("NewManager() unexpected error: %v", err)
	}

	tests := []struct {
		name        string
		accountName string
		repo        string
		want        string
		wantErr     string
	}{
		{
			name:        "qualified repo passes through",
			accountName: "with-owner",
			repo:        "someowner/somerepo",
			want:        "someowner/somerepo",
		},
		{
			name:        "bare repo gets owner prepended",
			accountName: "with-owner",
			repo:        "myrepo",
			want:        "myorg/myrepo",
		},
		{
			name:        "bare repo with no owner errors",
			accountName: "no-owner",
			repo:        "myrepo",
			wantErr:     "requires an owner",
		},
		{
			name:        "nonexistent account errors",
			accountName: "nonexistent",
			repo:        "myrepo",
			wantErr:     "not found",
		},
		{
			name:        "empty account uses primary with owner",
			accountName: "",
			repo:        "myrepo",
			want:        "myorg/myrepo",
		},
		{
			name:        "qualified repo with empty account",
			accountName: "",
			repo:        "other/repo",
			want:        "other/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := m.ResolveRepo(tt.accountName, tt.repo)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ResolveRepo(%q, %q) expected error containing %q, got nil", tt.accountName, tt.repo, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("ResolveRepo(%q, %q) error = %q, want substring %q", tt.accountName, tt.repo, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveRepo(%q, %q) unexpected error: %v", tt.accountName, tt.repo, err)
			}
			if got != tt.want {
				t.Errorf("ResolveRepo(%q, %q) = %q, want %q", tt.accountName, tt.repo, got, tt.want)
			}
		})
	}
}
