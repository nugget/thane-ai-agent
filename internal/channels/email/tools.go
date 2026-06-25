package email

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// Tools holds email tool dependencies. Each handler takes the raw
// argument map from the tool registry and returns formatted text for
// the LLM.
type Tools struct {
	manager  *Manager
	contacts ContactResolver
}

// NewTools creates email tools backed by the given manager and optional
// contact resolver for trust zone gating. Pass nil for contacts to
// disable trust zone checks on outbound email.
func NewTools(mgr *Manager, contacts ContactResolver) *Tools {
	return &Tools{manager: mgr, contacts: contacts}
}

// HandleList lists recent emails in a folder.
func (t *Tools) HandleList(ctx context.Context, args map[string]any) (string, error) {
	opts := ListOptions{
		Folder:  toolargs.String(args, "folder"),
		Limit:   toolargs.Int(args, "limit"),
		Unseen:  toolargs.Bool(args, "unseen"),
		Account: toolargs.String(args, "account"),
	}

	client, err := t.manager.Account(opts.Account)
	if err != nil {
		return "", err
	}

	envelopes, err := client.ListMessages(ctx, opts)
	if err != nil {
		return "", err
	}

	if len(envelopes) == 0 {
		folder := opts.Folder
		if folder == "" {
			folder = "INBOX"
		}
		return fmt.Sprintf("No messages in %s", folder), nil
	}

	return formatEnvelopeList(envelopes), nil
}

// HandleRead reads a single email by UID.
func (t *Tools) HandleRead(ctx context.Context, args map[string]any) (string, error) {
	uid := toolargs.Uint32(args, "uid")
	folder := toolargs.String(args, "folder")
	account := toolargs.String(args, "account")

	if uid == 0 {
		return "", fmt.Errorf("uid is required")
	}

	client, err := t.manager.Account(account)
	if err != nil {
		return "", err
	}

	msg, err := client.ReadMessage(ctx, folder, uid)
	if err != nil {
		return "", err
	}

	return formatMessage(msg), nil
}

// HandleFolders lists all folders with message counts.
func (t *Tools) HandleFolders(ctx context.Context, args map[string]any) (string, error) {
	account := toolargs.String(args, "account")

	client, err := t.manager.Account(account)
	if err != nil {
		return "", err
	}

	folders, err := client.ListFolders(ctx)
	if err != nil {
		return "", err
	}

	if len(folders) == 0 {
		return "No folders found", nil
	}

	return formatFolderList(folders), nil
}

// HandleSearch searches for emails matching the given criteria.
func (t *Tools) HandleSearch(ctx context.Context, args map[string]any) (string, error) {
	opts := SearchOptions{
		Folder:  toolargs.String(args, "folder"),
		Query:   toolargs.String(args, "query"),
		From:    toolargs.String(args, "from"),
		Limit:   toolargs.Int(args, "limit"),
		Account: toolargs.String(args, "account"),
	}

	if s := toolargs.String(args, "since"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			opts.Since = t
		}
	}
	if s := toolargs.String(args, "before"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			opts.Before = t
		}
	}

	client, err := t.manager.Account(opts.Account)
	if err != nil {
		return "", err
	}

	envelopes, err := client.SearchMessages(ctx, opts)
	if err != nil {
		return "", err
	}

	if len(envelopes) == 0 {
		return "No messages match the search criteria", nil
	}

	return formatEnvelopeList(envelopes), nil
}

// HandleMark modifies flags on specified messages.
func (t *Tools) HandleMark(ctx context.Context, args map[string]any) (string, error) {
	action := parseMarkAction(args)

	if len(action.UIDs) == 0 {
		return "", fmt.Errorf("uids is required")
	}
	if action.Flag == "" {
		return "", fmt.Errorf("flag is required (seen, flagged, answered)")
	}

	client, err := t.manager.Account(action.Account)
	if err != nil {
		return "", err
	}

	if err := client.MarkMessages(ctx, action); err != nil {
		return "", err
	}

	verb := "Added"
	if !action.Add {
		verb = "Removed"
	}
	return fmt.Sprintf("%s %q flag on %d message(s)", verb, action.Flag, len(action.UIDs)), nil
}

// parseMarkAction translates the raw tool-argument map into a
// [MarkAction]. Extracted from [Tools.HandleMark] so unit tests can
// assert the args→action translation (especially the `add`
// default-true contract from #930) without standing up a Manager.
//
// Default semantics:
//   - `add` omitted defaults to true, matching the schema's
//     "default: true" — marking seen after triage is the common case.
//   - `uids` accepts an array; `uid` is a single-message convenience.
//     If both are absent the returned action has UIDs == nil and the
//     caller (HandleMark) surfaces "uids is required" to the model.
func parseMarkAction(args map[string]any) MarkAction {
	action := MarkAction{
		Folder:  toolargs.String(args, "folder"),
		Flag:    toolargs.String(args, "flag"),
		Add:     toolargs.BoolOr(args, "add", true),
		Account: toolargs.String(args, "account"),
	}
	action.UIDs = toolargs.Uint32Slice(args, "uids")
	if len(action.UIDs) == 0 {
		if uid := toolargs.Uint32(args, "uid"); uid != 0 {
			action.UIDs = []uint32{uid}
		}
	}
	return action
}

// HandleSend composes and sends a new email.
func (t *Tools) HandleSend(ctx context.Context, args map[string]any) (string, error) {
	opts := SendOptions{
		To:      toolargs.StringSlice(args, "to"),
		Cc:      toolargs.StringSlice(args, "cc"),
		Subject: toolargs.String(args, "subject"),
		Body:    toolargs.String(args, "body"),
		Account: toolargs.String(args, "account"),
	}

	if len(opts.To) == 0 {
		return "", fmt.Errorf("to is required")
	}
	if opts.Subject == "" {
		return "", fmt.Errorf("subject is required")
	}
	if opts.Body == "" {
		return "", fmt.Errorf("body is required")
	}

	return t.sendEmail(ctx, opts.Account, opts.To, opts.Cc, opts.Subject, opts.Body, "", nil)
}

// HandleReply replies to an existing message with threading headers.
func (t *Tools) HandleReply(ctx context.Context, args map[string]any) (string, error) {
	opts := ReplyOptions{
		UID:      toolargs.Uint32(args, "uid"),
		Folder:   toolargs.String(args, "folder"),
		Body:     toolargs.String(args, "body"),
		ReplyAll: toolargs.Bool(args, "reply_all"),
		Account:  toolargs.String(args, "account"),
	}

	if opts.UID == 0 {
		return "", fmt.Errorf("uid is required")
	}
	if opts.Body == "" {
		return "", fmt.Errorf("body is required")
	}

	// Fetch the original message for threading info.
	client, err := t.manager.Account(opts.Account)
	if err != nil {
		return "", err
	}

	original, err := client.ReadMessage(ctx, opts.Folder, opts.UID)
	if err != nil {
		return "", fmt.Errorf("fetch original message: %w", err)
	}

	// Build reply subject.
	subject := original.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	// Build recipient list.
	var to []string
	if original.ReplyTo != "" {
		to = []string{original.ReplyTo}
	} else {
		to = []string{original.From}
	}

	var cc []string
	if opts.ReplyAll {
		// Add original To and Cc, excluding our own address.
		acctCfg, err := t.manager.AccountConfig(opts.Account)
		if err != nil {
			return "", err
		}
		ownAddr := extractAddress(acctCfg.DefaultFrom)
		for _, addr := range original.To {
			if extractAddress(addr) != ownAddr {
				to = append(to, addr)
			}
		}
		for _, addr := range original.Cc {
			if extractAddress(addr) != ownAddr {
				cc = append(cc, addr)
			}
		}
	}

	// Build threading references.
	var refs []string
	refs = append(refs, original.References...)
	if original.MessageID != "" {
		refs = append(refs, original.MessageID)
	}

	return t.sendEmail(ctx, opts.Account, to, cc, subject, opts.Body, original.MessageID, refs)
}

// HandleMove moves messages between folders.
func (t *Tools) HandleMove(ctx context.Context, args map[string]any) (string, error) {
	opts := MoveOptions{
		Folder:      toolargs.String(args, "folder"),
		Destination: toolargs.String(args, "destination"),
		Account:     toolargs.String(args, "account"),
	}

	// Models sometimes pass "folder" meaning the destination (e.g.,
	// "Trash") without providing "destination". When the destination
	// key is truly absent and folder is set, promote folder to
	// destination and let the source default to INBOX. An explicitly
	// empty destination still triggers the required-field error.
	_, hasDestination := args["destination"]
	if !hasDestination && opts.Destination == "" && opts.Folder != "" {
		opts.Destination = opts.Folder
		opts.Folder = ""
	}

	// Parse UIDs — accept "uids" array or singular "uid".
	opts.UIDs = toolargs.Uint32Slice(args, "uids")
	if len(opts.UIDs) == 0 {
		if uid := toolargs.Uint32(args, "uid"); uid != 0 {
			opts.UIDs = []uint32{uid}
		}
	}

	if len(opts.UIDs) == 0 {
		return "", fmt.Errorf("uids is required")
	}
	if opts.Destination == "" {
		return "", fmt.Errorf("destination is required")
	}

	client, err := t.manager.Account(opts.Account)
	if err != nil {
		return "", err
	}

	if err := client.MoveMessages(ctx, opts); err != nil {
		return "", err
	}

	folder := opts.Folder
	if folder == "" {
		folder = "INBOX"
	}
	return fmt.Sprintf("Moved %d message(s) from %s to %s", len(opts.UIDs), folder, opts.Destination), nil
}

// sendEmail is the shared send path for HandleSend and HandleReply.
// It handles trust zone gating, auto-Bcc, message composition, and SMTP delivery.
func (t *Tools) sendEmail(ctx context.Context, account string, to, cc []string, subject, body, inReplyTo string, references []string) (string, error) {
	acctCfg, err := t.manager.AccountConfig(account)
	if err != nil {
		return "", err
	}

	if !acctCfg.SMTPConfigured() {
		return "", fmt.Errorf("SMTP not configured for account %q", acctCfg.Name)
	}

	// Auto-Bcc owner if configured.
	var bcc []string
	if owner := t.manager.BccOwner(); owner != "" {
		ownerBare := extractAddress(owner)
		alreadyRecipient := false
		for _, addr := range slices.Concat(to, cc) {
			if extractAddress(addr) == ownerBare {
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
	trust := CheckRecipientTrust(t.contacts, allRecipients)
	if trust.HasIssues() {
		return "", fmt.Errorf("recipient trust issues: %s", trust.FormatIssues())
	}

	// Compose the MIME message.
	msg, err := ComposeMessage(ComposeOptions{
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

	// Collect all SMTP recipients (To + Cc + Bcc).
	smtpRecipients := collectRecipients(to, cc, bcc)

	// Send via SMTP.
	fromAddr := extractAddress(acctCfg.DefaultFrom)
	if err := SendMail(ctx, acctCfg.SMTP, fromAddr, smtpRecipients, msg); err != nil {
		return "", fmt.Errorf("send email: %w", err)
	}

	slog.Info("email sent",
		"from", acctCfg.DefaultFrom,
		"to", to,
		"subject", subject,
		"account", acctCfg.Name,
	)

	// Store a copy in the configured Sent folder via IMAP APPEND.
	if acctCfg.SentFolder != "" {
		client, err := t.manager.Account(account)
		if err != nil {
			slog.Warn("failed to retrieve account for storing sent message",
				"folder", acctCfg.SentFolder,
				"account", acctCfg.Name,
				"error", err,
			)
		} else if appendErr := client.AppendMessage(ctx, acctCfg.SentFolder, msg); appendErr != nil {
			slog.Warn("failed to store sent message in IMAP folder",
				"folder", acctCfg.SentFolder,
				"account", acctCfg.Name,
				"error", appendErr,
			)
		}
	}

	return fmt.Sprintf("Email sent to %s — subject: %s", strings.Join(to, ", "), subject), nil
}

// --- Formatting helpers ---

func formatEnvelopeList(envelopes []Envelope) string {
	now := time.Now()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d message(s):\n\n", len(envelopes)))

	for _, env := range envelopes {
		sb.WriteString(fmt.Sprintf("UID: %d\n", env.UID))
		sb.WriteString(fmt.Sprintf("From: %s\n", env.From))
		sb.WriteString(fmt.Sprintf("Subject: %s\n", env.Subject))
		sb.WriteString(fmt.Sprintf("Date: %s\n", promptfmt.FormatDelta(env.Date, now)))

		if len(env.Flags) > 0 {
			sb.WriteString(fmt.Sprintf("Flags: %s\n", strings.Join(env.Flags, ", ")))
		}
		sb.WriteString(fmt.Sprintf("Size: %d bytes\n", env.Size))
		sb.WriteString("\n")
	}

	return sb.String()
}

func formatMessage(msg *Message) string {
	now := time.Now()
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("From: %s\n", msg.From))
	sb.WriteString(fmt.Sprintf("To: %s\n", strings.Join(msg.To, ", ")))
	if len(msg.Cc) > 0 {
		sb.WriteString(fmt.Sprintf("Cc: %s\n", strings.Join(msg.Cc, ", ")))
	}
	sb.WriteString(fmt.Sprintf("Subject: %s\n", msg.Subject))
	sb.WriteString(fmt.Sprintf("Date: %s\n", promptfmt.FormatDelta(msg.Date, now)))
	if len(msg.Flags) > 0 {
		sb.WriteString(fmt.Sprintf("Flags: %s\n", strings.Join(msg.Flags, ", ")))
	}
	if msg.MessageID != "" {
		sb.WriteString(fmt.Sprintf("Message-ID: %s\n", msg.MessageID))
	}
	sb.WriteString(fmt.Sprintf("UID: %d | Size: %d bytes\n", msg.UID, msg.Size))
	sb.WriteString("\n---\n\n")

	if msg.TextBody != "" {
		sb.WriteString(msg.TextBody)
	} else if msg.HTMLBody != "" {
		sb.WriteString("[HTML content — no plain text version available]\n\n")
		sb.WriteString(msg.HTMLBody)
	} else {
		sb.WriteString("[No text content available]")
	}

	return sb.String()
}

func formatFolderList(folders []Folder) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d folder(s):\n\n", len(folders)))

	for _, f := range folders {
		sb.WriteString(fmt.Sprintf("%-30s  %d messages", f.Name, f.Messages))
		if f.Unseen > 0 {
			sb.WriteString(fmt.Sprintf(" (%d unseen)", f.Unseen))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}
