package email

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
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

	// Draft asks for the message to be held in Drafts regardless of
	// what the policy would have done.
	Draft bool
}

// SendOutcome is what happened to a message that was not refused.
type SendOutcome struct {
	Decision       Decision
	Composed       Composed
	BccCount       int
	Signed         bool
	SentFolderCopy string
	DraftsFolder   string
	DraftUID       uint32
}

// Send is the one path every outbound message takes: the account's
// access level, the audit copy, the recipient trust gate and domain
// rules, the delivery decision, the inspector, composition, signing,
// and delivery to SMTP or to the Drafts folder. A refusal is a
// [*PolicyRefusal] carrying the decision; any other error is a fault
// in delivery after the decision was made.
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
		return SendOutcome{}, s.refuse(req.Tool, decision)
	}

	bcc, err := s.auditCopy(req.To, req.Cc)
	if err != nil {
		return SendOutcome{}, err
	}

	trust := CheckRecipientTrust(ctx, s.contacts, slices.Concat(req.To, req.Cc))
	applyDomainRules(&trust, cfg.Policy)
	if trust.Assessments != nil {
		decision.Recipients = trust.Assessments
	}
	if trust.HasIssues() {
		decision.Route = RouteTrustGate
		decision.Reason = trustRefusalSentence(trust)
		return SendOutcome{}, s.refuse(req.Tool, decision)
	}
	decision.Gating = mostRestrictive(trust.Assessments)
	decision.Disposition, decision.Route = routeDelivery(cfg.DeliveryMode(), decision.Gating, decision.Attended, req.Draft)
	if decision.Disposition == DispositionSent && !cfg.SMTPConfigured() {
		decision.Disposition = DispositionRefused
		decision.Route = RouteNoSMTP
		decision.Reason = fmt.Sprintf("Email not sent: account %q has no smtp configured, so it can only draft; retry with draft: true or use an account that can send.", cfg.Name)
		return SendOutcome{}, s.refuse(req.Tool, decision)
	}

	composed, err := ComposeMessage(ComposeOptions{
		From:        cfg.DefaultFrom,
		To:          req.To,
		Cc:          req.Cc,
		Bcc:         bcc,
		BccInHeader: decision.Disposition == DispositionDrafted,
		Subject:     req.Subject,
		Body:        req.Body,
		InReplyTo:   req.InReplyTo,
		References:  req.References,
	})
	if err != nil {
		return SendOutcome{}, fmt.Errorf("compose message: %w", err)
	}

	if s.inspector != nil {
		objection, err := s.inspector.Inspect(ctx, OutboundReview{
			Tool:      req.Tool,
			Decision:  decision,
			From:      composed.From,
			To:        composed.To,
			Cc:        composed.Cc,
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
			return SendOutcome{}, s.refuse(req.Tool, decision)
		}
	}

	outcome := SendOutcome{Composed: composed, BccCount: len(bcc)}
	switch decision.Disposition {
	case DispositionDrafted:
		folder := s.draftsFolder(ctx, req.Account)
		appended, err := req.Account.Client.AppendMessage(ctx, folder, composed.Bytes, []imap.Flag{imap.FlagDraft, imap.FlagSeen})
		if err != nil {
			return SendOutcome{}, fmt.Errorf("hold message in drafts folder %q of account %q: %w", folder, cfg.Name, err)
		}
		decision.DraftsFolder = folder
		outcome.DraftsFolder = folder
		outcome.DraftUID = appended.UID
	case DispositionSent:
		wire := composed.Bytes
		if signer := s.signerFor(cfg.Name); signer != nil {
			wire, err = signer.Sign(ctx, OutboundMessage{Account: cfg.Name, From: composed.From, Message: composed.Bytes})
			if err != nil {
				return SendOutcome{}, fmt.Errorf("sign message for account %q: %w", cfg.Name, err)
			}
			outcome.Signed = true
		}
		bccAddrs, err := parseAddresses(bcc)
		if err != nil {
			return SendOutcome{}, fmt.Errorf("bcc addresses: %w", err)
		}
		recipients := collectRecipients(composed.To, composed.Cc, bccAddrs)
		if err := sendMail(ctx, cfg.Name, cfg.SMTP, composed.From.Address, recipients, wire); err != nil {
			return SendOutcome{}, err
		}
		if cfg.SentFolder != "" {
			if _, appendErr := req.Account.Client.AppendMessage(ctx, cfg.SentFolder, wire, []imap.Flag{imap.FlagSeen}); appendErr != nil {
				s.logger.Warn("failed to store sent message in IMAP folder",
					"folder", cfg.SentFolder,
					"account", cfg.Name,
					"message_id", composed.MessageID,
					"error", appendErr,
				)
				outcome.SentFolderCopy = "failed"
			} else {
				outcome.SentFolderCopy = cfg.SentFolder
			}
		}
		s.recordOutboundInteractions(ctx, cfg.Name, composed.MessageID, trust.Assessments)
	}
	outcome.Decision = decision

	s.logger.Info("email outbound decision",
		"tool", req.Tool,
		"account", cfg.Name,
		"disposition", decision.Disposition,
		"route", decision.Route,
		"attended", decision.Attended,
		"gating", decision.Gating,
		"recipient_count", len(decision.Recipients),
		"message_id", composed.MessageID,
		"in_reply_to", req.InReplyTo,
		"drafts_folder", decision.DraftsFolder,
		"signed", outcome.Signed,
	)
	s.recordOp(req.Tool, cfg.Name, decision.DraftsFolder, composed.MessageID)
	return outcome, nil
}

// refuse logs a refusal and returns it as the model-facing error.
func (s *Service) refuse(tool string, decision Decision) error {
	decision.Disposition = DispositionRefused
	s.logger.Info("email outbound refused",
		"tool", tool,
		"account", decision.Account,
		"route", decision.Route,
		"attended", decision.Attended,
		"gating", decision.Gating,
		"recipient_count", len(decision.Recipients),
		"reason", decision.Reason,
	)
	return &PolicyRefusal{Message: decision.Reason, Decision: decision}
}

// sendAccessRefusal returns the refusal a send-shaped tool gets on an
// account that cannot draft, or nil. Handlers call it before doing any
// work a refusal would waste.
func (s *Service) sendAccessRefusal(ctx context.Context, tool string, acct ResolvedAccount) error {
	if acct.Config.CanDraft() {
		return nil
	}
	return s.refuse(tool, Decision{
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
	return fmt.Sprintf("Email account %q cannot send or draft mail: %s. Use an account whose Email Accounts entry shows access \"send\", or report that this one cannot.", cfg.Name, why)
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

// draftsFolder returns where an account's drafts go: the configured
// folder, else the folder the server marks \Drafts, else "Drafts".
func (s *Service) draftsFolder(ctx context.Context, acct ResolvedAccount) string {
	if configured := strings.TrimSpace(acct.Config.DraftsFolder); configured != "" {
		return configured
	}
	folders, ok := s.cachedFolders(acct.Name)
	if !ok {
		if listed, err := s.listFolders(ctx, acct); err == nil {
			folders = folderSnapshot{Folders: listed, At: time.Now()}
		}
	}
	if f, found := FindFolderByRole(folders.Folders, RoleDrafts); found {
		return f.Name
	}
	return "Drafts"
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
		if a.ContactID == "" || seen[a.ContactID] {
			continue
		}
		seen[a.ContactID] = true
		err := s.interactions.RecordEmailInteraction(ctx, Interaction{
			ContactID: a.ContactID,
			At:        now,
			Direction: DirectionOutbound,
			Account:   account,
			MessageID: messageID,
		})
		if err != nil {
			s.logger.Warn("recording outbound email interaction failed", "account", account, "contact_id", a.ContactID, "error", err)
		}
	}
}
