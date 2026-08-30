package companion

import (
	"crypto/sha256"
	"crypto/subtle"
	"strings"
)

// ObservationPrincipal is the authenticated ownership boundary applied to an
// observation upload. DeviceIdentity is opaque to storage: bearer auth uses
// today's claimed client_id, while a future key authenticator can return a
// verified key fingerprint without changing the ingestion service or schema.
type ObservationPrincipal struct {
	Account        string
	DeviceIdentity string
}

// ObservationAuthenticator resolves an HTTP credential and the request's
// claimed device identity into the server-owned ingestion principal.
type ObservationAuthenticator interface {
	AuthenticateObservation(authorizationHeader, claimedDeviceIdentity string) (ObservationPrincipal, bool)
}

type bearerObservationCredential struct {
	digest  [sha256.Size]byte
	account string
}

// BearerObservationAuthenticator adapts configured companion bearer tokens to
// the replaceable observation-authentication seam. Configured and candidate
// tokens are compared as fixed-size SHA-256 digests in constant time.
type BearerObservationAuthenticator struct {
	credentials []bearerObservationCredential
}

// NewBearerObservationAuthenticator copies and hashes the configured token
// index so the authenticator does not retain plaintext bearer credentials.
func NewBearerObservationAuthenticator(tokenIndex map[string]string) *BearerObservationAuthenticator {
	credentials := make([]bearerObservationCredential, 0, len(tokenIndex))
	for token, account := range tokenIndex {
		credentials = append(credentials, bearerObservationCredential{
			digest: sha256.Sum256([]byte(token)), account: account,
		})
	}
	return &BearerObservationAuthenticator{credentials: credentials}
}

// AuthenticateObservation validates one bearer header and binds the claimed
// client ID as today's opaque device identity. The loop never returns early,
// so a matching credential's position does not affect the comparison count.
func (a *BearerObservationAuthenticator) AuthenticateObservation(authorizationHeader, claimedDeviceIdentity string) (ObservationPrincipal, bool) {
	scheme, token, ok := strings.Cut(authorizationHeader, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.TrimSpace(token) != token {
		return ObservationPrincipal{}, false
	}
	claimedDeviceIdentity = strings.TrimSpace(claimedDeviceIdentity)
	if claimedDeviceIdentity == "" {
		return ObservationPrincipal{}, false
	}

	candidate := sha256.Sum256([]byte(token))
	matched := 0
	account := ""
	for _, credential := range a.credentials {
		isMatch := subtle.ConstantTimeCompare(candidate[:], credential.digest[:])
		matched |= isMatch
		if isMatch == 1 {
			account = credential.account
		}
	}
	if matched != 1 {
		return ObservationPrincipal{}, false
	}
	return ObservationPrincipal{Account: account, DeviceIdentity: claimedDeviceIdentity}, true
}
