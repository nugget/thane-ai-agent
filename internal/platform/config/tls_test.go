package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// validTLS returns a front-door config that passes validation, for tests
// to break one field at a time.
func validTLS() TLSConfig {
	return TLSConfig{
		Enabled:    true,
		HTTPS:      TLSListenConfig{Port: 443},
		HTTP:       TLSRedirectConfig{Port: 80},
		HSTSMaxAge: 4320 * time.Hour,
		Hostnames:  map[string]string{"thane.example.net": "native"},
		CertMagic: CertMagicConfig{
			Agreed:  true,
			Storage: "/srv/thane/tls",
			DNS:     CertMagicDNSConfig{Provider: "linode"},
		},
	}
}

func TestTLSConfigParsesPassThrough(t *testing.T) {
	t.Parallel()
	src := `
tls:
  enabled: true
  hostnames:
    thane.example.net: native
  certmagic:
    ca: https://acme-staging-v02.api.letsencrypt.org/directory
    email: alice@example.net
    agreed: true
    key_type: p256
    cert_obtain_timeout: 30m
    dns:
      provider: linode
      propagation_delay: 10m
      propagation_timeout: 15m
      resolvers: [ns1.linode.com, "ns2.linode.com:53"]
      settings:
        api_token: abc123
        api_version: v4
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	d := cfg.TLS.CertMagic.DNS
	if d.PropagationDelay != 10*time.Minute || d.PropagationTimeout != 15*time.Minute {
		t.Fatalf("propagation = %s/%s, want 10m/15m", d.PropagationDelay, d.PropagationTimeout)
	}
	if cfg.TLS.CertMagic.CertObtainTimeout != 30*time.Minute {
		t.Fatalf("cert_obtain_timeout = %s", cfg.TLS.CertMagic.CertObtainTimeout)
	}
	if got := d.Settings["api_token"]; got != "abc123" {
		t.Fatalf("settings.api_token = %v", got)
	}
	if got := d.Settings["api_version"]; got != "v4" {
		t.Fatalf("settings.api_version = %v", got)
	}
	if len(d.Resolvers) != 2 || d.Resolvers[1] != "ns2.linode.com:53" {
		t.Fatalf("resolvers = %v", d.Resolvers)
	}
	if cfg.TLS.CertMagic.KeyType != "p256" || cfg.TLS.CertMagic.Email != "alice@example.net" {
		t.Fatalf("certmagic = %+v", cfg.TLS.CertMagic)
	}
}

func TestTLSDefaults(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.Workspace.Path = "/srv/thane"
	cfg.applyDefaults()
	if cfg.TLS.HTTPS.Port != 443 || cfg.TLS.HTTP.Port != 80 {
		t.Fatalf("ports = %d/%d, want 443/80", cfg.TLS.HTTPS.Port, cfg.TLS.HTTP.Port)
	}
	if cfg.TLS.HSTSMaxAge != 4320*time.Hour {
		t.Fatalf("hsts_max_age = %s, want 4320h", cfg.TLS.HSTSMaxAge)
	}
	if want := filepath.Join("/srv/thane", "tls"); cfg.TLS.CertMagic.Storage != want {
		t.Fatalf("storage = %q, want %q", cfg.TLS.CertMagic.Storage, want)
	}
	if cfg.TLS.Enabled {
		t.Fatal("tls enabled by default")
	}
}

func TestTLSDefaultsRespectExplicitValues(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.TLS.HTTPS.Port = 8443
	cfg.TLS.HSTSMaxAge = time.Hour
	cfg.TLS.CertMagic.Storage = "/elsewhere"
	cfg.applyDefaults()
	if cfg.TLS.HTTPS.Port != 8443 || cfg.TLS.HSTSMaxAge != time.Hour || cfg.TLS.CertMagic.Storage != "/elsewhere" {
		t.Fatalf("explicit values overwritten: %+v", cfg.TLS)
	}
}

func TestValidateTLS(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr string
	}{
		{"valid", func(c *Config) {}, ""},
		{"disabled skips checks", func(c *Config) { c.TLS.Enabled = false; c.TLS.Hostnames = nil }, ""},
		{"https port out of range", func(c *Config) { c.TLS.HTTPS.Port = 70000 }, "tls.https.port"},
		{"http port out of range", func(c *Config) { c.TLS.HTTP.Port = 0 }, "tls.http.port"},
		{"http disabled ignores its port", func(c *Config) { c.TLS.HTTP.Disabled = true; c.TLS.HTTP.Port = 0 }, ""},
		{"http and https collide", func(c *Config) { c.TLS.HTTP.Port = 443 }, "cannot share port"},
		{"negative hsts", func(c *Config) { c.TLS.HSTSMaxAge = -time.Second }, "hsts_max_age"},
		{"no hostnames", func(c *Config) { c.TLS.Hostnames = map[string]string{} }, "at least one hostname"},
		{"unknown surface", func(c *Config) { c.TLS.Hostnames["x.example.net"] = "carddav" }, "unknown surface"},
		{"ollama surface needs shim", func(c *Config) { c.TLS.Hostnames["o.example.net"] = "ollama" }, "ollama_api.enabled is false"},
		{"openai surface needs shim", func(c *Config) { c.TLS.Hostnames["a.example.net"] = "openai" }, "openai_api.enabled is false"},
		{"ollama surface with shim enabled", func(c *Config) {
			c.OllamaAPI.Enabled = true
			c.OllamaAPI.Port = 11434
			c.TLS.Hostnames["o.example.net"] = "ollama"
		}, ""},
		{"wildcard hostname", func(c *Config) { c.TLS.Hostnames["*.example.net"] = "native" }, "wildcard"},
		{"ip literal hostname", func(c *Config) { c.TLS.Hostnames["192.0.2.10"] = "native" }, "IP literal"},
		{"hostname with port", func(c *Config) { c.TLS.Hostnames["thane.example.net:443"] = "native" }, "without port"},
		{"uppercase hostname", func(c *Config) { c.TLS.Hostnames["Thane.example.net"] = "native" }, "lowercase"},
		{"empty peer ca", func(c *Config) { c.TLS.ClientAuth.TrustedPeerCAs = []string{" "} }, "trusted_peer_cas[0]"},
		{"agreed false", func(c *Config) { c.TLS.CertMagic.Agreed = false }, "agreed must be true"},
		{"unknown key type", func(c *Config) { c.TLS.CertMagic.KeyType = "dsa" }, "key_type"},
		{"renewal ratio out of range", func(c *Config) { c.TLS.CertMagic.RenewalWindowRatio = 1 }, "renewal_window_ratio"},
		{"negative obtain timeout", func(c *Config) { c.TLS.CertMagic.CertObtainTimeout = -1 }, "cert_obtain_timeout"},
		{"empty storage", func(c *Config) { c.TLS.CertMagic.Storage = "" }, "storage is required"},
		{"storage inside core", func(c *Config) {
			c.Workspace.Path = "/srv/thane"
			c.TLS.CertMagic.Storage = "/srv/thane/core/tls"
		}, "inside the core root"},
		{"storage beside core is fine", func(c *Config) {
			c.Workspace.Path = "/srv/thane"
			c.TLS.CertMagic.Storage = "/srv/thane/coretls"
		}, ""},
		{"missing provider", func(c *Config) { c.TLS.CertMagic.DNS.Provider = "" }, "dns.provider is required"},
		{"hostname with space", func(c *Config) { c.TLS.Hostnames["bad host.example.net"] = "native" }, "only lowercase letters"},
		{"hostname leading dot", func(c *Config) { c.TLS.Hostnames[".example.net"] = "native" }, "empty label"},
		{"hostname doubled dot", func(c *Config) { c.TLS.Hostnames["foo..example.net"] = "native" }, "empty label"},
		{"hostname trailing dot", func(c *Config) { c.TLS.Hostnames["thane.example.net."] = "native" }, "empty label"},
		{"hostname underscore", func(c *Config) { c.TLS.Hostnames["my_host.example.net"] = "native" }, "only lowercase letters"},
		{"hostname single label", func(c *Config) { c.TLS.Hostnames["localhost"] = "native" }, "at least two labels"},
		{"hostname label hyphen edge", func(c *Config) { c.TLS.Hostnames["-thane.example.net"] = "native" }, "hyphen"},
		{"hostname label too long", func(c *Config) { c.TLS.Hostnames[strings.Repeat("a", 64)+".example.net"] = "native" }, "longer than 63"},
		{"hostname with hyphen and digits ok", func(c *Config) { c.TLS.Hostnames["thane-2.example.net"] = "native" }, ""},
		{"propagation timeout -1 passes through", func(c *Config) { c.TLS.CertMagic.DNS.PropagationTimeout = -1 }, ""},
		{"propagation timeout below -1 rejected", func(c *Config) { c.TLS.CertMagic.DNS.PropagationTimeout = -2 }, "propagation_timeout"},
		{"negative propagation delay", func(c *Config) { c.TLS.CertMagic.DNS.PropagationDelay = -1 }, "propagation_delay"},
		{"empty resolver", func(c *Config) { c.TLS.CertMagic.DNS.Resolvers = []string{""} }, "resolvers[0]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{TLS: validTLS()}
			tc.mutate(cfg)
			err := cfg.validateTLS()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

// TestValidateRunsTLSChecks pins that Config.Validate reaches the TLS
// checks at all, since the table above calls them directly.
func TestValidateRunsTLSChecks(t *testing.T) {
	t.Parallel()
	cfg := &Config{TLS: validTLS()}
	cfg.Workspace.Path = t.TempDir()
	cfg.applyDefaults()
	cfg.TLS.CertMagic.Agreed = false
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "tls.certmagic.agreed") {
		t.Fatalf("Validate = %v, want the tls agreed error", err)
	}
}

// TestTLSStorageSymlinkIntoCoreRefused pins that containment resolves
// symlinks: a storage path that lexically sits beside core but resolves
// into it is still refused.
func TestTLSStorageSymlinkIntoCoreRefused(t *testing.T) {
	t.Parallel()
	ws := t.TempDir()
	core := filepath.Join(ws, "core")
	if err := os.MkdirAll(filepath.Join(core, "tls"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, "certs")
	if err := os.Symlink(filepath.Join(core, "tls"), link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	cfg := &Config{TLS: validTLS()}
	cfg.Workspace.Path = ws
	cfg.TLS.CertMagic.Storage = filepath.Join(link, "store")
	err := cfg.validateTLS()
	if err == nil || !strings.Contains(err.Error(), "inside the core root") {
		t.Fatalf("validateTLS = %v, want core-root refusal through the symlink", err)
	}
}

func TestTLSHSTSDisabledSurvivesDefaults(t *testing.T) {
	t.Parallel()
	cfg := &Config{}
	cfg.TLS.HSTSDisabled = true
	cfg.applyDefaults()
	if !cfg.TLS.HSTSDisabled || cfg.TLS.HSTSMaxAge != 4320*time.Hour {
		t.Fatalf("hsts after defaults = disabled:%v max_age:%s", cfg.TLS.HSTSDisabled, cfg.TLS.HSTSMaxAge)
	}
}
