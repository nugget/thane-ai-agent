package email

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/emersion/go-imap/v2"

	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// The outbound handlers live apart from the read-side handlers because
// they share one send path, sendEmail, which is the seam every outbound
// message passes through: trust gating, the audit copy, composition,
// delivery, and the Sent-folder copy happen there and nowhere else.

// HandleSend composes and sends a new message.
func (t *Tools) HandleSend(ctx context.Context, args map[string]any) (string, error) {
	opts := SendOptions{
		To:      toolargs.StringSlice(args, "to"),
		Cc:      toolargs.StringSlice(args, "cc"),
		Subject: toolargs.TrimmedString(args, "subject"),
		Body:    toolargs.String(args, "body"),
		Account: toolargs.TrimmedString(args, "account"),
	}

	var problems []string
	if len(opts.To) == 0 {
		problems = append(problems, "to is required (array of email addresses)")
	}
	if opts.Subject == "" {
		problems = append(problems, "subject is required")
	}
	if strings.TrimSpace(opts.Body) == "" {
		problems = append(problems, "body is required (markdown)")
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("%s", strings.Join(problems, "; "))
	}

	acct, err := t.service.ResolveAccount(ctx, opts.Account)
	if err != nil {
		return "", err
	}
	return t.sendEmail(ctx, "email_send", acct, opts.To, opts.Cc, opts.Subject, opts.Body, "", nil)
}

// HandleReply replies to an existing message with threading headers.
func (t *Tools) HandleReply(ctx context.Context, args map[string]any) (string, error) {
	opts := ReplyOptions{
		UID:      toolargs.Uint32(args, "uid"),
		Folder:   toolargs.TrimmedString(args, "folder"),
		Body:     toolargs.String(args, "body"),
		ReplyAll: toolargs.Bool(args, "reply_all"),
		Account:  toolargs.TrimmedString(args, "account"),
	}

	var problems []string
	if opts.UID == 0 {
		problems = append(problems, "uid is required (integer > 0): the UID of the message being replied to, from an email_list or email_read result")
	}
	if strings.TrimSpace(opts.Body) == "" {
		problems = append(problems, "body is required (markdown)")
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("%s", strings.Join(problems, "; "))
	}

	acct, err := t.service.ResolveAccount(ctx, opts.Account)
	if err != nil {
		return "", err
	}

	// Peek so that replying does not itself change the original's
	// seen state.
	original, err := acct.Client.ReadMessage(ctx, ReadOptions{Folder: opts.Folder, UID: opts.UID, Peek: true})
	if err != nil {
		return "", fmt.Errorf("fetch original message: %w", err)
	}

	subject := original.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	to, cc, err := replyRecipients(acct.Config, original, opts.ReplyAll)
	if err != nil {
		return "", err
	}

	var refs []string
	refs = append(refs, original.References...)
	if original.MessageID != "" {
		refs = append(refs, original.MessageID)
	}

	return t.sendEmail(ctx, "email_reply", acct, addressStrings(to), addressStrings(cc), subject, opts.Body, original.MessageID, refs)
}

// replyRecipients picks the recipients of a reply: Reply-To when the
// original set one, else From; with replyAll, the original To and Cc
// as well, minus the account's own address and minus duplicates.
func replyRecipients(acctCfg AccountConfig, original *Message, replyAll bool) (to, cc []Address, err error) {
	if len(original.ReplyTo) > 0 {
		to = append(to, original.ReplyTo...)
	} else if !original.From.IsZero() {
		to = append(to, original.From)
	}
	if len(to) == 0 {
		return nil, nil, fmt.Errorf("original message has no reply address")
	}
	if !replyAll {
		return to, nil, nil
	}

	var own Address
	if acctCfg.DefaultFrom != "" {
		if parsed, err := parseAddress(acctCfg.DefaultFrom); err == nil {
			own = parsed
		}
	}
	exclude := func(a Address) bool {
		return (!own.IsZero() && a.Key() == own.Key()) || containsAddress(to, a) || containsAddress(cc, a)
	}
	for _, addr := range original.To {
		if !exclude(addr) {
			to = append(to, addr)
		}
	}
	for _, addr := range original.Cc {
		if !exclude(addr) {
			cc = append(cc, addr)
		}
	}
	return to, cc, nil
}

// sendEmail is the shared send path for HandleSend and HandleReply. It
// handles trust zone gating, the audit copy, composition, SMTP
// delivery, and the Sent-folder copy.
func (t *Tools) sendEmail(ctx context.Context, tool string, acct ResolvedAccount, to, cc []string, subject, body, inReplyTo string, references []string) (string, error) {
	acctCfg := acct.Config
	if !acctCfg.SMTPConfigured() {
		return "", fmt.Errorf("email account %q has no smtp configured; it can read and organize mail but not send it. Use an account that can send, or report that this one cannot", acctCfg.Name)
	}

	// Auto-Bcc owner if configured and not already a recipient.
	var bcc []string
	if owner := t.service.BccOwner(); owner != "" {
		ownerAddr, err := parseAddress(owner)
		if err != nil {
			return "", fmt.Errorf("configured bcc_owner %q is not a valid address: %w", owner, err)
		}
		alreadyRecipient := false
		for _, addr := range slices.Concat(to, cc) {
			if parsed, err := parseAddress(addr); err == nil && parsed.Key() == ownerAddr.Key() {
				alreadyRecipient = true
				break
			}
		}
		if !alreadyRecipient {
			bcc = append(bcc, owner)
		}
	}

	// Trust zone gating: check all recipients including auto-Bcc.
	allRecipients := slices.Concat(to, cc, bcc)
	trust := CheckRecipientTrust(ctx, t.contacts, allRecipients)
	if trust.HasIssues() {
		return "", fmt.Errorf("recipient trust issues: %s", trust.FormatIssues())
	}

	composed, err := ComposeMessage(ComposeOptions{
		From:       acctCfg.DefaultFrom,
		To:         to,
		Cc:         cc,
		Bcc:        bcc,
		Subject:    subject,
		Body:       body,
		InReplyTo:  inReplyTo,
		References: references,
	})
	if err != nil {
		return "", fmt.Errorf("compose message: %w", err)
	}

	bccAddrs, err := parseAddresses(bcc)
	if err != nil {
		return "", fmt.Errorf("bcc addresses: %w", err)
	}
	smtpRecipients := collectRecipients(composed.To, composed.Cc, bccAddrs)

	wire := composed.Bytes
	signed := false
	if signer := t.service.signerFor(acctCfg.Name); signer != nil {
		wire, err = signer.Sign(ctx, OutboundMessage{Account: acctCfg.Name, From: composed.From, Message: composed.Bytes})
		if err != nil {
			return "", fmt.Errorf("sign message for account %q: %w", acctCfg.Name, err)
		}
		signed = true
	}

	if err := sendMail(ctx, acctCfg.Name, acctCfg.SMTP, composed.From.Address, smtpRecipients, wire); err != nil {
		return "", err
	}

	t.logger.Info("email sent",
		"tool", tool,
		"account", acctCfg.Name,
		"message_id", composed.MessageID,
		"recipient_count", len(smtpRecipients),
		"in_reply_to", inReplyTo,
	)

	recipients := trust.Assessments
	if recipients == nil {
		recipients = []RecipientAssessment{}
	}
	resp := sendResponse{
		Disposition: "sent",
		Account:     acctCfg.Name,
		MessageID:   composed.MessageID,
		To:          nonNilStrings(addressStrings(composed.To)),
		Cc:          nonNilStrings(addressStrings(composed.Cc)),
		BccCount:    len(bccAddrs),
		Subject:     subject,
		InReplyTo:   inReplyTo,
		Signed:      signed,
		Recipients:  recipients,
	}

	// Store a copy in the configured Sent folder via IMAP APPEND.
	if acctCfg.SentFolder != "" {
		if _, appendErr := acct.Client.AppendMessage(ctx, acctCfg.SentFolder, wire, []imap.Flag{imap.FlagSeen}); appendErr != nil {
			t.logger.Warn("failed to store sent message in IMAP folder",
				"folder", acctCfg.SentFolder,
				"account", acctCfg.Name,
				"message_id", composed.MessageID,
				"error", appendErr,
			)
			resp.SentFolderCopy = "failed"
		} else {
			resp.SentFolderCopy = acctCfg.SentFolder
		}
	}

	t.service.recordOp(tool, acctCfg.Name, "", composed.MessageID)
	return marshalResponse(resp)
}
