package companion

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// TokenAuthenticator authenticates observation uploads against the
// configured companion tokens (token → account, the same index the
// WebSocket handshake uses). It implements [ObservationAuthenticator]
// as the bearer-token half of the auth seam; the enrollment arc
// (#1444) supplies a signature-based implementation later without
// touching ingestion.
type TokenAuthenticator struct {
	tokenIndex map[string]string
}

// NewTokenAuthenticator creates a bearer-token authenticator over the
// companion token index.
func NewTokenAuthenticator(tokenIndex map[string]string) *TokenAuthenticator {
	return &TokenAuthenticator{tokenIndex: tokenIndex}
}

// Authenticate resolves the Authorization header to an account.
//
// Unlike the WebSocket handshake — an in-band message on a private
// connection — this credential rides an HTTP header on every request,
// so each configured token is compared in constant time and the scan
// never breaks early on a match. What can still vary with timing is
// the number of configured tokens and their lengths, neither of which
// is secret material.
func (a *TokenAuthenticator) Authenticate(r *http.Request) (string, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return "", false
	}

	var (
		account string
		found   bool
	)
	for candidate, acct := range a.tokenIndex {
		if len(candidate) == len(token) &&
			subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
			account = acct
			found = true
		}
	}
	return account, found
}
