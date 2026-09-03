package edge

import (
	"crypto/tls"
	"maps"
	"slices"
	"strings"
	"time"
)

// unheldNameLogInterval coalesces the warning for a ClientHello naming a
// hostname this door does not hold. One misconfigured client retries in
// a tight loop and a scanner walks names by the thousand; either would
// otherwise put a WARN record on the wire per handshake attempt, which
// is how a diagnostic becomes a noise storm.
const unheldNameLogInterval = 30 * time.Second

// unheldNameSampleLimit bounds how many distinct names one coalesced
// summary carries. The names are the whole diagnostic — an internal
// hostname is a misconfigured client, an unrelated domain is a scanner —
// but the set is chosen by whoever is connecting, so it cannot be
// allowed to grow with them.
const unheldNameSampleLimit = 8

// certificateFor wraps the certificate callback so a ClientHello naming
// a hostname this door does not hold is refused as exactly that.
//
// Two unrelated failures used to be indistinguishable from outside. A
// name the door was never configured to serve, and a name it holds whose
// certificate has not been issued yet, both ended as an error out of the
// certificate cache — and an error there makes the Go TLS server send
// internal_error, an alert that says "this server is broken" for what is
// usually "you asked the wrong door". Nothing on this side logged it
// either, so the only evidence lived in the client's error message.
// Reaching Thane at a hostname it does not serve is answered at HTTP
// with 421 and a warning; the handshake is the same misdirection one
// layer down and should read the same way.
//
// An unheld name therefore returns no certificate and no error, which is
// what makes the standard library send unrecognized_name (RFC 6066,
// alert 112) instead: this Config carries no static certificates, so a
// nil certificate from this callback is read as "this server has no
// certificate for that name", which is precisely the claim being made.
// A held name still goes to certmagic and still fails as an internal
// error when its certificate is missing, because that one really is an
// internal condition — the door was told to hold that name and does not.
func (s *Server) certificateFor(base func(*tls.ClientHelloInfo) (*tls.Certificate, error)) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		if _, ok := s.held[normalizeServerName(hello.ServerName)]; ok {
			return base(hello)
		}
		s.recordUnheldName(hello)
		return nil, nil
	}
}

// normalizeServerName lowercases a ClientHello's server name and drops
// the trailing dot a fully qualified name may carry, matching how
// certmagic normalizes the names it manages and how routeByHost
// normalizes the Host header. Configured hostnames are already lowercase
// and dotless — [config.validateTLSHostname] refuses anything else — so
// this normalization only ever moves a client's spelling toward theirs.
func normalizeServerName(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

// recordUnheldName warns that a handshake asked for a name this door
// does not hold, coalescing bursts. The first refusal after a quiet
// stretch logs immediately and names both the hostname asked for and the
// client that asked, which is the whole diagnostic. Refusals inside the
// window fold into one summary carrying the count and a bounded sample
// of the names, and a tail flush guarantees that summary lands within
// one interval of a burst's last refusal rather than waiting for a
// caller who may never come back.
func (s *Server) recordUnheldName(hello *tls.ClientHelloInfo) {
	name := normalizeServerName(hello.ServerName)
	if name == "" {
		// A client that sent no SNI at all cannot be told which name it
		// should have asked for, but it is the same refusal and belongs
		// in the same count.
		name = "(none)"
	}
	var remote string
	if hello.Conn != nil {
		remote = hello.Conn.RemoteAddr().String()
	}

	s.sniMu.Lock()
	defer s.sniMu.Unlock()

	s.sniRefused++
	now := time.Now()
	if now.Sub(s.sniLastLog) >= unheldNameLogInterval {
		s.logger.Warn("refused a TLS handshake for a hostname this door does not hold",
			"server_name", name,
			"remote", remote,
			"hostnames", s.hostnames,
			"refused_total", s.sniRefused)
		s.sniLogged = s.sniRefused
		s.sniLastLog = now
		return
	}

	if s.sniNames == nil {
		s.sniNames = make(map[string]struct{}, unheldNameSampleLimit)
	}
	if len(s.sniNames) < unheldNameSampleLimit {
		s.sniNames[name] = struct{}{}
	}
	if s.sniFlush == nil {
		s.sniFlush = time.AfterFunc(unheldNameLogInterval-now.Sub(s.sniLastLog), s.flushUnheldNames)
	}
}

// flushUnheldNames emits the coalesced summary for refusals that folded
// into a quiet window with no later refusal to surface them: the tail of
// a finite burst.
func (s *Server) flushUnheldNames() {
	s.sniMu.Lock()
	defer s.sniMu.Unlock()

	s.sniFlush = nil
	if s.sniRefused == s.sniLogged {
		return
	}
	names := slices.Sorted(maps.Keys(s.sniNames))
	s.logger.Warn("refused TLS handshakes for hostnames this door does not hold",
		"server_names", names,
		"refused_since_last", s.sniRefused-s.sniLogged,
		"refused_total", s.sniRefused)
	s.sniNames = nil
	s.sniLogged = s.sniRefused
	s.sniLastLog = time.Now()
}

// stopUnheldNameFlush cancels a pending tail flush so a shutting-down
// door does not log after its listeners are gone.
func (s *Server) stopUnheldNameFlush() {
	s.sniMu.Lock()
	defer s.sniMu.Unlock()
	if s.sniFlush != nil {
		s.sniFlush.Stop()
		s.sniFlush = nil
	}
}
