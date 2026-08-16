package forge

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/google/go-github/v69/github"
)

// DenialKind classifies a failed forge request by what the caller must
// do differently. Multi-account deployments give each account a token
// with its own access policy, so a denial is a designed-for outcome
// rather than a fault: an account provisioned for observation will be
// refused when it attempts a write, and that refusal is the boundary
// working. What a model cannot recover from is a refusal it mistakes
// for a transient failure, because the recovery for the two is
// opposite — a permanent wall answers "stop and escalate" while a
// transient one answers "wait and retry".
type DenialKind string

const (
	// DenialNone marks a failure carrying no forge-level authorization
	// signal: a transport error, a malformed request, or any upstream
	// fault. These render exactly as they always have.
	DenialNone DenialKind = ""

	// DenialForbidden marks a request the account's token is not
	// permitted to make. Retrying cannot succeed.
	DenialForbidden DenialKind = "forbidden"

	// DenialUnauthenticated marks a token the forge rejected outright:
	// expired, revoked, or malformed. Every call on the account fails
	// until the operator replaces the credential, so it is worth
	// distinguishing from a per-operation denial.
	DenialUnauthenticated DenialKind = "unauthenticated"

	// DenialInvisible marks a not-found on an account whose token may
	// simply not see the resource. GitHub deliberately answers
	// unauthorized reads with 404 rather than 403 so that private
	// resources do not leak their existence, which means absence and
	// invisibility are indistinguishable from the response alone. A
	// scoped token therefore sees phantom-missing repositories, and a
	// model told only "not found" will conclude the repository was
	// deleted.
	DenialInvisible DenialKind = "not_found_or_invisible"

	// DenialRateLimited marks an exhausted primary rate limit or a
	// secondary abuse limit. Transient by construction.
	DenialRateLimited DenialKind = "rate_limited"
)

// ProviderError wraps a forge API failure with the account that made
// the request and the classification the caller should act on. It
// implements the teaching-error contract in docs/model-facing-tools.md:
// the text names which account hit the wall, whether the wall is
// permanent, and what the next move is.
type ProviderError struct {
	// Account is the configured forge account name whose token made
	// the request.
	Account string

	// Op is the operation label, e.g. "get repository" or
	// "submit review on PR #12".
	Op string

	// Kind is the classification. [DenialNone] means the failure said
	// nothing about authorization.
	Kind DenialKind

	// Err is the underlying provider error.
	Err error
}

func (e *ProviderError) Unwrap() error { return e.Err }

// Error renders the model-facing message. Unclassified failures keep
// the plain "op: err" shape they have always had; classified ones add
// the account, the nature of the wall, and the retry semantics.
func (e *ProviderError) Error() string {
	account := e.Account
	if account == "" {
		account = "(default)"
	}

	switch e.Kind {
	case DenialForbidden:
		return fmt.Sprintf(
			"%s: denied for forge account %q. This is the token's access policy, not a transient failure — retrying, rewording, or trying another repository under the same account will fail the same way. Use an account authorized for this operation if one is configured, otherwise report the denial rather than working around it (%v)",
			e.Op, account, e.Err)

	case DenialUnauthenticated:
		return fmt.Sprintf(
			"%s: forge account %q was not authenticated — its token is expired, revoked, or malformed. Every operation on this account fails until the operator replaces the credential; do not retry, and say so plainly if the work depended on it (%v)",
			e.Op, account, e.Err)

	case DenialInvisible:
		return fmt.Sprintf(
			"%s: not found under forge account %q. The forge answers unauthorized reads with not-found, so the resource either does not exist or lies outside this token's visibility — verify the name, and if it is right, treat the resource as out of scope for this account instead of retrying (%v)",
			e.Op, account, e.Err)

	case DenialRateLimited:
		return fmt.Sprintf(
			"%s: forge account %q is rate limited. This is transient — wait for the window to reset rather than retrying immediately, and prefer fewer, wider calls when you resume (%v)",
			e.Op, account, e.Err)

	default:
		return fmt.Sprintf("%s: %v", e.Op, e.Err)
	}
}

// Denied reports whether err is a permanent authorization wall — a
// forbidden operation or an unusable token. Callers use it to decide
// between escalating and retrying. Rate limits and invisibility are
// deliberately excluded: the first is transient, and the second may be
// simple absence.
func Denied(err error) bool {
	var pErr *ProviderError
	if !errors.As(err, &pErr) {
		return false
	}
	return pErr.Kind == DenialForbidden || pErr.Kind == DenialUnauthenticated
}

// DenialKindOf returns the classification carried by err, or
// [DenialNone] when err is not a classified provider failure.
func DenialKindOf(err error) DenialKind {
	var pErr *ProviderError
	if !errors.As(err, &pErr) {
		return DenialNone
	}
	return pErr.Kind
}

// classifyGitHubError maps a go-github failure to its [DenialKind].
// Rate-limit errors are checked before the generic status mapping
// because the API reports both primary and secondary limits as 403,
// and conflating a limit with an ACL wall would tell the model to stop
// when it should wait.
func classifyGitHubError(err error) DenialKind {
	if err == nil {
		return DenialNone
	}

	var rateErr *github.RateLimitError
	if errors.As(err, &rateErr) {
		return DenialRateLimited
	}
	var abuseErr *github.AbuseRateLimitError
	if errors.As(err, &abuseErr) {
		return DenialRateLimited
	}

	var respErr *github.ErrorResponse
	if !errors.As(err, &respErr) || respErr.Response == nil {
		return DenialNone
	}

	switch respErr.Response.StatusCode {
	case http.StatusUnauthorized:
		return DenialUnauthenticated
	case http.StatusForbidden:
		return DenialForbidden
	case http.StatusNotFound:
		return DenialInvisible
	default:
		return DenialNone
	}
}
