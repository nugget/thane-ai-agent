package email

import (
	"context"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// The outbound handlers live apart from the read-side handlers because
// they share one path, [Service.Send], which is where every policy
// decision about outbound mail is made: access, the audit copy, the
// trust gate, delivery routing, inspection, signing, and the SMTP or
// Drafts hand-off. The handlers only assemble the request and render
// the outcome.

// HandleSend composes a new message and hands it to the send pipeline.
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
	outcome, err := t.service.Send(ctx, SendRequest{
		Tool:    "email_send",
		Account: acct,
		To:      opts.To,
		Cc:      opts.Cc,
		Subject: opts.Subject,
		Body:    opts.Body,
		Draft:   toolargs.Bool(args, "draft"),
	})
	if err != nil {
		return "", err
	}
	return marshalResponse(newSendResponse(outcome, opts.Subject, ""))
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
	// Refuse before fetching the original when the account cannot
	// write mail at all; the fetch would only delay the same answer.
	if err := t.service.sendAccessRefusal(ctx, "email_reply", acct); err != nil {
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

	outcome, err := t.service.Send(ctx, SendRequest{
		Tool:       "email_reply",
		Account:    acct,
		To:         addressStrings(to),
		Cc:         addressStrings(cc),
		Subject:    subject,
		Body:       opts.Body,
		InReplyTo:  original.MessageID,
		References: refs,
		Draft:      toolargs.Bool(args, "draft"),
	})
	if err != nil {
		return "", err
	}
	return marshalResponse(newSendResponse(outcome, subject, original.MessageID))
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
