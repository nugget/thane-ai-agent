package listen

import (
	"log/slog"
	"net"
	"net/http"
	"net/netip"
)

// RestrictSources refuses a request whose caller is not inside one of the
// allowed prefixes. It is the admission control for the surfaces that
// cannot ask their callers for a credential: Home Assistant's Ollama
// integration sends no bearer token and never will, so on that shim an
// address policy is the only policy there is.
//
// The source is the peer on the socket — r.RemoteAddr, which is the TLS
// connection's peer under the front door and the TCP peer on the
// plaintext listeners — and never a forwarded header. Thane no longer
// sits behind anything that would set one honestly, and a header any
// client can write is a claim, not a source. An IPv4-mapped IPv6 peer is
// unmapped before matching, so a dual-stack listener behaves the way an
// operator expects when they write 192.168.1.0/24, and a zone is dropped
// so a link-local peer matches the prefix that covers it. A refusal logs
// that matched form, so the address an operator reads out of the log is
// the one they can add to the list.
//
// An empty prefix list leaves the surface open, which is the shipped
// default: the field is opt-in and a deployment that never writes it
// sees no change. A peer that does not parse is refused, because a
// source policy that cannot see the source admits nobody.
//
// The guard does not consult the mTLS principal the front door extracts
// from a client certificate. When that principal becomes an admission
// input, a certificate-bearing peer should be admitted regardless of
// address, and that judgement belongs to a resolver that can weigh both,
// not to a guard that only knows addresses.
func RestrictSources(logger *slog.Logger, surface string, allowed []netip.Prefix, next http.Handler) http.Handler {
	if len(allowed) == 0 {
		return next
	}
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, ok := peerAddr(r.RemoteAddr)
		if !ok || !containsAddr(allowed, peer) {
			// peer is the address the guard actually matched, so an
			// operator can paste it straight into allowed_sources;
			// remote_addr keeps the raw socket string beside it, port and
			// all, for anyone correlating with the access dataset. An
			// unparseable peer logs as "invalid IP" and remote_addr shows
			// what arrived.
			logger.Warn("refused request from disallowed source",
				"surface", surface,
				"method", r.Method,
				"path", r.URL.Path,
				"peer", peer.String(),
				"remote_addr", r.RemoteAddr,
			)
			writeJSONError(w, http.StatusForbidden, "source address not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// peerAddr extracts the comparable address from an http.Request's
// RemoteAddr, which net/http fills with "host:port" for TCP listeners.
// The address is unmapped and stripped of any zone so it compares
// against prefixes written the way operators write them.
func peerAddr(remoteAddr string) (netip.Addr, bool) {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap().WithZone(""), true
}

// containsAddr reports whether any prefix covers addr.
func containsAddr(allowed []netip.Prefix, addr netip.Addr) bool {
	for _, prefix := range allowed {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
