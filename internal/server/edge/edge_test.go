package edge

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/identity"
)

func testConfig(t *testing.T, storage string) config.TLSConfig {
	t.Helper()
	return config.TLSConfig{
		Enabled:    true,
		HTTPS:      config.TLSListenConfig{Address: "127.0.0.1", Port: 8443},
		HTTP:       config.TLSRedirectConfig{Address: "127.0.0.1", Port: 8080},
		HSTSMaxAge: 4320 * time.Hour,
		Hostnames: map[string]string{
			"thane.example.net":  "native",
			"ollama.example.net": "ollama",
		},
		CertMagic: config.CertMagicConfig{
			CA:      "https://acme-staging-v02.api.letsencrypt.org/directory",
			Email:   "alice@example.net",
			Agreed:  true,
			Storage: storage,
			DNS: config.CertMagicDNSConfig{
				Provider: "linode",
				Settings: map[string]any{"api_token": "test-token"},
			},
		},
	}
}

func surfaceHandler(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Surface", name)
		w.WriteHeader(http.StatusOK)
	})
}

func testSurfaces() Surfaces {
	return Surfaces{"native": surfaceHandler("native"), "ollama": surfaceHandler("ollama")}
}

// writeCore lays down a core root holding a freshly generated channel CA
// and returns the root plus the CA so tests can issue client leaves.
func writeCore(t *testing.T) (string, *identity.CertificateAuthority) {
	t.Helper()
	root := t.TempDir()
	ca, err := identity.GenerateCertificateAuthority("Thane Channel CA", time.Now())
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	certPath := filepath.Join(root, identity.ChannelCACertFile)
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, ca.Certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, ca
}

// issueClientLeaf signs a client certificate with the CA and returns its
// parsed form plus the CA certificate, as a verified chain would hold them.
func issueClientLeaf(t *testing.T, ca *identity.CertificateAuthority, cn string) []*x509.Certificate {
	t.Helper()
	caCert, err := identity.ParseCACertificate(ca.Certificate)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}
	block, _ := pem.Decode(ca.PrivatePEM)
	if block == nil {
		t.Fatal("CA private PEM did not decode")
	}
	caKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse CA key: %v", err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, pub, caKey)
	if err != nil {
		t.Fatalf("issue leaf: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return []*x509.Certificate{leaf, caCert}
}

func TestNewRoutesByHostAndSetsHSTS(t *testing.T) {
	t.Parallel()
	s, err := New(Options{Config: testConfig(t, t.TempDir()), Surfaces: testSurfaces(), Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	tests := []struct {
		name        string
		host        string
		wantCode    int
		wantSurface string
	}{
		{"native hostname reaches native", "thane.example.net", http.StatusOK, "native"},
		{"ollama hostname reaches ollama", "ollama.example.net", http.StatusOK, "ollama"},
		{"hostname with port still matches", "thane.example.net:8443", http.StatusOK, "native"},
		{"hostname case-insensitive", "Thane.Example.NET", http.StatusOK, "native"},
		{"unknown hostname refused with 421", "other.example.net", http.StatusMisdirectedRequest, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "https://"+tc.host+"/v1/version", nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if got := rec.Header().Get("X-Surface"); got != tc.wantSurface {
				t.Fatalf("surface = %q, want %q", got, tc.wantSurface)
			}
			if got := rec.Header().Get("Strict-Transport-Security"); got != "max-age=15552000" {
				t.Fatalf("HSTS = %q, want max-age=15552000", got)
			}
		})
	}
}

func TestNewRefusesHostnameToMissingSurface(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, t.TempDir())
	cfg.Hostnames["api.example.net"] = "openai"
	_, err := New(Options{Config: cfg, Surfaces: testSurfaces(), Logger: slog.New(slog.DiscardHandler)})
	if err == nil || !strings.Contains(err.Error(), `"openai"`) {
		t.Fatalf("err = %v, want surface-not-running error naming openai", err)
	}
}

func TestRedirectTargetsHTTPS(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		httpsPort int
		host      string
		path      string
		want      string
	}{
		{"default port omitted", 443, "thane.example.net", "/v1/version?x=1", "https://thane.example.net/v1/version?x=1"},
		{"request port dropped", 443, "thane.example.net:80", "/", "https://thane.example.net/"},
		{"non-default https port carried", 8443, "thane.example.net", "/docs", "https://thane.example.net:8443/docs"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig(t, t.TempDir())
			cfg.HTTPS.Port = tc.httpsPort
			s, err := New(Options{Config: cfg, Surfaces: testSurfaces(), Logger: slog.New(slog.DiscardHandler)})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
			req := httptest.NewRequest(http.MethodGet, "http://"+tc.host+tc.path, nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			s.redirect().ServeHTTP(rec, req)
			if rec.Code != http.StatusPermanentRedirect {
				t.Fatalf("status = %d, want 308", rec.Code)
			}
			if got := rec.Header().Get("Location"); got != tc.want {
				t.Fatalf("Location = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClientCertificatePrincipal(t *testing.T) {
	t.Parallel()
	core, ca := writeCore(t)
	cfg := testConfig(t, t.TempDir())
	var seen Principal
	var present bool
	surfaces := Surfaces{
		"native": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen, present = PrincipalFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}),
		"ollama": surfaceHandler("ollama"),
	}
	s, err := New(Options{Config: cfg, Surfaces: surfaces, CoreRoot: core, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	if s.https.TLSConfig.ClientAuth != tls.VerifyClientCertIfGiven || s.https.TLSConfig.ClientCAs == nil {
		t.Fatalf("client auth not armed: mode=%v pool=%v", s.https.TLSConfig.ClientAuth, s.https.TLSConfig.ClientCAs != nil)
	}

	chain := issueClientLeaf(t, ca, "device-alice")

	t.Run("verified chain yields principal", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://thane.example.net/v1/version", nil)
		req.Host = "thane.example.net"
		req.TLS = &tls.ConnectionState{PeerCertificates: chain, VerifiedChains: [][]*x509.Certificate{chain}}
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
		if !present {
			t.Fatal("no principal on request with verified chain")
		}
		if !strings.Contains(seen.Subject, "device-alice") || seen.SerialNumber != "42" || len(seen.Fingerprint) != 64 {
			t.Fatalf("principal = %+v", seen)
		}
	})

	t.Run("presented but unverified certificate yields none", func(t *testing.T) {
		present = false
		req := httptest.NewRequest(http.MethodGet, "https://thane.example.net/v1/version", nil)
		req.Host = "thane.example.net"
		req.TLS = &tls.ConnectionState{PeerCertificates: chain}
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
		if present {
			t.Fatal("unverified certificate became a principal")
		}
	})

	t.Run("no certificate is not refused", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://thane.example.net/v1/version", nil)
		req.Host = "thane.example.net"
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d without client cert, want 200", rec.Code)
		}
	})
}

func TestClientAuthDisabledLeavesPoolNil(t *testing.T) {
	t.Parallel()
	core, _ := writeCore(t)
	cfg := testConfig(t, t.TempDir())
	cfg.ClientAuth.Disabled = true
	s, err := New(Options{Config: cfg, Surfaces: testSurfaces(), CoreRoot: core, Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	if s.https.TLSConfig.ClientCAs != nil || s.https.TLSConfig.ClientAuth != tls.NoClientCert {
		t.Fatal("client auth armed despite being disabled")
	}
}

func TestPreflightReportsMissingPeerCA(t *testing.T) {
	t.Parallel()
	core, _ := writeCore(t)
	cfg := testConfig(t, t.TempDir())
	cfg.ClientAuth.TrustedPeerCAs = []string{"ca/missing.crt"}
	err := Preflight(cfg, core)
	if err == nil || !strings.Contains(err.Error(), "trusted_peer_cas") {
		t.Fatalf("err = %v, want trusted_peer_cas failure", err)
	}
}

func TestPreflightCreatesStorageOwnerOnly(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "tls")
	cfg := testConfig(t, dir)
	if err := Preflight(cfg, ""); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("storage mode = %o, want 700", info.Mode().Perm())
	}
}

func TestProviderRegistry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		provider string
		settings map[string]any
		wantErr  string
	}{
		{"linode with token", "linode", map[string]any{"api_token": "abc"}, ""},
		{"linode case-insensitive name", "Linode", map[string]any{"api_token": "abc"}, ""},
		{"linode missing token", "linode", map[string]any{}, "api_token is required"},
		{"linode unknown setting rejected", "linode", map[string]any{"api_token": "abc", "api-token": "x"}, "unknown field"},
		{"unregistered provider", "route53", map[string]any{}, "not registered"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, defaults, err := newProvider(tc.provider, tc.settings)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("newProvider: %v", err)
			}
			if p == nil {
				t.Fatal("nil provider")
			}
			if defaults.PropagationDelay != 10*time.Minute || defaults.PropagationTimeout != 15*time.Minute {
				t.Fatalf("linode defaults = %+v, want 10m delay / 15m timeout", defaults)
			}
		})
	}
	if got := Providers(); len(got) != 1 || got[0] != "linode" {
		t.Fatalf("Providers() = %v", got)
	}
}

func TestZapBridgeForwardsToSlog(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	z := newZapBridge(logger)
	z.Debug("hidden")
	z.Info("obtained certificate", zapString("identifier", "thane.example.net"))
	z.Warn("renewal failed")
	out := buf.String()
	if strings.Contains(out, "hidden") {
		t.Fatalf("debug line leaked through an info-level handler: %s", out)
	}
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "identifier=thane.example.net") || !strings.Contains(out, "subsystem=tls") {
		t.Fatalf("info line missing fields: %s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("warn line missing: %s", out)
	}
}
