package companion

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrObservationUnauthorized is returned for a missing or invalid bearer
// credential, client claim, or durable inventory mapping.
var ErrObservationUnauthorized = errors.New("companion observation unauthorized")

// ObservationAuthRequest is the bounded HTTP material an authenticator may
// verify. Scheme and Authority retain the request components that Go keeps
// outside Header; TargetURI is their canonical absolute composition with the
// exact request target. Together with the exact body, this allows a future RFC
// 9421 authenticator to replace bearer auth without changing ingestion.
type ObservationAuthRequest struct {
	Method          string
	Scheme          string
	Authority       string
	RequestTarget   string
	TargetURI       string
	Header          http.Header
	Body            []byte
	ClaimedClientID string
}

// ObservationAuthenticator resolves HTTP credentials and a request claim into
// the server-owned ingestion principal.
type ObservationAuthenticator interface {
	AuthenticateObservation(context.Context, ObservationAuthRequest) (ObservationPrincipal, error)
}

type bearerObservationCredential struct {
	digest  [sha256.Size]byte
	account string
}

// BearerObservationAuthenticator adapts configured companion bearer tokens to
// the replaceable observation-authentication seam. Configured and candidate
// tokens are compared as fixed-size SHA-256 digests in constant time.
type BearerObservationAuthenticator struct {
	credentials    []bearerObservationCredential
	identityLookup ObservationIdentityLookup
}

// NewBearerObservationAuthenticator copies and hashes the configured token
// index so the authenticator does not retain plaintext bearer credentials.
func NewBearerObservationAuthenticator(tokenIndex map[string]string, identityLookup ObservationIdentityLookup) *BearerObservationAuthenticator {
	credentials := make([]bearerObservationCredential, 0, len(tokenIndex))
	for token, account := range tokenIndex {
		credentials = append(credentials, bearerObservationCredential{
			digest: sha256.Sum256([]byte(token)), account: account,
		})
	}
	return &BearerObservationAuthenticator{credentials: credentials, identityLookup: identityLookup}
}

// AuthenticateObservation validates one bearer header, resolves the account,
// then maps today's claimed client ID to the inventory's immutable device ID.
// The comparison loop never returns early, so a matching credential's
// position does not affect the number of constant-time comparisons.
func (a *BearerObservationAuthenticator) AuthenticateObservation(
	ctx context.Context,
	request ObservationAuthRequest,
) (ObservationPrincipal, error) {
	scheme, token, ok := strings.Cut(request.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.TrimSpace(token) != token {
		return ObservationPrincipal{}, ErrObservationUnauthorized
	}
	claimedClientID := strings.TrimSpace(request.ClaimedClientID)
	if claimedClientID == "" {
		return ObservationPrincipal{}, ErrObservationUnauthorized
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
		// Deliberately avoid a dummy SQLite lookup for invalid bearer tokens.
		// This temporary bearer path is restricted to Thane's private-network
		// boundary; dummy database work would amplify unauthenticated traffic
		// without making the full HTTP exchange constant-time. Per-device
		// signatures in #1444 replace this shared-secret posture.
		return ObservationPrincipal{}, ErrObservationUnauthorized
	}
	if a.identityLookup == nil {
		return ObservationPrincipal{}, fmt.Errorf("resolve companion observation identity: resolver unavailable")
	}
	deviceID, found, err := a.identityLookup(ctx, account, claimedClientID)
	if err != nil {
		return ObservationPrincipal{}, fmt.Errorf("resolve companion observation identity: %w", err)
	}
	if !found || deviceID == "" {
		return ObservationPrincipal{}, ErrObservationUnauthorized
	}
	return ObservationPrincipal{Account: account, DeviceID: deviceID}, nil
}
