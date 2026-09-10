package email

import (
	"context"
	"errors"
	"fmt"
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
			return fmt.Sprintf("%s: folder %q does not exist in email account %q; folders here are [%s]. Pick one of those exactly as spelled; moves never create folders and folders are not shared across accounts", e.Op, e.Folder, account, strings.Join(e.Available, ", "))
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
	default:
		return fmt.Sprintf("%s: %v", e.Op, e.Err)
	}
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

	var netErr net.Error
	if errors.As(err, &netErr) {
		return FailureUnavailable
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return FailureUnavailable
	}
	return FailureUnclassified
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

	// DeliveryConnect marks a failure before any SMTP dialogue: dial,
	// TLS handshake, or greeting.
	DeliveryConnect DeliveryKind = "connect_failed"
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
	var netErr net.Error
	if errors.As(err, &netErr) {
		return DeliveryConnect
	}
	return DeliveryUnclassified
}
