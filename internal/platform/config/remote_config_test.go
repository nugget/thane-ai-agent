package config

import (
	"strings"
	"testing"
)

func TestValidateGitRemote(t *testing.T) {
	t.Parallel()

	base := func() DocumentRootGitConfig {
		return DocumentRootGitConfig{
			Enabled:          true,
			SignCommits:      true,
			VerifySignatures: "required",
			SigningKey:       "/keys/signing_ed25519",
			Remote: &DocumentRootGitRemoteConfig{
				URL:         "aimee@pocket.hollowoak.net:Thane/kb.git",
				Mode:        "bidirectional",
				Interval:    "60s",
				TrustAnchor: "/keys/kb.allowed_signers",
				Auth: DocumentRootGitRemoteAuthConfig{
					SSHKey:     "/keys/transport_ed25519",
					KnownHosts: "/ssh/known_hosts",
				},
			},
		}
	}

	for _, tc := range []struct {
		name   string
		mutate func(*DocumentRootGitConfig)
		want   string // substring of expected error; "" means valid
	}{
		{"valid bidirectional", func(*DocumentRootGitConfig) {}, ""},
		{"nil remote is valid", func(g *DocumentRootGitConfig) { g.Remote = nil }, ""},
		{"fetch mode does not require sign_commits", func(g *DocumentRootGitConfig) {
			g.SignCommits = false
			g.Remote.Mode = "fetch"
		}, ""},
		{"https url does not require known_hosts", func(g *DocumentRootGitConfig) {
			g.Remote.URL = "https://example.com/kb.git"
			g.Remote.Auth.KnownHosts = ""
		}, ""},
		{"remote requires git.enabled", func(g *DocumentRootGitConfig) { g.Enabled = false }, "requires"},
		{"url required", func(g *DocumentRootGitConfig) { g.Remote.URL = "" }, "url is required"},
		{"mode required", func(g *DocumentRootGitConfig) { g.Remote.Mode = "" }, "mode is required"},
		{"mode invalid", func(g *DocumentRootGitConfig) { g.Remote.Mode = "sync" }, "must be"},
		{"bidirectional requires sign_commits", func(g *DocumentRootGitConfig) { g.SignCommits = false }, "requires git.sign_commits"},
		{"ssh url requires known_hosts", func(g *DocumentRootGitConfig) { g.Remote.Auth.KnownHosts = "" }, "known_hosts is required"},
		{"transport key must differ from signing key", func(g *DocumentRootGitConfig) { g.Remote.Auth.SSHKey = g.SigningKey }, "must not be the same key"},
		{"verify required needs trust_anchor", func(g *DocumentRootGitConfig) { g.Remote.TrustAnchor = "" }, "trust_anchor is required"},
		{"bad interval", func(g *DocumentRootGitConfig) { g.Remote.Interval = "soon" }, "interval"},
		{"interval 0 disables timer", func(g *DocumentRootGitConfig) { g.Remote.Interval = "0" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			git := base()
			tc.mutate(&git)
			err := validateGitRemote("kb", git)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validateGitRemote() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateGitRemote() = nil, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateGitRemote() = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// TestValidate_GitRemoteWired confirms the remote block is reached by
// Config.Validate through validateDocRoots.
func TestValidate_GitRemoteWired(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.DocRoots = map[string]DocumentRootConfig{
		"kb": {Git: DocumentRootGitConfig{
			Enabled: true,
			Remote:  &DocumentRootGitRemoteConfig{URL: "https://example.com/kb.git", Mode: "nope"},
		}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "doc_roots.kb.git.remote.mode") {
		t.Fatalf("Validate() = %v, want doc_roots.kb.git.remote.mode error", err)
	}
}

func TestIsSSHGitURL(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		url string
		ssh bool
	}{
		{"aimee@pocket.hollowoak.net:Thane/kb.git", true},
		{"pocket.hollowoak.net:Thane/kb.git", true}, // scp-like without a user
		{"ssh://git@host/repo.git", true},
		{"https://example.com/kb.git", false},
		{"git://example.com/kb.git", false},
		{"/local/path/repo.git", false},
		{"./relative/path:notacolon.git", false}, // colon after a slash → local path
	} {
		if got := isSSHGitURL(tc.url); got != tc.ssh {
			t.Fatalf("isSSHGitURL(%q) = %v, want %v", tc.url, got, tc.ssh)
		}
	}
}
