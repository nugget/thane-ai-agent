package companion

import (
	"crypto/sha256"
	"crypto/subtle"
)

// tokenCredential is one configured bearer token, retained only as its
// SHA-256 digest alongside the account it authenticates.
type tokenCredential struct {
	digest  [sha256.Size]byte
	account string
}

// tokenMatcher resolves a presented bearer token to its account. It is the
// single comparison both companion authentication paths use, the
// WebSocket handshake and HTTP observation ingestion, so the two cannot
// drift in posture: configured tokens are hashed at construction so no
// plaintext is retained, candidates are compared as fixed-size digests in
// constant time, and the loop never returns early, so a matching
// credential's position does not change the work done.
type tokenMatcher struct {
	credentials []tokenCredential
}

// newTokenMatcher hashes a token → account index (see
// config.CompanionConfig.TokenIndex) into a matcher.
func newTokenMatcher(tokenIndex map[string]string) tokenMatcher {
	credentials := make([]tokenCredential, 0, len(tokenIndex))
	for token, account := range tokenIndex {
		credentials = append(credentials, tokenCredential{
			digest: sha256.Sum256([]byte(token)), account: account,
		})
	}
	return tokenMatcher{credentials: credentials}
}

// match returns the account the token authenticates, or ok=false. An empty
// token never matches, even if an empty string was configured.
func (m tokenMatcher) match(token string) (account string, ok bool) {
	if token == "" {
		return "", false
	}
	candidate := sha256.Sum256([]byte(token))
	matched := 0
	for _, credential := range m.credentials {
		isMatch := subtle.ConstantTimeCompare(candidate[:], credential.digest[:])
		matched |= isMatch
		if isMatch == 1 {
			account = credential.account
		}
	}
	return account, matched == 1
}
