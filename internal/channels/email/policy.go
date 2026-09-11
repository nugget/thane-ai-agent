package email

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// Disposition is what became of an outbound message.
type Disposition string

// Dispositions. A message ends in exactly one.
const (
	// DispositionSent means SMTP accepted the message.
	DispositionSent Disposition = "sent"

	// DispositionDrafted means the message was written to the
	// account's Drafts folder for the operator to send from their own
	// client; nothing left the mailbox.
	DispositionDrafted Disposition = "drafted"

	// DispositionRefused means nothing was sent or drafted.
	DispositionRefused Disposition = "refused"
)

// Gating levels, the contacts package's per-zone send policy as the
// gate reads it.
const (
	GatingAllowed      = "allowed"
	GatingConfirmation = "confirmation"
	GatingBlocked      = "blocked"
)

// Routes name the rule that settled a decision, so a log line or a
// result says not just what happened but which policy made it happen.
const (
	// RouteAccess: the account's access level does not permit sending.
	RouteAccess = "access"

	// RouteTrustGate: at least one recipient failed the trust gate.
	RouteTrustGate = "trust_gate"

	// RouteRequestedDraft: the caller asked for a draft.
	RouteRequestedDraft = "requested_draft"

	// RoutePolicyDrafts: the account's delivery mode is drafts.
	RoutePolicyDrafts = "policy_drafts"

	// RoutePolicyDirect: the account's delivery mode is direct.
	RoutePolicyDirect = "policy_direct"

	// RouteTrustZone: by_trust_zone delivery routed on the most
	// restrictive recipient's zone.
	RouteTrustZone = "trust_zone"

	// RouteUnattendedFloor: by_trust_zone delivery held the message
	// because no human is attending this turn.
	RouteUnattendedFloor = "unattended_floor"

	// RouteInspector: the outbound inspector refused the message.
	RouteInspector = "inspector"

	// RouteNoSMTP: the decision was to send but the account cannot.
	RouteNoSMTP = "no_smtp"
)

// Decision is the gate's complete account of one outbound message. It
// rides on every send result and every refusal, and one line per
// decision is logged, so the question "why did this go out, or not?"
// is answered by the record rather than reconstructed.
type Decision struct {
	Disposition Disposition `json:"disposition"`
	Account     string      `json:"account"`

	// Access and Delivery are the account's configured policy.
	Access   string `json:"access"`
	Delivery string `json:"delivery"`

	// Attended is whether a human was present for the turn: the
	// message arrived through an operator-facing API or the
	// conversation is bound to the operator's own contact.
	Attended bool `json:"attended"`

	// DraftRequested is whether the caller asked for a draft.
	DraftRequested bool `json:"draft_requested,omitempty"`

	// Gating is the most restrictive recipient's send policy.
	Gating string `json:"gating,omitempty"`

	// Route names the rule that settled the disposition.
	Route string `json:"route"`

	// Reason is the one-sentence explanation of a refusal.
	Reason string `json:"reason,omitempty"`

	// Recipients carries each recipient's assessment.
	Recipients []RecipientAssessment `json:"recipients"`

	// DraftsFolder is where a drafted message was written.
	DraftsFolder string `json:"drafts_folder,omitempty"`
}

// PolicyRefusal is the error form of a refused send: one sentence a
// model can act on, then the decision as JSON so the reasons per
// recipient and the policy that applied travel with it. Nothing was
// sent or drafted.
type PolicyRefusal struct {
	Message  string
	Decision Decision
}

// Error renders the sentence, a newline, and the decision JSON.
func (e *PolicyRefusal) Error() string {
	data, err := json.Marshal(e.Decision)
	if err != nil {
		return e.Message
	}
	return e.Message + "\n" + string(data)
}

// Inspector is a Go-side review of every outbound message after the
// policy decision and before delivery. It can only refuse: an
// objection is returned as text, the message is refused with it, and
// the inspector cannot approve what the gate refused or alter what it
// allowed. An error is treated as a refusal too, because an egress
// control that fails must fail closed.
type Inspector interface {
	Inspect(ctx context.Context, review OutboundReview) (objection string, err error)
}

// OutboundReview is what an [Inspector] sees: the decision so far and
// the message as composed.
type OutboundReview struct {
	// Tool is the tool that asked for the send.
	Tool string

	// Decision is the gate's decision, with Disposition set to what
	// will happen if the inspector does not object.
	Decision Decision

	From      Address
	To        []Address
	Cc        []Address
	Subject   string
	Body      string
	InReplyTo string
}

// attended reports whether a human is present for this turn. It is
// true when the message arrived through an operator-facing API or the
// conversation is bound to the operator's own contact, and false for
// poller wakes, scheduled loops, and conversations with anyone else.
func attended(ctx context.Context) bool {
	if tools.MessageOriginFromContext(ctx) == memory.OriginAPI {
		return true
	}
	if binding := tools.ChannelBindingFromContext(ctx); binding != nil && binding.IsOwner {
		return true
	}
	return false
}

// gatingForZone reads the contacts package's send policy for a zone.
func gatingForZone(zone string) string {
	return contacts.Policy(zone).SendGating
}

// gatingRank orders gating levels from least to most restrictive.
func gatingRank(g string) int {
	switch g {
	case GatingBlocked:
		return 2
	case GatingConfirmation:
		return 1
	}
	return 0
}

// mostRestrictive returns the most restrictive gating among the
// assessments, allowed when there are none.
func mostRestrictive(assessments []RecipientAssessment) string {
	result := GatingAllowed
	for _, a := range assessments {
		if gatingRank(a.Gating) > gatingRank(result) {
			result = a.Gating
		}
	}
	return result
}

// routeDelivery decides where a message that passed the gate goes. The
// caller's request for a draft wins, then the account's fixed modes,
// then by_trust_zone: a confirmation-gated recipient drafts, an
// unattended turn drafts, and everything else sends.
func routeDelivery(delivery, gating string, isAttended, draftRequested bool) (Disposition, string) {
	switch {
	case draftRequested:
		return DispositionDrafted, RouteRequestedDraft
	case delivery == DeliveryDrafts:
		return DispositionDrafted, RoutePolicyDrafts
	case delivery == DeliveryDirect:
		return DispositionSent, RoutePolicyDirect
	case gating == GatingConfirmation:
		return DispositionDrafted, RouteTrustZone
	case !isAttended:
		return DispositionDrafted, RouteUnattendedFloor
	}
	return DispositionSent, RouteTrustZone
}

// applyDomainRules blocks recipients the account's domain lists
// exclude. A denied domain wins over everything, including a trusted
// contact; an allow list, when set, refuses every domain outside it.
func applyDomainRules(result *TrustResult, policy PolicyConfig) {
	if len(policy.DeniedRecipientDomains) == 0 && len(policy.AllowedRecipientDomains) == 0 {
		return
	}
	allowed := result.Allowed[:0]
	for i := range result.Assessments {
		a := &result.Assessments[i]
		if !a.Allowed {
			continue
		}
		domain := domainOf(a.Address)
		switch {
		case matchesAnyDomain(domain, policy.DeniedRecipientDomains):
			a.Allowed = false
			a.Gating = GatingBlocked
			a.Reason = "the account's policy denies recipients at " + domain + "; drop the recipient or ask the operator to change denied_recipient_domains"
		case len(policy.AllowedRecipientDomains) > 0 && !matchesAnyDomain(domain, policy.AllowedRecipientDomains):
			a.Allowed = false
			a.Gating = GatingBlocked
			a.Reason = "the account's policy limits recipients to " + strings.Join(policy.AllowedRecipientDomains, ", ") + "; drop the recipient or ask the operator to change allowed_recipient_domains"
		}
		if a.Allowed {
			allowed = append(allowed, a.Address)
		} else {
			result.Blocked = append(result.Blocked, "Cannot send to "+a.Address+": "+a.Reason)
		}
	}
	result.Allowed = allowed
}

// domainOf returns the lowercase domain of an address, or empty.
func domainOf(address string) string {
	if at := strings.LastIndex(address, "@"); at >= 0 {
		return strings.ToLower(address[at+1:])
	}
	return ""
}

// matchesAnyDomain reports whether domain is one of the listed domains
// or a subdomain of one.
func matchesAnyDomain(domain string, list []string) bool {
	for _, d := range list {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "."))
		if d == "" {
			continue
		}
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	return false
}

// zoneRouting is one account's outbound routing by trust zone for one
// turn, rendered in the Email Accounts block so the model knows where
// a message will land before composing it.
type zoneRouting struct {
	SendsDirectlyTo []string `json:"sends_directly_to"`
	DraftsFor       []string `json:"drafts_for"`
	Refuses         []string `json:"refuses"`
}

// routingByZone projects the account's policy onto every trust zone
// for a turn with the given attendance.
func routingByZone(cfg AccountConfig, isAttended bool) zoneRouting {
	r := zoneRouting{SendsDirectlyTo: []string{}, DraftsFor: []string{}, Refuses: []string{}}
	for _, policy := range contacts.Policies() {
		if !cfg.CanDraft() || policy.SendGating == GatingBlocked {
			r.Refuses = append(r.Refuses, policy.Zone)
			continue
		}
		disposition, _ := routeDelivery(cfg.DeliveryMode(), policy.SendGating, isAttended, false)
		if disposition == DispositionSent && cfg.CanDeliver() {
			r.SendsDirectlyTo = append(r.SendsDirectlyTo, policy.Zone)
		} else {
			r.DraftsFor = append(r.DraftsFor, policy.Zone)
		}
	}
	return r
}
