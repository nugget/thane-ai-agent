package mcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/httpkit"
)

// httpMaxRedirects is the most redirect hops one POST may follow.
const httpMaxRedirects = 3

// newHTTPClient returns the MCP HTTP client: an httpkit transport with
// ResponseHeaderTimeout disabled and httpkit.WithTimeout(0), so the
// caller's ctx is the only bound on a request (a slow tools/call or a
// long SSE response is never cut off by a client-wide timer), and
// CheckRedirect = httpRedirectPolicy.
func newHTTPClient() *http.Client {
	t := httpkit.NewTransport()
	t.ResponseHeaderTimeout = 0
	c := httpkit.NewClient(httpkit.WithTransport(t), httpkit.WithTimeout(0))
	c.CheckRedirect = httpRedirectPolicy
	// httpkit wraps t in an unexported User-Agent round tripper that
	// does not forward CloseIdleConnections; keep a handle on t so
	// http.Client.CloseIdleConnections reaches the pool on Close.
	c.Transport = httpIdleCloser{RoundTripper: c.Transport, base: t}
	return c
}

// httpIdleCloser forwards CloseIdleConnections to the httpkit transport
// underneath the round-tripper chain.
type httpIdleCloser struct {
	http.RoundTripper
	base *http.Transport
}

// CloseIdleConnections closes the pool's idle connections.
func (c httpIdleCloser) CloseIdleConnections() { c.base.CloseIdleConnections() }

// httpRedirectPolicy allows at most httpMaxRedirects hops, each a 307 or
// 308 (which keep the method and body) to the same hostname as the
// original request with either the same scheme and port or an upgrade
// from http to https. Anything else (301/302/303, another host, a
// downgrade, a different port without upgrade) is refused with
// errProtocol so credentials in configured headers never travel to
// another origin; the transport logs the Location for the operator.
//
// http.Client returns a non-nil Response together with an error only
// when CheckRedirect refused a hop, which is how the transport tells a
// refused redirect from other failures.
func httpRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if len(via) > httpMaxRedirects {
		return fmt.Errorf("%w: refused redirect: more than %d hops", errProtocol, httpMaxRedirects)
	}
	status := 0
	if req.Response != nil {
		status = req.Response.StatusCode
	}
	if status != http.StatusTemporaryRedirect && status != http.StatusPermanentRedirect {
		return fmt.Errorf("%w: refused redirect: status %d", errProtocol, status)
	}
	if !httpSameOriginOrUpgrade(via[0].URL, req.URL) {
		return fmt.Errorf("%w: refused redirect: different origin", errProtocol)
	}
	return nil
}

// httpSameOriginOrUpgrade reports whether to is the same hostname as
// from with the same scheme and effective port, or an http to https
// upgrade of the same hostname.
func httpSameOriginOrUpgrade(from, to *url.URL) bool {
	if !strings.EqualFold(from.Hostname(), to.Hostname()) {
		return false
	}
	fromScheme, toScheme := strings.ToLower(from.Scheme), strings.ToLower(to.Scheme)
	if fromScheme == "http" && toScheme == "https" {
		return true
	}
	return fromScheme == toScheme && httpEffectivePort(from) == httpEffectivePort(to)
}

// httpEffectivePort returns u's port, or the scheme default.
func httpEffectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}

// httpRoundTripError classifies an error from http.Client.Do (or from
// reading a response body). ctx is the caller's ctx; life reports
// whether the transport was closed meanwhile.
//
//   - caller ctx done: wraps ctx.Err() (DeadlineExceeded / Canceled);
//   - refused redirect: errProtocol;
//   - transport closed mid-request: errConnectionLost;
//   - dial, DNS, proxy-connect and TLS failures: errUnreachable (the
//     request was not delivered);
//   - anything else (reset, EOF, broken stream): errConnectionLost
//     (outcome unknown).
func httpRoundTripError(ctx context.Context, lifeDone bool, method string, err error) error {
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("mcp: http %s: %w", method, ctx.Err())
	case errors.Is(err, errProtocol):
		return fmt.Errorf("mcp: http %s: %w", method, err)
	case lifeDone:
		// The cause is the transport's own abort (context.Canceled);
		// keep it for logs without letting errors.Is mistake it for
		// the caller's cancellation.
		return fmt.Errorf("%w: http %s: transport closed: %v", errConnectionLost, method, err)
	case httpNotDelivered(err):
		return fmt.Errorf("%w: http %s: %w", errUnreachable, method, err)
	default:
		return fmt.Errorf("%w: http %s: %w", errConnectionLost, method, err)
	}
}

// httpNotDelivered reports errors that happen before a request can
// reach the server: connection setup, name resolution and TLS.
func httpNotDelivered(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && (opErr.Op == "dial" || opErr.Op == "proxyconnect") {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return true
	}
	var recErr tls.RecordHeaderError
	if errors.As(err, &recErr) {
		return true
	}
	var alertErr tls.AlertError
	if errors.As(err, &alertErr) {
		return true
	}
	var (
		unknownAuth x509.UnknownAuthorityError
		hostErr     x509.HostnameError
		invalidErr  x509.CertificateInvalidError
	)
	return errors.As(err, &unknownAuth) || errors.As(err, &hostErr) || errors.As(err, &invalidErr)
}
