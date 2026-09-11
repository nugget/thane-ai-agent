package email

import "context"

// AuthMethod names the mechanism behind an [Authentication] result.
type AuthMethod string

// Authentication methods. Only [AuthMethodSMIME] and [AuthMethodPGP]
// bind a message to a person; [AuthMethodDKIM] binds it to a signing
// domain and can never by itself set Verified.
const (
	AuthMethodNone  AuthMethod = "none"
	AuthMethodDKIM  AuthMethod = "dkim"
	AuthMethodSMIME AuthMethod = "smime"
	AuthMethodPGP   AuthMethod = "pgp"
)

// AuthStatus keeps "could not check" apart from "checked and failed",
// the same three-way distinction the provenance package draws for
// commit signatures: an unfinished check says nothing about whether the
// message is genuine, and rendering it as a failure would teach the
// model to treat every unsigned message as forged.
type AuthStatus string

// Authentication statuses.
const (
	// AuthVerified means a signature was validated against a key the
	// directory holds for the sender.
	AuthVerified AuthStatus = "verified"

	// AuthFailed means a signature was present and did not validate, or
	// validated against a key that does not belong to the From address.
	AuthFailed AuthStatus = "failed"

	// AuthUnavailable means a check was attempted but could not
	// complete: no key on file, the raw message was truncated, a
	// lookup timed out.
	AuthUnavailable AuthStatus = "unavailable"

	// AuthAbsent means nothing was checked, because the message carries
	// no signature or no authenticator is configured. This is the
	// steady state today and carries no suspicion.
	AuthAbsent AuthStatus = "absent"
)

// Authentication is what an [Authenticator] concluded about one
// inbound message. Its invariant is that Verified is true only when
// Thane itself validated a signature over the message with a key the
// contact directory holds for the contact whose address matches the
// RFC 5322 From — an S/MIME certificate's rfc822Name or an OpenPGP User
// ID. Domain-level results (DKIM, DMARC) and any header a receiving
// mail server wrote never set it; when such hints ship they render
// under a separate field, not here.
type Authentication struct {
	Method AuthMethod `json:"method"`
	Status AuthStatus `json:"status"`

	// Verified is true only when Status is [AuthVerified].
	Verified bool `json:"verified"`

	// Principal is the identity the signature bound: an address for
	// S/MIME and PGP, a domain for DKIM.
	Principal string `json:"principal,omitempty"`

	// PrincipalKind is "address" or "domain".
	PrincipalKind string `json:"principal_kind,omitempty"`

	// KeyFingerprint identifies the key or certificate used, as
	// "sha256:<hex>".
	KeyFingerprint string `json:"key_fingerprint,omitempty"`

	// Reason explains a failed or unavailable result in one sentence.
	Reason string `json:"reason,omitempty"`
}

// AbsentAuthentication is the result for a message nothing checked.
func AbsentAuthentication() Authentication {
	return Authentication{Method: AuthMethodNone, Status: AuthAbsent}
}

// normalize enforces the Verified invariant on a result an
// implementation returned, so a buggy authenticator cannot claim
// verification without the status to match.
func (a Authentication) normalize() Authentication {
	if a.Method == "" {
		a.Method = AuthMethodNone
	}
	if a.Status == "" {
		a.Status = AuthAbsent
	}
	a.Verified = a.Status == AuthVerified && a.Method != AuthMethodNone && a.Method != AuthMethodDKIM
	if !a.Verified && a.Status == AuthVerified {
		a.Status = AuthFailed
		if a.Reason == "" {
			a.Reason = "verification claimed by a method that cannot bind a message to a person"
		}
	}
	return a
}

// InboundMessage is the bounded material an [Authenticator] may
// inspect: the complete raw message as fetched, with CRLF line endings
// preserved, and the address the trust decision is about.
type InboundMessage struct {
	// Account is the receiving account.
	Account string

	// Raw is the RFC 5322 message as received, at most
	// [maxRawMessageSize] bytes.
	Raw []byte

	// Truncated is true when Raw hit the cap; a signature over the
	// whole message is then uncheckable and the result should be
	// [AuthUnavailable], never [AuthFailed].
	Truncated bool

	// From is the RFC 5322 From mailbox the signature must bind to.
	From Address
}

// Authenticator checks an inbound message's signature. Implementations
// arrive with S/MIME and OpenPGP support (#317); until then the
// service uses none and every message reads as [AuthAbsent]. An
// unverifiable message is a result, not an error: the error return is
// for programmer and I/O faults only.
type Authenticator interface {
	Authenticate(ctx context.Context, msg InboundMessage) (Authentication, error)
}

// OutboundMessage is what a [Signer] receives after composition.
type OutboundMessage struct {
	// Account is the sending account.
	Account string

	// From is the mailbox the signature must bind to.
	From Address

	// Message is the complete RFC 5322 message to sign.
	Message []byte
}

// Signer signs an outbound message and returns the complete message
// to deliver. It may restructure the body into multipart/signed but
// must preserve the top-level headers (Message-ID, In-Reply-To,
// References, Date), which the sender records before signing.
type Signer interface {
	Sign(ctx context.Context, msg OutboundMessage) ([]byte, error)
}

// SignerResolver picks the signer for an account, or nil to send
// unsigned. Signing is per account because keys are.
type SignerResolver interface {
	SignerFor(account string) Signer
}
