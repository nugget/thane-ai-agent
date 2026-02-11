// Package httpkit provides shared HTTP client construction and utilities
// for all outbound HTTP calls in Thane. It enforces consistent timeouts,
// connection management, and good-citizen defaults across all packages.
//
// Issue #53: Go's net.Dial intermittently fails on macOS with "no route to
// host" for LAN targets. The shared transport here sets explicit dial and
// TLS timeouts, limits idle connections, and provides a foundation for
// future diagnostics (GODEBUG=netdns=2, custom DialContext hooks, etc).
package httpkit

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/nugget/thane-ai-agent/internal/buildinfo"
)

// Default timeouts and connection pool limits for the shared transport.
const (
	// DefaultDialTimeout is the maximum time to establish a TCP connection.
	DefaultDialTimeout = 10 * time.Second

	// DefaultKeepAlive is the interval between TCP keep-alive probes.
	DefaultKeepAlive = 30 * time.Second

	// DefaultTLSHandshakeTimeout is the maximum time for the TLS handshake.
	DefaultTLSHandshakeTimeout = 10 * time.Second

	// DefaultResponseHeader is the maximum time to wait for response headers
	// after a request is fully written.
	DefaultResponseHeader = 15 * time.Second

	// DefaultIdleConnTimeout is how long idle connections stay in the pool.
	DefaultIdleConnTimeout = 90 * time.Second

	// DefaultMaxIdleConns is the total number of idle connections across all hosts.
	DefaultMaxIdleConns = 20

	// DefaultMaxIdleConnsPerHost is the per-host idle connection limit.
	DefaultMaxIdleConnsPerHost = 5
)

// ClientOption configures a Client built by NewClient.
type ClientOption func(*clientConfig)

type clientConfig struct {
	timeout               time.Duration
	userAgent             string
	skipUserAgent         bool
	transport             *http.Transport
	disableKeepAlives     bool
	tlsInsecureSkipVerify bool
}

// WithTimeout sets the overall request timeout on the http.Client.
// A zero value disables the timeout (useful for streaming responses).
func WithTimeout(d time.Duration) ClientOption {
	return func(c *clientConfig) { c.timeout = d }
}

// WithUserAgent overrides the default User-Agent header.
func WithUserAgent(ua string) ClientOption {
	return func(c *clientConfig) { c.userAgent = ua }
}

// WithoutUserAgent disables the automatic User-Agent roundtripper.
func WithoutUserAgent() ClientOption {
	return func(c *clientConfig) { c.skipUserAgent = true }
}

// WithTransport overrides the default shared transport.
// Use sparingly — the shared transport handles connection pooling.
func WithTransport(t *http.Transport) ClientOption {
	return func(c *clientConfig) { c.transport = t }
}

// WithDisableKeepAlives disables HTTP keep-alives on the transport.
func WithDisableKeepAlives() ClientOption {
	return func(c *clientConfig) { c.disableKeepAlives = true }
}

// WithTLSInsecureSkipVerify skips TLS certificate verification.
// Use only for local/development targets.
func WithTLSInsecureSkipVerify() ClientOption {
	return func(c *clientConfig) { c.tlsInsecureSkipVerify = true }
}

// NewTransport creates an http.Transport with sensible defaults.
// This is the foundation for all outbound connections.
func NewTransport() *http.Transport {
	return &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   DefaultDialTimeout,
			KeepAlive: DefaultKeepAlive,
		}).DialContext,
		TLSHandshakeTimeout:   DefaultTLSHandshakeTimeout,
		ResponseHeaderTimeout: DefaultResponseHeader,
		IdleConnTimeout:       DefaultIdleConnTimeout,
		MaxIdleConns:          DefaultMaxIdleConns,
		MaxIdleConnsPerHost:   DefaultMaxIdleConnsPerHost,
		ForceAttemptHTTP2:     true,
	}
}

// NewClient builds an *http.Client with the shared transport and
// good-citizen defaults (timeouts, User-Agent, connection limits).
func NewClient(opts ...ClientOption) *http.Client {
	cfg := &clientConfig{
		timeout:   30 * time.Second,
		userAgent: buildinfo.UserAgent(),
	}
	for _, o := range opts {
		o(cfg)
	}

	t := cfg.transport
	if t == nil {
		t = NewTransport()
	}

	if cfg.disableKeepAlives {
		t.DisableKeepAlives = true
	}

	if cfg.tlsInsecureSkipVerify {
		if t.TLSClientConfig == nil {
			t.TLSClientConfig = &tls.Config{}
		}
		t.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // explicit opt-in
	}

	var rt http.RoundTripper = t
	if !cfg.skipUserAgent {
		rt = &userAgentTransport{
			base: t,
			ua:   cfg.userAgent,
		}
	}

	return &http.Client{
		Timeout:   cfg.timeout,
		Transport: rt,
	}
}

// userAgentTransport injects the User-Agent header on every request
// unless one is already set.
type userAgentTransport struct {
	base http.RoundTripper
	ua   string
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		// Clone the request to avoid mutating the original, per RoundTripper contract.
		req = req.Clone(req.Context())
		req.Header.Set("User-Agent", t.ua)
	}
	return t.base.RoundTrip(req)
}

// DrainAndClose reads up to limit bytes from rc and closes it.
// Use to ensure HTTP connections are returned to the pool.
func DrainAndClose(rc io.ReadCloser, limit int64) {
	if rc == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, limit))
	rc.Close()
}

// ReadErrorBody reads up to limit bytes from rc for error messages,
// then drains and closes the remainder to allow connection reuse.
// Returns an empty string if rc is nil.
func ReadErrorBody(rc io.ReadCloser, limit int64) string {
	if rc == nil {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(rc, limit))
	// Drain remainder so the connection can be reused, then close.
	DrainAndClose(rc, 1024)
	if err != nil {
		return fmt.Sprintf("(failed to read error body: %v)", err)
	}
	return string(body)
}
