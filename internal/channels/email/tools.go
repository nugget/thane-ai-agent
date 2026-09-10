package email

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// Tools holds email tool dependencies. Each handler takes the raw
// argument map from the tool registry and returns formatted text for
// the LLM.
type Tools struct {
	manager  *Manager
	contacts ContactResolver
	logger   *slog.Logger
}

// NewTools creates email tools backed by the given manager and optional
// contact resolver for trust zone gating. Pass nil for contacts to
// disable trust zone checks on outbound email.
func NewTools(mgr *Manager, contacts ContactResolver) *Tools {
	logger := slog.Default()
	if mgr != nil && mgr.logger != nil {
		logger = mgr.logger
	}
	return &Tools{manager: mgr, contacts: contacts, logger: logger}
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

	listed, err := client.ListMessages(ctx, opts)
	if err != nil {
		return "", err
	}

	if len(listed.Envelopes) == 0 {
		return fmt.Sprintf("No messages in %s", listed.Folder), nil
	}

	return formatEnvelopeList(listed), nil
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

	msg, err := client.ReadMessage(ctx, ReadOptions{Folder: folder, UID: uid, Peek: !toolargs.BoolOr(args, "mark_seen", true)})
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

	now := time.Now()
	if s := toolargs.String(args, "since"); s != "" {
		since, err := parseSearchDate(s, now)
		if err != nil {
			return "", fmt.Errorf("since must be YYYY-MM-DD, RFC 3339, or a delta like -7d (got %q)", s)
		}
		opts.Since = since
	}
	if s := toolargs.String(args, "before"); s != "" {
		before, err := parseSearchDate(s, now)
		if err != nil {
			return "", fmt.Errorf("before must be YYYY-MM-DD, RFC 3339, or a delta like -1d (got %q)", s)
		}
		opts.Before = before
	}

	client, err := t.manager.Account(opts.Account)
	if err != nil {
		return "", err
	}

	found, err := client.SearchMessages(ctx, opts)
	if err != nil {
		return "", err
	}

	if len(found.Envelopes) == 0 {
		return "No messages match the search criteria", nil
	}

	return formatEnvelopeList(found), nil
}

// parseSearchDate accepts the shapes a model plausibly sends for a
// date bound: a bare YYYY-MM-DD, an RFC 3339 instant, or a delta such
// as "-7d" relative to now.
func parseSearchDate(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	return promptfmt.ParseTimeOrDelta(s, now)
}

// HandleMark modifies flags on specified messages.
func (t *Tools) HandleMark(ctx context.Context, args map[string]any) (string, error) {
	action := parseMarkAction(args)

	if len(action.UIDs) == 0 {
		return "", fmt.Errorf("uids is required: pass uids (array of integers) or uid (single integer) from an email_list or email_search result in the same account and folder")
	}
	if action.Flag == "" {
		return "", fmt.Errorf("flag is required (%s)", strings.Join(ValidFlagNames(), ", "))
	}

	client, err := t.manager.Account(action.Account)
	if err != nil {
		return "", err
	}

	result, err := client.MarkMessages(ctx, action)
	if err != nil {
		return "", err
	}

	verb := "Added"
	if !action.Add {
		verb = "Removed"
	}
	if len(result.Affected) < len(result.Requested) {
		return fmt.Sprintf("%s %q flag on %d of %d message(s) in %s; UIDs not found in this folder: %v", verb, action.Flag, len(result.Affected), len(result.Requested), result.Folder, missingUIDs(result.Requested, result.Affected)), nil
	}
	return fmt.Sprintf("%s %q flag on %d message(s) in %s", verb, action.Flag, len(result.Affected), result.Folder), nil
}

// missingUIDs returns the requested UIDs the server did not report back.
func missingUIDs(requested, affected []uint32) []uint32 {
	var missing []uint32
	for _, uid := range requested {
		if !slices.Contains(affected, uid) {
			missing = append(missing, uid)
		}
	}
	return missing
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
		return "", fmt.Errorf("to is required (array of email addresses)")
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
		return "", fmt.Errorf("uid is required (integer) — the UID of the message being replied to, from an email_list or email_read result")
	}
	if opts.Body == "" {
		return "", fmt.Errorf("body is required")
	}

	// Fetch the original message for threading info. Peek so that
	// replying does not itself change the original's seen state.
	client, err := t.manager.Account(opts.Account)
	if err != nil {
		return "", err
	}

	original, err := client.ReadMessage(ctx, ReadOptions{Folder: opts.Folder, UID: opts.UID, Peek: true})
	if err != nil {
		return "", fmt.Errorf("fetch original message: %w", err)
	}

	subject := original.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	to, cc, err := t.replyRecipients(opts.Account, original, opts.ReplyAll)
	if err != nil {
		return "", err
	}

	// Build threading references.
	var refs []string
	refs = append(refs, original.References...)
	if original.MessageID != "" {
		refs = append(refs, original.MessageID)
	}

	return t.sendEmail(ctx, opts.Account, addressStrings(to), addressStrings(cc), subject, opts.Body, original.MessageID, refs)
}

// replyRecipients picks the recipients of a reply: Reply-To when the
// original set one, else From; with replyAll, the original To and Cc
// as well, minus the account's own address and minus duplicates.
func (t *Tools) replyRecipients(account string, original *Message, replyAll bool) (to, cc []Address, err error) {
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

	acctCfg, err := t.manager.AccountConfig(account)
	if err != nil {
		return nil, nil, err
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
		return "", fmt.Errorf("uids is required: pass uids (array of integers) or uid (single integer) from an email_list or email_search result in the same account and folder")
	}
	if opts.Destination == "" {
		return "", fmt.Errorf("destination is required: an existing folder in the same account (see email_folders)")
	}

	client, err := t.manager.Account(opts.Account)
	if err != nil {
		return "", err
	}

	result, err := client.MoveMessages(ctx, opts)
	if err != nil {
		return "", err
	}

	if result.DestUIDsKnown {
		return fmt.Sprintf("Moved %d message(s) from %s to %s; new UIDs in %s: %v", len(result.UIDs), result.SourceFolder, result.Destination, result.Destination, result.DestUIDs), nil
	}
	return fmt.Sprintf("Moved %d message(s) from %s to %s (new UIDs unknown; list %s to find them)", len(result.UIDs), result.SourceFolder, result.Destination, result.Destination), nil
}

// sendEmail is the shared send path for HandleSend and HandleReply.
// It handles trust zone gating, auto-Bcc, message composition, and
// SMTP delivery.
func (t *Tools) sendEmail(ctx context.Context, account string, to, cc []string, subject, body, inReplyTo string, references []string) (string, error) {
	acctCfg, err := t.manager.AccountConfig(account)
	if err != nil {
		return "", err
	}

	if !acctCfg.SMTPConfigured() {
		return "", fmt.Errorf("SMTP not configured for account %q; this account can read and organize mail but not send it", acctCfg.Name)
	}

	// Auto-Bcc owner if configured and not already a recipient.
	var bcc []string
	if owner := t.manager.BccOwner(); owner != "" {
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
	trust := CheckRecipientTrust(t.contacts, allRecipients)
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

	if err := sendMail(ctx, acctCfg.Name, acctCfg.SMTP, composed.From.Address, smtpRecipients, composed.Bytes); err != nil {
		return "", err
	}

	t.logger.Info("email sent",
		"account", acctCfg.Name,
		"message_id", composed.MessageID,
		"recipient_count", len(smtpRecipients),
		"in_reply_to", inReplyTo,
	)

	// Store a copy in the configured Sent folder via IMAP APPEND.
	if acctCfg.SentFolder != "" {
		client, err := t.manager.Account(account)
		if err != nil {
			t.logger.Warn("failed to retrieve account for storing sent message",
				"folder", acctCfg.SentFolder,
				"account", acctCfg.Name,
				"message_id", composed.MessageID,
				"error", err,
			)
		} else if _, appendErr := client.AppendMessage(ctx, acctCfg.SentFolder, composed.Bytes, []imap.Flag{imap.FlagSeen}); appendErr != nil {
			t.logger.Warn("failed to store sent message in IMAP folder",
				"folder", acctCfg.SentFolder,
				"account", acctCfg.Name,
				"message_id", composed.MessageID,
				"error", appendErr,
			)
		}
	}

	return fmt.Sprintf("Email sent to %s — subject: %s (Message-ID: %s)", strings.Join(to, ", "), subject, composed.MessageID), nil
}
