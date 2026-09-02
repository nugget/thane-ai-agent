package listen

import (
	"crypto/sha256"
	"crypto/subtle"
)

// tokenCredential is one configured bearer token, retained only as its
// SHA-256 digest alongside the label it resolves to.
type tokenCredential struct {
	digest [sha256.Size]byte
	label  string
}

// TokenSet resolves a presented bearer token to the label it was
// configured under. It is the one comparison every bearer surface uses,
// the native API's operator tokens and the companion account tokens
// alike, so no surface can drift in posture: configured tokens are
// hashed at construction so no plaintext is retained, candidates are
// compared as fixed-size digests in constant time, and the loop never
// returns early, so a matching credential's position does not change
// the work done.
type TokenSet struct {
	credentials []tokenCredential
}

// NewTokenSet hashes a token → label index into a set. Labels need not be
// unique; a token resolves to whichever label it was configured under.
func NewTokenSet(tokens map[string]string) *TokenSet {
	credentials := make([]tokenCredential, 0, len(tokens))
	for token, label := range tokens {
		credentials = append(credentials, tokenCredential{
			digest: sha256.Sum256([]byte(token)), label: label,
		})
	}
	return &TokenSet{credentials: credentials}
}

// Len reports how many tokens the set holds.
func (s *TokenSet) Len() int {
	if s == nil {
		return 0
	}
	return len(s.credentials)
}

// Match returns the label the token authenticates, or ok=false. An empty
// token never matches, even if an empty string was configured, and a nil
// set matches nothing.
func (s *TokenSet) Match(token string) (label string, ok bool) {
	if s == nil || token == "" {
		return "", false
	}
	candidate := sha256.Sum256([]byte(token))
	matched := 0
	for _, credential := range s.credentials {
		isMatch := subtle.ConstantTimeCompare(candidate[:], credential.digest[:])
		matched |= isMatch
		if isMatch == 1 {
			label = credential.label
		}
	}
	return label, matched == 1
}
