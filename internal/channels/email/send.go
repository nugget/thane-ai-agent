package email

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// SendRequest is one outbound message as a handler assembled it.
type SendRequest struct {
	// Tool is the tool that asked, for the log and the operation record.
	Tool string

	// Account is the resolved sending account.
	Account ResolvedAccount

	To, Cc     []string
	Subject    string
	Body       string
	InReplyTo  string
	References []string

	// Draft asks for the message to be held in the account's drafts
	// folder regardless of what the policy would have done.
	Draft bool

	// Original is the header marks of the message being replied to; it
	// is zero for email_send.
	Original HeaderMarks

	// OriginalRecipients is the To and Cc of the message being replied
	// to, which says whether list mail named the account personally; it
	// is nil for email_send.
	OriginalRecipients []Address

	// OriginalEnvelope and OriginalFolder identify the message being
	// replied to, so a draft's ledger entry can find it again; both are
	// zero for email_send.
	OriginalEnvelope *Envelope
	OriginalFolder   string
}

// SendOutcome is what happened to a message that was not refused.
// SentFolder names the folder a sent copy went to and SentFolderCopy is
// "stored" or "failed"; both are empty for a draft or an account that
// keeps no Sent copy.
type SendOutcome struct {
	Decision       Decision
	Composed       Composed
	BccCount       int
	Signed         bool
	SentFolder     string
	SentFolderCopy string
	DraftsFolder   string
	DraftUID       uint32

	// DraftID is the draft ledger's id for a drafted message, or empty
	// when nothing was drafted or the ledger could not record it.
	DraftID string

	// DraftUntracked is true when a message was drafted on a service that
	// keeps a draft ledger but the ledger could not record it.
	DraftUntracked bool

	// OpenDraftIDs lists Thane's open drafts answering the same message
	// as a reply that was sent directly, which the operator could still
	// send as a second answer.
	OpenDraftIDs []string
}

// Send is the one path every outbound message takes: the account's
// access level, the recipient trust gate and domain rules, the delivery
// decision, the audit copy on mail that will be sent, the inspector,
// composition, signing, and delivery to SMTP or to the drafts folder.
// A refusal is a [*PolicyRefusal] carrying the decision; any other
// error is a fault in delivery after the decision was made.
func (s *Service) Send(ctx context.Context, req SendRequest) (SendOutcome, error) {
	cfg := req.Account.Config
	decision := Decision{
		Disposition:    DispositionRefused,
		Account:        cfg.Name,
		Access:         cfg.AccessLevel(),
		Delivery:       cfg.DeliveryMode(),
		Attended:       attended(ctx),
		DraftRequested: req.Draft,
		Recipients:     []RecipientAssessment{},
	}

	if !cfg.CanDraft() {
		decision.Route = RouteAccess
		decision.Reason = accessRefusalSentence(cfg)
		return SendOutcome{}, s.refuse(ctx, req.Tool, decision)
	}
	// An unattended reply to mail whose own headers mark it automatic
	// or bulk would be an automatic response (RFC 3834 §2). It is
	// refused in every delivery mode, a requested draft included, and
	// whatever the recipients' zones, with one exception that only
	// drafts: list mail addressed to a relaxed drafts-only account in
	// its own To or Cc, which a person reads and sends by hand.
	listReply := false
	if req.Original.Marked() {
		decision.Original = req.Original
		if !decision.Attended {
			if !personallyAddressedListReply(cfg, req.Original, req.OriginalRecipients) {
				decision.Route = RouteAutomaticResponse
				decision.Reason = automaticResponseReason(req.Original, listReplyGap(cfg, req.Original))
				return SendOutcome{}, s.refuse(ctx, req.Tool, decision)
			}
			listReply = true
		}
	}
	if n := len(req.To) + len(req.Cc); n > maxRecipients {
		decision.Route = RouteRecipientLimit
		decision.Reason = fmt.Sprintf("Email not sent: it addresses %d recipients in to and cc, and one message may address at most %d; split the audience, or ask the operator to send it from their own client.", n, maxRecipients)
		return SendOutcome{}, s.refuse(ctx, req.Tool, decision)
	}

	trust := CheckRecipientTrust(ctx, s.contacts, slices.Concat(req.To, req.Cc))
	// The relaxation must come first: applyDomainRules skips recipients
	// already refused, so only a recipient relaxed before it can be
	// refused again for its domain.
	relaxForDraftsOnly(&trust, cfg)
	applyDomainRules(&trust, cfg.Policy)
	if trust.Assessments != nil {
		decision.Recipients = trust.Assessments
	}
	if trust.HasIssues() {
		decision.Route = RouteTrustGate
		decision.Reason = trustRefusalSentence(trust)
		return SendOutcome{}, s.refuse(ctx, req.Tool, decision)
	}
	// The draft names a draft_only recipient by bare address, so the
	// operator about to send it sees where it goes.
	req.To, req.Cc = withoutDraftOnlyNames(req.To, trust.Assessments), withoutDraftOnlyNames(req.Cc, trust.Assessments)
	decision.Gating = mostRestrictive(trust.Assessments)
	decision.Disposition, decision.Route = routeDelivery(cfg.DeliveryMode(), decision.Gating, decision.Attended, req.Draft)
	if listReply {
		// The exemption decided this reply, and it only ever drafts.
		decision.Disposition, decision.Route = DispositionDrafted, RoutePersonallyAddressedListReply
	}
	if decision.Disposition == DispositionSent && !cfg.SMTPConfigured() {
		decision.Disposition = DispositionRefused
		decision.Route = RouteNoSMTP
		decision.Reason = fmt.Sprintf("Email not sent: account %q has no smtp configured, so it can only draft; retry with draft: true, and do not write the message from any other account.", cfg.Name)
		return SendOutcome{}, s.refuse(ctx, req.Tool, decision)
	}
	// A draft needs a folder with the drafts role. Without one the
	// message is refused here, before it is composed or inspected, and
	// never sent in the draft's place.
	var draftsFolder string
	if decision.Disposition == DispositionDrafted {
		folder, err := s.sendDraftsFolder(ctx, req.Account)
		if err != nil {
			s.logDecision(ctx, req, decision, SendOutcome{}, err)
			return SendOutcome{}, err
		}
		if folder == "" {
			decision.Route = RouteNoDraftsFolder
			decision.Reason = noDraftsFolderReason(cfg, req.Draft)
			return SendOutcome{}, s.refuse(ctx, req.Tool, decision)
		}
		draftsFolder = folder
		// The ledger stays locked from the conflict check through the
		// entry the draft gets, so two replies to one message cannot both
		// pass the check.
		unlock := s.lockDraftLedger(req.Account.Name)
		defer unlock()
		if err := s.refuseDraftConflict(ctx, req, decision, draftsFolder); err != nil {
			return SendOutcome{}, err
		}
	}

	// The audit copy rides only mail Thane delivers. A draft carries
	// none on any account: the operator sends it from their own client,
	// and a Bcc left in its header would copy the audit sink on mail the
	// operator, not Thane, chose to deliver.
	var bcc []string
	if decision.Disposition == DispositionSent {
		auditBcc, err := s.auditCopy(req.To, req.Cc)
		if err != nil {
			return SendOutcome{}, err
		}
		bcc = auditBcc
	}

	composed, err := ComposeMessage(ComposeOptions{
		From:       cfg.DefaultFrom,
		To:         req.To,
		Cc:         req.Cc,
		Bcc:        bcc,
		Subject:    req.Subject,
		Body:       req.Body,
		InReplyTo:  req.InReplyTo,
		References: req.References,
	})
	if err != nil {
		err = fmt.Errorf("compose message: %w", err)
		s.logDecision(ctx, req, decision, SendOutcome{}, err)
		return SendOutcome{}, err
	}

	if s.inspector != nil {
		// The inspector gets copies: a refuse-only seam must not be able
		// to rewrite the envelope or the decision record through slices
		// it shares with them.
		objection, err := s.inspector.Inspect(ctx, OutboundReview{
			Tool:      req.Tool,
			Decision:  reviewDecision(decision),
			From:      composed.From,
			To:        slices.Clone(composed.To),
			Cc:        slices.Clone(composed.Cc),
			Subject:   req.Subject,
			Body:      req.Body,
			InReplyTo: req.InReplyTo,
		})
		if err != nil {
			objection = "the outbound inspector failed: " + err.Error()
		}
		if objection != "" {
			decision.Disposition = DispositionRefused
			decision.Route = RouteInspector
			decision.Reason = "Email not sent: the outbound inspector objected (" + objection + ")."
			return SendOutcome{}, s.refuse(ctx, req.Tool, decision)
		}
	}

	outcome := SendOutcome{Composed: composed, BccCount: len(bcc)}
	switch decision.Disposition {
	case DispositionDrafted:
		appended, err := req.Account.Client.AppendMessage(ctx, draftsFolder, composed.Bytes, []imap.Flag{imap.FlagDraft, imap.FlagSeen})
		if err != nil {
			decision.DraftsFolder = draftsFolder
			err = fmt.Errorf("hold message in drafts folder %q of account %q: %w", draftsFolder, cfg.Name, err)
			s.logDecision(ctx, req, decision, outcome, err)
			return SendOutcome{}, err
		}
		decision.DraftsFolder = draftsFolder
		outcome.DraftsFolder = draftsFolder
		outcome.DraftUID = appended.UID
		outcome.DraftID = s.recordDraft(ctx, req, composed, appended)
		outcome.DraftUntracked = s.state != nil && outcome.DraftID == ""
		s.queueDraftForReview(ctx, req, decision, outcome.DraftID)
	case DispositionSent:
		wire := composed.Bytes
		if signer := s.signerFor(cfg.Name); signer != nil {
			wire, err = signer.Sign(ctx, OutboundMessage{Account: cfg.Name, From: composed.From, Message: composed.Bytes})
			if err != nil {
				err = fmt.Errorf("sign message for account %q: %w", cfg.Name, err)
				s.logDecision(ctx, req, decision, outcome, err)
				return SendOutcome{}, err
			}
			outcome.Signed = true
		}
		bccAddrs, err := parseAddresses(bcc)
		if err != nil {
			err = fmt.Errorf("bcc addresses: %w", err)
			s.logDecision(ctx, req, decision, outcome, err)
			return SendOutcome{}, err
		}
		recipients := collectRecipients(composed.To, composed.Cc, bccAddrs)
		if err := sendMail(ctx, cfg.Name, cfg.SMTP, composed.From.Address, recipients, wire); err != nil {
			s.logDecision(ctx, req, decision, outcome, err)
			return SendOutcome{}, err
		}
		if cfg.SentFolder != "" {
			outcome.SentFolder = cfg.SentFolder
			if _, appendErr := req.Account.Client.AppendMessage(ctx, cfg.SentFolder, wire, []imap.Flag{imap.FlagSeen}); appendErr != nil {
				s.logger.Warn("failed to store sent message in IMAP folder",
					"folder", cfg.SentFolder,
					"account", cfg.Name,
					"message_id", composed.MessageID,
					"error", appendErr,
				)
				outcome.SentFolderCopy = "failed"
			} else {
				outcome.SentFolderCopy = "stored"
			}
		}
		s.recordOutboundInteractions(ctx, cfg.Name, composed.MessageID, trust.Assessments)
		outcome.OpenDraftIDs = s.openDraftsAnswering(cfg.Name, req.InReplyTo)
	}
	outcome.Decision = decision

	s.logDecision(ctx, req, decision, outcome, nil)
	s.recordOp(req.Tool, cfg.Name, decision.DraftsFolder, composed.MessageID)
	return outcome, nil
}

// maxRecipients bounds the addresses one message may carry in to and
// cc, which also bounds the per-recipient assessments a result renders.
const maxRecipients = 50

// logDecision writes the one log line every decided message gets,
// keyed to the loop and conversation that asked, whether delivery then
// succeeded or not: a forensic pass must see attempted deliveries, not
// only the ones that landed. deliveryErr is nil on success.
func (s *Service) logDecision(ctx context.Context, req SendRequest, decision Decision, outcome SendOutcome, deliveryErr error) {
	attrs := []any{
		"tool", req.Tool,
		"account", decision.Account,
		"disposition", decision.Disposition,
		"route", decision.Route,
		"attended", decision.Attended,
		"gating", decision.Gating,
		"recipient_count", len(decision.Recipients),
		"message_id", outcome.Composed.MessageID,
		"in_reply_to", req.InReplyTo,
		"drafts_folder", decision.DraftsFolder,
		"draft_id", outcome.DraftID,
		"signed", outcome.Signed,
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	}
	attrs = append(attrs, originalAttrs(decision.Original)...)
	if deliveryErr != nil {
		s.logger.Warn("email outbound decision not delivered", append(attrs, "error", deliveryErr)...)
		return
	}
	s.logger.Info("email outbound decision", attrs...)
}

// reviewDecision copies a decision for the inspector, recipients and
// their contact records included.
func reviewDecision(d Decision) Decision {
	d.Recipients = slices.Clone(d.Recipients)
	for i := range d.Recipients {
		if c := d.Recipients[i].Contact; c != nil {
			cp := *c
			d.Recipients[i].Contact = &cp
		}
	}
	return d
}

// refuse logs a refusal, keyed to the loop and conversation that asked,
// and returns it as the model-facing error.
func (s *Service) refuse(ctx context.Context, tool string, decision Decision) error {
	decision.Disposition = DispositionRefused
	attrs := []any{
		"tool", tool,
		"account", decision.Account,
		"route", decision.Route,
		"attended", decision.Attended,
		"gating", decision.Gating,
		"recipient_count", len(decision.Recipients),
		"reason", decision.Reason,
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	}
	s.logger.Info("email outbound refused", append(attrs, originalAttrs(decision.Original)...)...)
	return &PolicyRefusal{Message: decision.Reason, Decision: decision}
}

// originalAttrs returns the log attributes naming a reply's marked
// original, or none when it is unmarked or the message is not a reply.
func originalAttrs(original HeaderMarks) []any {
	if !original.Marked() {
		return nil
	}
	return []any{"original_auto_submitted", original.AutoSubmitted, "original_bulk", original.Bulk}
}

// automaticResponseReason explains an unattended reply refused because
// the original's own headers mark it automatic or bulk, and names the
// only moves that work. gap is the clause naming why the list-mail rule
// did not apply, or empty where the account has no such rule.
func automaticResponseReason(original HeaderMarks, gap string) string {
	return "Email not sent: the original message's own headers mark it as " + original.describe() +
		", so a reply written while the operator is not present would be an automatic response, which is neither sent nor drafted (RFC 3834)" + gap + "; do not retry it or send it fresh with email_send, " +
		"but file the message and, if it needs an answer, bring it to the operator with request_core_attention so they can reply in their own turn."
}

// sendAccessRefusal returns the refusal a send-shaped tool gets on an
// account that cannot draft, or nil. Handlers call it before doing any
// work a refusal would waste.
func (s *Service) sendAccessRefusal(ctx context.Context, tool string, acct ResolvedAccount) error {
	if acct.Config.CanDraft() {
		return nil
	}
	return s.refuse(ctx, tool, Decision{
		Account:    acct.Config.Name,
		Access:     acct.Config.AccessLevel(),
		Delivery:   acct.Config.DeliveryMode(),
		Attended:   attended(ctx),
		Route:      RouteAccess,
		Reason:     accessRefusalSentence(acct.Config),
		Recipients: []RecipientAssessment{},
	})
}

// requireOrganize refuses a flag or move on an account whose access
// level is read.
func (s *Service) requireOrganize(acct ResolvedAccount, tool string) error {
	if acct.Config.AccessLevel() != AccessRead {
		return nil
	}
	return fmt.Errorf("email account %q is read-only (policy.access: read): %s cannot change flags or move mail there. Report the need instead of routing around it", acct.Config.Name, tool)
}

// accessRefusalSentence explains why an account cannot send or draft.
func accessRefusalSentence(cfg AccountConfig) string {
	permits := "reading, flagging, and moving mail"
	if cfg.AccessLevel() == AccessRead {
		permits = "listing, searching, and reading only"
	}
	why := fmt.Sprintf("its policy.access is %q, which permits %s", cfg.AccessLevel(), permits)
	if !cfg.SMTPConfigured() {
		why = "it has no smtp configured and " + why
	}
	return fmt.Sprintf("Email account %q cannot send or draft mail: %s. The message belongs to this mailbox and must not be written from any other account; report that this account cannot compose.", cfg.Name, why)
}

// trustRefusalSentence names the recipients the gate refused.
func trustRefusalSentence(trust TrustResult) string {
	var names []string
	for _, a := range trust.Assessments {
		if !a.Allowed {
			names = append(names, a.Address)
		}
	}
	return fmt.Sprintf("Email not sent to %s: %d recipient(s) failed the trust gate, so nothing was sent or drafted; recipients below name each reason and its recovery.", strings.Join(names, ", "), len(names))
}

// auditCopy returns the configured bcc_owner as a Bcc unless it is
// already a visible recipient. It is exempt from the trust gate: it is
// the operator's audit sink, not a recipient the agent chose.
func (s *Service) auditCopy(to, cc []string) ([]string, error) {
	owner := s.BccOwner()
	if owner == "" {
		return nil, nil
	}
	ownerAddr, err := parseAddress(owner)
	if err != nil {
		return nil, fmt.Errorf("configured bcc_owner %q is not a valid address: %w", owner, err)
	}
	for _, addr := range slices.Concat(to, cc) {
		if parsed, err := parseAddress(addr); err == nil && parsed.Key() == ownerAddr.Key() {
			return nil, nil
		}
	}
	return []string{owner}, nil
}

// recordOutboundInteractions notes, once per matched recipient, that
// the agent wrote to them. Only delivered mail counts; a draft is not
// an exchange until the operator sends it.
func (s *Service) recordOutboundInteractions(ctx context.Context, account, messageID string, assessments []RecipientAssessment) {
	if s.interactions == nil {
		return
	}
	now := time.Now()
	seen := make(map[string]bool)
	for _, a := range assessments {
		if a.Contact == nil || a.Contact.ID == "" || seen[a.Contact.ID] {
			continue
		}
		seen[a.Contact.ID] = true
		err := s.interactions.RecordEmailInteraction(ctx, Interaction{
			ContactID: a.Contact.ID,
			At:        now,
			Direction: DirectionOutbound,
			Account:   account,
			MessageID: messageID,
		})
		if err != nil {
			s.logger.Warn("recording outbound email interaction failed", "account", account, "contact_id", a.Contact.ID, "error", err)
		}
	}
}
