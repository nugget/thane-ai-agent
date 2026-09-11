package email

import (
	"context"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// ContactStatus classifies how an address resolved against the contact
// directory. The four outcomes are deliberately distinct: a stranger, a
// duplicate record, and a store that could not answer each want a
// different next move, and collapsing them would hand the model one
// "unknown" it could only guess at.
type ContactStatus string

const (
	// ContactMatched means exactly one directory record carries the
	// address; [ContactMatch.Binding] identifies it.
	ContactMatched ContactStatus = "matched"

	// ContactUnmatched means no directory record carries the address.
	ContactUnmatched ContactStatus = "unmatched"

	// ContactAmbiguous means more than one record carries the address.
	// [ContactMatch.Candidates] lists them; the effective trust zone is
	// the least privileged among them, and no candidate is treated as
	// the operator.
	ContactAmbiguous ContactStatus = "ambiguous"

	// ContactLookupFailed means the directory could not be consulted.
	// It is not a stranger: a send gate refuses on it, and the reason
	// names the store error rather than the recipient.
	ContactLookupFailed ContactStatus = "lookup_failed"
)

// ZoneUnknown is the trust zone rendered for an address the directory
// holds no record of. It is the contacts package's implicit zone for a
// stranger and never appears on a stored record.
const ZoneUnknown = "unknown"

// Interaction directions recorded on a contact.
const (
	DirectionInbound  = "inbound"
	DirectionOutbound = "outbound"
)

// ContactCandidate is one of several directory records sharing an
// address.
type ContactCandidate struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	TrustZone string `json:"trust_zone"`
}

// ContactMatch is the directory's answer for one address.
type ContactMatch struct {
	// Status says how the address resolved.
	Status ContactStatus

	// Binding is the matched contact as a channel binding, set only
	// when Status is [ContactMatched]. It carries the contact UUID,
	// display name, trust zone, and whether the record is the
	// operator's. It is an identity claim about the address, not proof
	// that the contact wrote a given message.
	Binding *memory.ChannelBinding

	// TrustZone is the effective zone the gate and the wake metadata
	// use: the contact's zone when matched, the least privileged
	// candidate's when ambiguous, and [ZoneUnknown] otherwise.
	TrustZone string

	// Candidates lists the records sharing the address when Status is
	// [ContactAmbiguous].
	Candidates []ContactCandidate

	// Err is the store failure when Status is [ContactLookupFailed].
	Err error
}

// ContactResolver resolves an email address against the contact
// directory. Implementations live in the application layer so the
// email package does not import the contacts store; the one in
// internal/app is shared with the Signal channel so both speak the
// same identity.
//
// A returned error is a lookup failure and is reported as such; an
// address nobody holds is a successful [ContactUnmatched] result, not
// an error.
type ContactResolver interface {
	ResolveEmailContact(ctx context.Context, address string) (ContactMatch, error)
}

// Interaction is one exchange with a directory contact, recorded on
// the contact so a dossier can say when the person was last in touch.
type Interaction struct {
	// ContactID is the contact's UUID.
	ContactID string

	// At is when the exchange happened: the message's Date for inbound
	// mail, the send time for outbound.
	At time.Time

	// Direction is [DirectionInbound] or [DirectionOutbound].
	Direction string

	// Account is the mailbox the exchange went through.
	Account string

	// MessageID identifies the message, when known.
	MessageID string
}

// InteractionRecorder persists an [Interaction] on a contact. The
// application layer implements it over the contacts store; a recorder
// must treat an older timestamp than the one already recorded as a
// no-op so replayed or reordered batches cannot move a contact's last
// interaction backwards.
type InteractionRecorder interface {
	RecordEmailInteraction(ctx context.Context, in Interaction) error
}

// identityLookup memoizes address resolution within one tool call or
// poll cycle, so a thread with the same three correspondents on every
// message costs three lookups rather than three per message.
type identityLookup struct {
	ctx      context.Context
	resolver ContactResolver
	logger   *slog.Logger
	cache    map[string]ContactMatch
}

func newIdentityLookup(ctx context.Context, resolver ContactResolver, logger *slog.Logger) *identityLookup {
	if logger == nil {
		logger = slog.Default()
	}
	return &identityLookup{ctx: ctx, resolver: resolver, logger: logger, cache: make(map[string]ContactMatch)}
}

// resolve returns the directory's answer for an address. Without a
// resolver every address is unmatched.
func (l *identityLookup) resolve(a Address) ContactMatch {
	key := a.Key()
	if key == "" {
		return ContactMatch{Status: ContactUnmatched, TrustZone: ZoneUnknown}
	}
	if match, ok := l.cache[key]; ok {
		return match
	}
	match := resolveContact(l.ctx, l.resolver, key)
	if match.Status == ContactLookupFailed {
		l.logger.Warn("contact lookup failed for email address",
			"address", key,
			"error", match.Err,
		)
	}
	l.cache[key] = match
	return match
}

// resolveContact normalizes a resolver's answer: a nil resolver is
// "unmatched", an error is "lookup_failed", and the effective zone is
// always populated so callers never branch on an empty string.
func resolveContact(ctx context.Context, resolver ContactResolver, address string) ContactMatch {
	if resolver == nil {
		return ContactMatch{Status: ContactUnmatched, TrustZone: ZoneUnknown}
	}
	match, err := resolver.ResolveEmailContact(ctx, address)
	if err != nil {
		return ContactMatch{Status: ContactLookupFailed, TrustZone: ZoneUnknown, Err: err}
	}
	if match.Status == "" {
		match.Status = ContactUnmatched
	}
	if match.TrustZone == "" {
		if match.Status == ContactMatched && match.Binding != nil && match.Binding.TrustZone != "" {
			match.TrustZone = match.Binding.TrustZone
		} else {
			match.TrustZone = ZoneUnknown
		}
	}
	if match.Status != ContactMatched {
		match.Binding = nil
	}
	return match
}
