package email

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strings"

	"github.com/emersion/go-imap/v2"
)

// FailureKind classifies an IMAP failure by what the caller should do
// differently. A model reading a failure needs to know whether the
// wall is permanent (stop and escalate), a selection problem (change
// an argument), or transient (wait and retry), because the recovery
// for each is different and the raw server text does not say which.
type FailureKind string

const (
	// FailureUnclassified marks a failure carrying no recognized
	// signal. It renders as plain "op: err".
	FailureUnclassified FailureKind = ""

	// FailureAuthentication marks a rejected login. Every operation
	// on the account fails until the operator fixes the credential.
	FailureAuthentication FailureKind = "authentication_failed"

	// FailureFolderNotFound marks a folder the account does not have.
	// [ClientError.Available] lists the folders that exist.
	FailureFolderNotFound FailureKind = "folder_not_found"

	// FailureMessageNotFound marks a UID that does not exist in the
	// named folder of the named account. UIDs are scoped to both, so
	// the usual cause is a UID carried across a folder or account.
	FailureMessageNotFound FailureKind = "message_not_found"

	// FailurePermission marks an operation the server's ACL refuses.
	FailurePermission FailureKind = "permission_denied"

	// FailureQuota marks a store that is full.
	FailureQuota FailureKind = "over_quota"

	// FailureUnavailable marks a transport or server fault that a
	// later retry may clear: connection refused, reset, timed out, or
	// the server saying UNAVAILABLE.
	FailureUnavailable FailureKind = "unavailable"

	// FailureCancelled marks an operation abandoned because the
	// caller's context ended.
	FailureCancelled FailureKind = "cancelled"

	// FailureTimeout marks an operation the connection watchdog ended
	// because the server stopped answering mid-command. The connection
	// was dropped; the next call reconnects.
	FailureTimeout FailureKind = "timed_out"

	// FailureTLS marks a connection that could not be made secure: the
	// server does not offer STARTTLS on a plaintext port, or its
	// certificate failed verification. Retrying will fail the same
	// way; the operator must fix the server or the account's tls
	// setting.
	FailureTLS FailureKind = "tls_failed"

	// FailureUnsupported marks an operation the server lacks the
	// extensions to perform safely. Nothing was changed.
	FailureUnsupported FailureKind = "unsupported"
)

// ClientError is an IMAP operation failure with the account and folder
// it happened in and the classification the caller should act on. Its
// text follows the teaching-error contract in
// docs/model-facing-tools.md: it names the scope, says whether retry
// can help, and names the next move.
type ClientError struct {
	// Account is the configured account name the operation ran under.
	Account string

	// Op is the operation label, for example "select folder" or
	// "move messages".
	Op string

	// Folder is the folder the operation targeted, when relevant.
	Folder string

	// UID is the message UID the operation targeted, when relevant.
	UID uint32

	// Kind is the classification.
	Kind FailureKind

	// Available lists the account's folders when Kind is
	// [FailureFolderNotFound], so the caller can pick a real one.
	Available []string

	// Err is the underlying failure.
	Err error
}

func (e *ClientError) Unwrap() error { return e.Err }

// Error renders the model-facing message.
func (e *ClientError) Error() string {
	account := e.Account
	if account == "" {
		account = "(default)"
	}
	switch e.Kind {
	case FailureAuthentication:
		return fmt.Sprintf("%s: email account %q rejected the login. Every operation on this account fails until the operator fixes the credential; do not retry, and say so plainly if the work depended on it (%v)", e.Op, account, e.Err)
	case FailureFolderNotFound:
		if len(e.Available) > 0 {
			return fmt.Sprintf("%s: folder %q does not exist in email account %q; folders here are [%s]. Pick one of those exactly as spelled; moves never create folders and folders are not shared across accounts", e.Op, e.Folder, account, listFolders(e.Available))
		}
		return fmt.Sprintf("%s: folder %q does not exist in email account %q. Call email_folders for this account and pick a name from the result; folders are not shared across accounts", e.Op, e.Folder, account)
	case FailureMessageNotFound:
		return fmt.Sprintf("%s: no message with uid=%d in email account %q folder %q. UIDs are scoped to one folder of one account, so pass the account and folder the UID was listed from (a wake event carries both), or list the folder again to get current UIDs", e.Op, e.UID, account, e.Folder)
	case FailurePermission:
		return fmt.Sprintf("%s: email account %q is not permitted to do this on folder %q; the server's access control refused it. Retrying will fail the same way; report it rather than working around it (%v)", e.Op, account, e.Folder, e.Err)
	case FailureQuota:
		return fmt.Sprintf("%s: email account %q is over its storage quota. Nothing can be appended until space is freed; this is not transient (%v)", e.Op, account, e.Err)
	case FailureUnavailable:
		return fmt.Sprintf("%s: email account %q is unreachable or the server refused the connection. This is transient; retry later rather than immediately (%v)", e.Op, account, e.Err)
	case FailureCancelled:
		return fmt.Sprintf("%s: the operation on email account %q was cancelled before it completed (%v)", e.Op, account, e.Err)
	case FailureTimeout:
		return fmt.Sprintf("%s: the IMAP server for email account %q stopped answering mid-command and the connection was dropped. This is transient; retry later rather than immediately (%v)", e.Op, account, e.Err)
	case FailureTLS:
		return fmt.Sprintf("%s: email account %q could not be connected securely (%v). Retrying will fail the same way; the operator must fix the server's TLS or the account's tls setting, and no credential was sent", e.Op, account, e.Err)
	case FailureUnsupported:
		return fmt.Sprintf("%s: the IMAP server for email account %q lacks the extensions to do this safely, so nothing was changed. Retrying will fail the same way; report it (%v)", e.Op, account, e.Err)
	default:
		return fmt.Sprintf("%s: %v", e.Op, e.Err)
	}
}

// listFolders renders a folder list for error text, capped so a
// label-heavy mailbox does not turn one refusal into a page.
func listFolders(names []string) string {
	if len(names) <= maxListedFolders {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s, … and %d more; call email_folders for the full list", strings.Join(names[:maxListedFolders], ", "), len(names)-maxListedFolders)
}

// FailureKindOf returns the classification carried by err, or
// [FailureUnclassified] when err is not a classified client failure.
func FailureKindOf(err error) FailureKind {
	var cErr *ClientError
	if !errors.As(err, &cErr) {
		return FailureUnclassified
	}
	return cErr.Kind
}

// classifyIMAPError maps a go-imap or transport failure to a
// [FailureKind]. Context errors are checked first because a cancelled
// operation surfaces as a closed connection, which would otherwise
// read as an outage.
func classifyIMAPError(ctx context.Context, err error) FailureKind {
	if err == nil {
		return FailureUnclassified
	}
	if ctx != nil && ctx.Err() != nil {
		return FailureCancelled
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return FailureCancelled
	}

	var imapErr *imap.Error
	if errors.As(err, &imapErr) {
		switch imapErr.Code {
		case imap.ResponseCodeAuthenticationFailed, imap.ResponseCodeAuthorizationFailed, imap.ResponseCodeExpired:
			return FailureAuthentication
		case imap.ResponseCodeNonExistent, imap.ResponseCodeTryCreate:
			return FailureFolderNotFound
		case imap.ResponseCodeNoPerm:
			return FailurePermission
		case imap.ResponseCodeOverQuota:
			return FailureQuota
		case imap.ResponseCodeUnavailable, imap.ResponseCodeInUse, imap.ResponseCodeServerBug:
			return FailureUnavailable
		}
		return FailureUnclassified
	}

	if isTLSError(err) {
		return FailureTLS
	}
	// A connection the server (or the watchdog) closed mid-command
	// surfaces as the reader's EOF, not as a net.Error.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return FailureUnavailable
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return FailureUnavailable
	}
	return FailureUnclassified
}

// isTLSError reports whether err is a certificate or record failure
// from crypto/tls: a chain that does not verify, a name that does not
// match, or a server speaking plaintext where TLS was expected.
func isTLSError(err error) bool {
	var certErr *tls.CertificateVerificationError
	var recordErr tls.RecordHeaderError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &certErr) || errors.As(err, &recordErr) || errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalid)
}

// DeliveryKind classifies an SMTP failure. The distinction that
// matters to a caller is permanent versus transient: a 5xx reply will
// recur on retry and a 4xx may not.
type DeliveryKind string

const (
	// DeliveryUnclassified marks a failure with no SMTP reply code.
	DeliveryUnclassified DeliveryKind = ""

	// DeliveryAuthentication marks a rejected SMTP login.
	DeliveryAuthentication DeliveryKind = "authentication_failed"

	// DeliveryPermanent marks a 5xx reply: the server refused the
	// message or a recipient and will do so again.
	DeliveryPermanent DeliveryKind = "permanent_reject"

	// DeliveryTransient marks a 4xx reply: try again later.
	DeliveryTransient DeliveryKind = "transient_reject"

	// DeliveryConnect marks a failure before any SMTP dialogue: dial
	// or greeting. Transient.
	DeliveryConnect DeliveryKind = "connect_failed"

	// DeliveryTLS marks a connection that could not be made secure:
	// the server does not offer STARTTLS, or its certificate failed
	// verification. Permanent until the operator acts; no credential
	// was sent.
	DeliveryTLS DeliveryKind = "tls_failed"
)

// DeliveryError is an SMTP failure with the account that was sending
// and the classification the caller should act on.
type DeliveryError struct {
	// Account is the configured account name.
	Account string

	// Op is the SMTP step that failed, for example "RCPT TO
	// bob@example.com" or "STARTTLS".
	Op string

	// Kind is the classification.
	Kind DeliveryKind

	// Err is the underlying failure.
	Err error
}

func (e *DeliveryError) Unwrap() error { return e.Err }

// Error renders the model-facing message.
func (e *DeliveryError) Error() string {
	account := e.Account
	if account == "" {
		account = "(default)"
	}
	switch e.Kind {
	case DeliveryAuthentication:
		return fmt.Sprintf("%s: the SMTP server for email account %q rejected the login. Nothing can be sent from this account until the operator fixes the credential; do not retry (%v)", e.Op, account, e.Err)
	case DeliveryPermanent:
		return fmt.Sprintf("%s: the SMTP server for email account %q permanently refused the message. Retrying the same message will fail the same way; check the recipient address and report the refusal (%v)", e.Op, account, e.Err)
	case DeliveryTransient:
		return fmt.Sprintf("%s: the SMTP server for email account %q temporarily refused the message. This is transient; retry later rather than immediately (%v)", e.Op, account, e.Err)
	case DeliveryConnect:
		return fmt.Sprintf("%s: could not reach the SMTP server for email account %q. This is transient; retry later rather than immediately (%v)", e.Op, account, e.Err)
	case DeliveryTLS:
		return fmt.Sprintf("%s: the SMTP server for email account %q could not be used securely (%v). Retrying will fail the same way; the operator must fix smtp.starttls, the port, or the server certificate, and no credential was sent", e.Op, account, e.Err)
	default:
		return fmt.Sprintf("%s: %v", e.Op, e.Err)
	}
}

// DeliveryKindOf returns the classification carried by err, or
// [DeliveryUnclassified] when err is not a classified delivery failure.
func DeliveryKindOf(err error) DeliveryKind {
	var dErr *DeliveryError
	if !errors.As(err, &dErr) {
		return DeliveryUnclassified
	}
	return dErr.Kind
}

// classifySMTPError maps a net/smtp failure to a [DeliveryKind] from
// its reply code.
func classifySMTPError(err error) DeliveryKind {
	if err == nil {
		return DeliveryUnclassified
	}
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) {
		switch {
		case tpErr.Code == 530, tpErr.Code == 534, tpErr.Code == 535, tpErr.Code == 538:
			return DeliveryAuthentication
		case tpErr.Code >= 500:
			return DeliveryPermanent
		case tpErr.Code >= 400:
			return DeliveryTransient
		}
		return DeliveryUnclassified
	}
	if isTLSError(err) {
		return DeliveryTLS
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return DeliveryConnect
	}
	return DeliveryUnclassified
}
