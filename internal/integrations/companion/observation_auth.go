package companion

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/server/listen"
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

// BearerObservationAuthenticator adapts configured companion bearer tokens to
// the replaceable observation-authentication seam. Token comparison is the
// shared listen.TokenSet, so this path and the WebSocket handshake hold the
// same constant-time, digest-only posture.
type BearerObservationAuthenticator struct {
	tokens         *listen.TokenSet
	identityLookup ObservationIdentityLookup
}

// NewBearerObservationAuthenticator copies and hashes the configured token
// index so the authenticator does not retain plaintext bearer credentials.
func NewBearerObservationAuthenticator(tokenIndex map[string]string, identityLookup ObservationIdentityLookup) *BearerObservationAuthenticator {
	return &BearerObservationAuthenticator{tokens: listen.NewTokenSet(tokenIndex), identityLookup: identityLookup}
}

// AuthenticateObservation validates one bearer header, resolves the account,
// then maps today's claimed client ID to the inventory's immutable device ID.
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

	account, matched := a.tokens.Match(token)
	if !matched {
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
