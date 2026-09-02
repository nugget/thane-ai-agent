package edge

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"time"
)

// Principal is the identity a verified client certificate establishes
// for a request. It is attached to the request context by the HTTPS
// front door whenever the handshake presented a certificate that chains
// to the channel CA or a trusted peer CA, and it is absent otherwise.
// Nothing consumes it yet; it is the seam later authentication layers
// read, so that a device or peer that proved a key at the transport
// does not have to prove it again at the application.
type Principal struct {
	// Subject is the certificate's subject as an RFC 4514 string.
	Subject string `json:"subject"`
	// Issuer is the issuing CA's subject as an RFC 4514 string.
	Issuer string `json:"issuer"`
	// Fingerprint is the lowercase hex SHA-256 of the leaf's DER
	// encoding, the same form the identity evidence reports for the
	// channel CA.
	Fingerprint string `json:"fingerprint"`
	// SerialNumber is the leaf's serial in decimal.
	SerialNumber string `json:"serial_number"`
	// NotAfter is the leaf's expiry.
	NotAfter time.Time `json:"not_after"`
}

type principalKey struct{}

// PrincipalFromContext returns the transport-verified principal for the
// request, if the handshake established one.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// principalFromCertificate builds the Principal a verified leaf proves.
func principalFromCertificate(leaf *x509.Certificate) Principal {
	sum := sha256.Sum256(leaf.Raw)
	return Principal{
		Subject:      leaf.Subject.String(),
		Issuer:       leaf.Issuer.String(),
		Fingerprint:  hex.EncodeToString(sum[:]),
		SerialNumber: leaf.SerialNumber.String(),
		NotAfter:     leaf.NotAfter,
	}
}

// withPrincipal attaches the Principal for a request whose TLS handshake
// verified a client chain. Only VerifiedChains counts: a certificate the
// client merely presented, without chaining to a configured CA, never
// becomes a principal.
func withPrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && len(r.TLS.VerifiedChains) > 0 && len(r.TLS.VerifiedChains[0]) > 0 {
			p := principalFromCertificate(r.TLS.VerifiedChains[0][0])
			r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
		}
		next.ServeHTTP(w, r)
	})
}
