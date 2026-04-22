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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"syscall"
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
	retryCount            int
	retryDelay            time.Duration
	retryStatuses         map[int]bool
	logger                *slog.Logger
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

// AgentSurface re-exports the standard buildinfo surface vocabulary so
// callers can opt into precise truthful disclosure without inventing
// one-off User-Agent strings.
type AgentSurface = buildinfo.AgentSurface

const (
	AgentSurfaceGeneral = buildinfo.AgentSurfaceGeneral
	AgentSurfaceForge   = buildinfo.AgentSurfaceForge
)

// WithTruthfulUserAgent uses Thane's canonical truthful User-Agent
// generation for the given standard surface.
func WithTruthfulUserAgent(surface AgentSurface) ClientOption {
	return func(c *clientConfig) { c.userAgent = buildinfo.UserAgentFor(surface) }
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

// WithRetry enables automatic retry on transient connection errors
// (e.g., EHOSTUNREACH, ENETUNREACH, connection refused). Retries are
// only attempted when the request body can be rewound, but this does
// not guarantee that the server has not already received the prior
// attempt. In practice, the retryable error set (dial/connect failures)
// occurs before any bytes reach the server.
// Designed to handle macOS ARP table race conditions (issue #53).
func WithRetry(count int, delay time.Duration) ClientOption {
	return func(c *clientConfig) {
		c.retryCount = count
		c.retryDelay = delay
	}
}

// WithRetryOnStatus extends [WithRetry] with HTTP-level retry: if the
// server returns one of the listed statuses, the transport drains and
// closes the response body and retries the request. Callers should
// still set WithRetry to configure the backoff count/delay; this option
// on its own has no effect.
//
// Typical use: cloud API clients opt in with 429 and 5xx to ride out
// transient rate-limit / upstream hiccups without bubbling them up to
// the agent loop. Local service clients (Ollama, LMStudio) don't need
// this and should omit it.
//
// The retry honors the Retry-After response header when present; the
// configured backoff delay is used only when the server doesn't supply
// one.
func WithRetryOnStatus(statuses ...int) ClientOption {
	return func(c *clientConfig) {
		if len(statuses) == 0 {
			c.retryStatuses = nil
			return
		}
		c.retryStatuses = make(map[int]bool, len(statuses))
		for _, s := range statuses {
			c.retryStatuses[s] = true
		}
	}
}

// WithLogger sets a logger for retry diagnostics.
func WithLogger(l *slog.Logger) ClientOption {
	return func(c *clientConfig) { c.logger = l }
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

	if cfg.retryCount > 0 {
		rt = &retryTransport{
			base:          rt,
			count:         cfg.retryCount,
			delay:         cfg.retryDelay,
			retryStatuses: cfg.retryStatuses,
			logger:        cfg.logger,
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

// retryTransport wraps a RoundTripper and retries on transient connection
// errors. It only retries when the request body (if any) supports rewinding
// via GetBody, ensuring safety for POST/PUT requests.
//
// When retryStatuses is non-empty, it additionally retries when the
// server returns one of the listed HTTP statuses (typically 429 and
// 5xx). Status-based retries drain and close the prior response body
// before re-sending; Retry-After is honored when present, otherwise
// the configured delay is used.
type retryTransport struct {
	base          http.RoundTripper
	count         int
	delay         time.Duration
	retryStatuses map[int]bool
	logger        *slog.Logger
}

func (t *retryTransport) shouldRetryStatus(resp *http.Response) bool {
	if resp == nil || len(t.retryStatuses) == 0 {
		return false
	}
	return t.retryStatuses[resp.StatusCode]
}

// retryAfterDelay extracts a delay from a response's Retry-After header,
// parsing either a delta-seconds integer or an HTTP-date. Returns a
// negative duration when the header is absent or unparseable so the
// caller can distinguish "no header" from "header says wait zero" and
// fall back to the configured backoff only in the former case.
func retryAfterDelay(resp *http.Response) time.Duration {
	if resp == nil {
		return -1
	}
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return -1
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(v); err == nil {
		d := time.Until(when)
		if d < 0 {
			return 0
		}
		return d
	}
	return -1
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil && !t.shouldRetryStatus(resp) {
		return resp, err
	}
	if err != nil && !isRetryableError(err) {
		return resp, err
	}

	// If request has a non-empty body, we need GetBody to rewind it for retry.
	// http.NoBody is treated as empty (common for GET/HEAD/DELETE).
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return resp, err
	}

	for attempt := 1; attempt <= t.count; attempt++ {
		lastErr := err // capture for success logging
		statusRetry := err == nil && t.shouldRetryStatus(resp)

		// Determine the backoff for this attempt. Default to the
		// configured delay; let Retry-After win for HTTP-level retries
		// when the server supplies one (including "Retry-After: 0").
		wait := t.delay
		if statusRetry {
			if d := retryAfterDelay(resp); d >= 0 {
				wait = d
			}
		}

		if t.logger != nil {
			attrs := []any{
				"method", req.Method,
				"url", req.URL.String(),
				"attempt", attempt,
				"maxRetries", t.count,
				"wait_ms", wait.Milliseconds(),
			}
			if statusRetry {
				attrs = append(attrs, "status", resp.StatusCode)
				t.logger.Debug("retrying request after transient HTTP status", attrs...)
			} else {
				attrs = append(attrs, "error", err)
				t.logger.Debug("retrying request after transient error", attrs...)
			}
		}

		// For status-based retries, drain and close the prior response
		// body so the connection is returned to the pool before we
		// re-dial.
		if statusRetry {
			DrainAndClose(resp.Body, 64*1024)
			resp = nil
		}

		// Wait before retry.
		timer := time.NewTimer(wait)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}

		// Clone the request to avoid mutating the original, per RoundTripper contract.
		retryReq := req.Clone(req.Context())
		if req.GetBody != nil {
			body, bodyErr := req.GetBody()
			if bodyErr != nil {
				return nil, fmt.Errorf("retry: rewind body: %w", bodyErr)
			}
			retryReq.Body = body
		}

		resp, err = t.base.RoundTrip(retryReq)
		switch {
		case err == nil && !t.shouldRetryStatus(resp):
			if t.logger != nil {
				attrs := []any{
					"method", req.Method,
					"url", req.URL.String(),
					"attempts", attempt + 1, // total attempts including original
				}
				if lastErr != nil {
					attrs = append(attrs, "last_error", lastErr.Error())
				}
				t.logger.Info("request succeeded after retry", attrs...)
			}
			return resp, nil
		case err != nil && !isRetryableError(err):
			return resp, err
		}
	}

	return resp, err
}

// isRetryableError returns true for transient connection-level errors
// that are likely to succeed on retry (e.g., macOS ARP race conditions).
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Check for specific syscall errors that indicate transient dial/connect
	// failures. These occur before any bytes reach the server, making retry safe.
	// ECONNRESET is intentionally excluded — it can occur after the server has
	// received and processed the request, risking duplicate side effects.
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.EHOSTUNREACH, // no route to host (ARP race)
			syscall.ENETUNREACH,  // network unreachable
			syscall.ECONNREFUSED: // connection refused (service restarting)
			return true
		}
	}

	// Check for net.OpError wrapping these.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if errors.As(opErr.Err, &errno) {
			switch errno {
			case syscall.EHOSTUNREACH, syscall.ENETUNREACH,
				syscall.ECONNREFUSED:
				return true
			}
		}
	}

	return false
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
