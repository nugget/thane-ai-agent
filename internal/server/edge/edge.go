// Package edge is Thane's HTTPS front door: one TLS listener that holds a
// publicly trusted certificate for each configured hostname, routes each
// hostname to one of the plaintext surfaces, redirects plain HTTP, and
// verifies client certificates against the instance's channel CA. It
// replaces a reverse proxy in front of Thane, and it is the precondition
// for the X.509 material the instance already generates to mean
// anything at the transport.
//
// Certificates are obtained and renewed in-process by certmagic over the
// ACME DNS-01 challenge through a registered libdns provider; see
// [Providers]. The configuration is a pass-through to certmagic's own
// settings ([config.CertMagicConfig]) plus the hostname-to-surface map
// only Thane can supply.
package edge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/caddyserver/certmagic"

	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/identity"
	"github.com/nugget/thane-ai-agent/internal/server/listen"
)

// Surfaces maps a surface name ("native", "ollama", "openai") to the
// complete handler chain that serves it on its plaintext port. The front
// door dispatches to these by hostname, so a hostname reaches exactly
// the routes, guards, and logging its plaintext twin has.
type Surfaces map[string]http.Handler

// Options configures a front door.
type Options struct {
	// Config is the validated tls: block.
	Config config.TLSConfig
	// Surfaces are the handler chains hostnames may route to.
	Surfaces Surfaces
	// CoreRoot locates the channel CA certificate and resolves relative
	// trusted peer CA paths. Empty disables client authentication.
	CoreRoot string
	// Logger receives issuance, renewal, and failure events plus the
	// bridged certmagic log.
	Logger *slog.Logger
}

// Server is a running or runnable front door.
type Server struct {
	cfg       config.TLSConfig
	logger    *slog.Logger
	cache     *certmagic.Cache
	magic     *certmagic.Config
	hostnames []string
	handler   http.Handler
	https     *http.Server
	http      *http.Server
}

// New builds the front door without touching the network: it resolves
// the DNS provider, prepares certificate storage, loads the client CA
// pool, and assembles the listeners. Issuance starts in Start.
func New(opts Options) (*Server, error) {
	cfg := opts.Config
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("subsystem", "tls")

	if !cfg.Enabled {
		return nil, errors.New("edge: tls is not enabled")
	}
	if len(opts.Surfaces) == 0 {
		return nil, errors.New("edge: no surfaces to route to")
	}
	routes := make(map[string]http.Handler, len(cfg.Hostnames))
	hostnames := make([]string, 0, len(cfg.Hostnames))
	for host, surface := range cfg.Hostnames {
		h, ok := opts.Surfaces[surface]
		if !ok || h == nil {
			return nil, fmt.Errorf("edge: hostname %q routes to surface %q, which is not running (available: %s)",
				host, surface, strings.Join(sortedKeys(opts.Surfaces), ", "))
		}
		routes[strings.ToLower(host)] = h
		hostnames = append(hostnames, strings.ToLower(host))
	}

	provider, defaults, err := newProvider(cfg.CertMagic.DNS.Provider, cfg.CertMagic.DNS.Settings)
	if err != nil {
		return nil, fmt.Errorf("edge: %w", err)
	}
	if err := prepareStorage(cfg.CertMagic.Storage); err != nil {
		return nil, fmt.Errorf("edge: %w", err)
	}
	clientCAs, err := loadClientCAs(cfg.ClientAuth, opts.CoreRoot)
	if err != nil {
		return nil, fmt.Errorf("edge: %w", err)
	}

	zlog := newZapBridge(logger)
	s := &Server{cfg: cfg, logger: logger, hostnames: hostnames}

	s.cache = certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return s.magic, nil },
		Logger:           zlog,
	})
	template := certmagic.Config{
		Storage:            &certmagic.FileStorage{Path: cfg.CertMagic.Storage},
		RenewalWindowRatio: cfg.CertMagic.RenewalWindowRatio,
		MustStaple:         cfg.CertMagic.MustStaple,
		OnEvent:            s.onEvent,
		Logger:             zlog,
	}
	if kt := strings.ToLower(cfg.CertMagic.KeyType); kt != "" {
		template.KeySource = certmagic.StandardKeyGenerator{KeyType: certmagic.KeyType(kt)}
	}
	s.magic = certmagic.New(s.cache, template)

	dns := cfg.CertMagic.DNS
	issuer := certmagic.NewACMEIssuer(s.magic, certmagic.ACMEIssuer{
		CA:                      cfg.CertMagic.CA,
		Email:                   cfg.CertMagic.Email,
		Agreed:                  cfg.CertMagic.Agreed,
		CertObtainTimeout:       cfg.CertMagic.CertObtainTimeout,
		DisableHTTPChallenge:    true,
		DisableTLSALPNChallenge: true,
		DNS01Solver: &certmagic.DNS01Solver{DNSManager: certmagic.DNSManager{
			DNSProvider:        provider,
			TTL:                dns.TTL,
			PropagationDelay:   or(dns.PropagationDelay, defaults.PropagationDelay),
			PropagationTimeout: or(dns.PropagationTimeout, defaults.PropagationTimeout),
			Resolvers:          dns.Resolvers,
			OverrideDomain:     dns.OverrideDomain,
			Logger:             zlog,
		}},
		Logger: zlog,
	})
	s.magic.Issuers = []certmagic.Issuer{issuer}

	tlsCfg := s.magic.TLSConfig()
	tlsCfg.MinVersion = tls.VersionTLS12
	tlsCfg.NextProtos = append([]string{"h2", "http/1.1"}, tlsCfg.NextProtos...)
	if clientCAs != nil {
		tlsCfg.ClientCAs = clientCAs
		tlsCfg.ClientAuth = tls.VerifyClientCertIfGiven
	}

	s.handler = s.hsts(withPrincipal(s.routeByHost(routes)))
	s.https = listen.NewServer(
		net.JoinHostPort(cfg.HTTPS.Address, strconv.Itoa(cfg.HTTPS.Port)),
		s.handler,
		30*time.Second,
		300*time.Second, // streaming surfaces sit behind this listener
	)
	s.https.TLSConfig = tlsCfg
	if !cfg.HTTP.Disabled {
		s.http = listen.NewServer(
			net.JoinHostPort(cfg.HTTP.Address, strconv.Itoa(cfg.HTTP.Port)),
			s.redirect(),
			10*time.Second,
			10*time.Second,
		)
	}
	return s, nil
}

// Hostnames returns the hostnames the front door holds certificates for.
func (s *Server) Hostnames() []string { return append([]string(nil), s.hostnames...) }

// Handler returns the HTTPS handler chain, for tests and for callers that
// terminate TLS elsewhere.
func (s *Server) Handler() http.Handler { return s.handler }

// Start begins certificate management for every hostname and serves
// both listeners until ctx is cancelled or a listener fails. Management
// is asynchronous: the listeners come up immediately and a handshake for
// a hostname whose certificate has not yet been issued fails until it
// arrives, which with a ten-minute DNS propagation wait is the honest
// behaviour rather than a boot that blocks on the CA.
func (s *Server) Start(ctx context.Context) error {
	if err := s.magic.ManageAsync(ctx, s.hostnames); err != nil {
		return fmt.Errorf("edge: manage certificates: %w", err)
	}
	errc := make(chan error, 2)
	if s.http != nil {
		s.logger.Info("starting HTTP redirect listener", "address", s.http.Addr)
		go func() {
			if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("edge: http listener: %w", err)
				return
			}
			errc <- nil
		}()
	}
	s.logger.Info("starting HTTPS front door", "address", s.https.Addr, "hostnames", s.hostnames)
	go func() {
		if err := s.https.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("edge: https listener: %w", err)
			return
		}
		errc <- nil
	}()
	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		return err
	}
}

// Shutdown drains both listeners and stops the certificate cache's
// maintenance loop.
func (s *Server) Shutdown(ctx context.Context) error {
	var first error
	if s.http != nil {
		if err := s.http.Shutdown(ctx); err != nil && first == nil {
			first = err
		}
	}
	if err := s.https.Shutdown(ctx); err != nil && first == nil {
		first = err
	}
	if s.cache != nil {
		s.cache.Stop()
	}
	return first
}

// routeByHost dispatches by the request's hostname. An unknown hostname
// gets 421 Misdirected Request: the connection was authoritative for
// none of the names the door holds, and a default surface would let SNI
// and Host disagree silently.
func (s *Server) routeByHost(routes map[string]http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(hostOnly(r.Host))
		next, ok := routes[host]
		if !ok {
			s.logger.Warn("refused request for unconfigured hostname", "host", r.Host, "path", r.URL.Path, "remote", r.RemoteAddr)
			writeJSONError(w, http.StatusMisdirectedRequest, "hostname not served here")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hsts adds Strict-Transport-Security to every HTTPS response.
func (s *Server) hsts(next http.Handler) http.Handler {
	if s.cfg.HSTSMaxAge <= 0 {
		return next
	}
	value := "max-age=" + strconv.FormatInt(int64(s.cfg.HSTSMaxAge/time.Second), 10)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", value)
		next.ServeHTTP(w, r)
	})
}

// redirect answers every plain-HTTP request with a permanent redirect to
// the same hostname and path over HTTPS, carrying the HTTPS port when it
// is not the default.
func (s *Server) redirect() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostOnly(r.Host)
		if host == "" {
			writeJSONError(w, http.StatusBadRequest, "missing host")
			return
		}
		if s.cfg.HTTPS.Port != 443 {
			host = net.JoinHostPort(host, strconv.Itoa(s.cfg.HTTPS.Port))
		}
		target := "https://" + host + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	})
}

// onEvent turns certmagic's lifecycle events into operator-story log
// lines: issuance and renewal at INFO, failures at WARN, everything
// else at DEBUG.
func (s *Server) onEvent(_ context.Context, event string, data map[string]any) error {
	attrs := make([]any, 0, 2*len(data)+2)
	attrs = append(attrs, "event", event)
	for k, v := range data {
		attrs = append(attrs, k, v)
	}
	switch event {
	case "cert_obtained", "cert_renewed":
		s.logger.Info("tls certificate "+strings.TrimPrefix(event, "cert_"), attrs...)
	case "cert_failed":
		s.logger.Warn("tls certificate issuance failed", attrs...)
	default:
		s.logger.Debug("tls certificate event", attrs...)
	}
	return nil
}

// Preflight checks what New would check without building listeners: the
// provider resolves and its settings decode, storage is writable, and
// every client CA parses. It is what `thane validate` runs so a bad
// token or a missing CA file is reported before serve.
func Preflight(cfg config.TLSConfig, coreRoot string) error {
	if !cfg.Enabled {
		return nil
	}
	if _, _, err := newProvider(cfg.CertMagic.DNS.Provider, cfg.CertMagic.DNS.Settings); err != nil {
		return err
	}
	if err := prepareStorage(cfg.CertMagic.Storage); err != nil {
		return err
	}
	if _, err := loadClientCAs(cfg.ClientAuth, coreRoot); err != nil {
		return err
	}
	return nil
}

// prepareStorage creates the certificate directory owner-only and
// confirms it is writable.
func prepareStorage(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("tls.certmagic.storage is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("tls.certmagic.storage: %w", err)
	}
	probe, err := os.CreateTemp(dir, ".write-probe-*")
	if err != nil {
		return fmt.Errorf("tls.certmagic.storage %q is not writable: %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

// loadClientCAs builds the pool a client certificate must chain to: the
// channel CA from core plus any trusted peer CAs. A nil pool means
// client authentication is off, either by config or because there is no
// core root to find the channel CA in.
func loadClientCAs(cfg config.TLSClientAuthConfig, coreRoot string) (*x509.CertPool, error) {
	if cfg.Disabled || strings.TrimSpace(coreRoot) == "" {
		return nil, nil
	}
	pool := x509.NewCertPool()
	channelCA := filepath.Join(coreRoot, identity.ChannelCACertFile)
	if err := addPEMFile(pool, channelCA); err != nil {
		return nil, fmt.Errorf("channel CA: %w", err)
	}
	for _, path := range cfg.TrustedPeerCAs {
		if !filepath.IsAbs(path) {
			path = filepath.Join(coreRoot, path)
		}
		if err := addPEMFile(pool, path); err != nil {
			return nil, fmt.Errorf("tls.client_auth.trusted_peer_cas: %w", err)
		}
	}
	return pool, nil
}

// addPEMFile parses one PEM file and adds its certificate to the pool.
func addPEMFile(pool *x509.CertPool, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	cert, err := identity.ParseCACertificate(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	pool.AddCert(cert)
	return nil
}

// hostOnly strips a port from a Host header value.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// writeJSONError writes the minimal {"error": message} body every
// surface's clients can parse.
func writeJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	body, err := json.Marshal(map[string]string{"error": message})
	if err != nil {
		body = []byte(`{"error":"internal error"}`)
	}
	_, _ = w.Write(body)
}
