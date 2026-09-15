package email

import (
	"context"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/tools"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// maxEscalateReasonBytes bounds email_escalate's reason, which rides
// the queued item to the review pass.
const maxEscalateReasonBytes = 500

// escalateResponse is email_escalate's result. PendingForReview is null
// when the queue could not be counted, which is not the same as zero.
type escalateResponse struct {
	Queued           bool   `json:"queued"`
	Subject          string `json:"subject"`
	PendingForReview *int   `json:"pending_for_review"`
}

func (t *Tools) escalateToolDefinition() *tools.Tool {
	return &tools.Tool{
		Name: "email_escalate",
		Description: "Hand one message to the account's review pass: the loop its Email Accounts entry names as review_loop, which reads the message again in full and may use a more capable model. " +
			"Use it for a message you cannot judge, or whose answer needs more than you can write well from the message itself. It changes nothing in the mailbox: the message stays where it is, unread if it was. " +
			"The message is queued as subject message:<account>:<message_id>, keyed by its Message-ID so the review pass finds it wherever it is filed; escalating it again only replaces the reason. The review pass is woken once the account's queued work has gathered. " +
			"Returns JSON {queued: true, subject, pending_for_review}: pending_for_review counts the account's queued review work, this message included, and is null when it could not be counted. " +
			"An account whose entry shows no review_loop has no review pass, and the call is refused with nothing queued: flag the message with email_mark flag \"flagged\" so the operator sees it instead. A message without a Message-ID, and a call from the review pass itself, are refused the same way. " +
			"The UID must be given with the account and folder it was listed from.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"uid": map[string]any{
					"type":        "integer",
					"description": "UID (integer) of the message to hand over, from an email_list or email_search result or a wake event.",
				},
				"folder": folderParameter("Folder containing the message."),
				"reason": map[string]any{
					"type":        "string",
					"description": "One sentence, at most 500 bytes, on why the message needs the review pass: what you could not judge, or what its answer needs. The review pass reads it beside the message.",
				},
				"account": accountParameter(),
			},
			"required": []string{"uid", "reason"},
		},
		ContentResolveExempt: []string{"account", "folder", "reason"},
		Handler:              t.HandleEscalate,
	}
}

// HandleEscalate queues one message for the account's review loop.
func (t *Tools) HandleEscalate(ctx context.Context, args map[string]any) (string, error) {
	const tool = "email_escalate"
	uid := toolargs.Uint32(args, "uid")
	folder := normalizeFolder(toolargs.TrimmedString(args, "folder"))
	reason := toolargs.TrimmedString(args, "reason")
	var problems []string
	if uid == 0 {
		problems = append(problems, "uid is required (integer > 0): the UID of the message to hand over, from an email_list or email_search result or a wake event")
	}
	switch {
	case reason == "":
		problems = append(problems, "reason is required (string): one sentence on why the message needs the review pass")
	case len(reason) > maxEscalateReasonBytes:
		problems = append(problems, fmt.Sprintf("reason is %d bytes and may be at most %d: say why in one sentence", len(reason), maxEscalateReasonBytes))
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("%s", strings.Join(problems, "; "))
	}

	s := t.service
	acct, err := s.ResolveAccount(ctx, toolargs.TrimmedString(args, "account"))
	if err != nil {
		return "", err
	}
	flagInstead := fmt.Sprintf("Flag it for the operator instead: email_mark {account: %q, folder: %q, uids: [%d], flag: \"flagged\"}", acct.Name, folder, uid)
	reviewLoop := acct.Config.ReviewLoopName()
	switch {
	case reviewLoop == "":
		return "", fmt.Errorf("%s: account %q has no review_loop, so there is no review pass to hand this message to, and nothing was queued. %s", tool, acct.Name, flagInstead)
	case s.queue == nil:
		return "", fmt.Errorf("%s: this Thane runs without a loop work queue, so nothing can be queued for review on account %q, and nothing was. %s", tool, acct.Name, flagInstead)
	case callerLoopName(ctx) == reviewLoop:
		return "", fmt.Errorf("%s: this turn is account %q's review pass (%s), and there is no pass after it, so nothing was queued. %s", tool, acct.Name, reviewLoop, flagInstead)
	}

	envs, err := acct.Client.FetchEnvelopes(ctx, folder, []uint32{uid})
	if err != nil {
		return "", t.refreshOnFolderMiss(ctx, acct, err)
	}
	if len(envs) == 0 {
		return "", fmt.Errorf("%s: folder %q of account %q holds no message with uid %d, so nothing was queued; it may have been moved. List or search the folder again for its current UID", tool, folder, acct.Name, uid)
	}
	env := envs[0]
	messageID := normalizeMessageID(env.MessageID)
	if messageID == "" {
		return "", fmt.Errorf("%s: message uid %d in %q of account %q has no Message-ID, which is how the review pass finds a message wherever it is filed, so it cannot be queued and nothing was. %s", tool, uid, folder, acct.Name, flagInstead)
	}

	subject := messageReviewSubject(acct.Name, messageID)
	summary := promptfmt.MarshalCompact(map[string]any{
		"account":    acct.Name,
		"folder":     folder,
		"message_id": messageID,
		"from":       env.From.String(),
		// message_subject, not subject: the item's own subject is its
		// queue key, the one queue_ack takes.
		"message_subject": env.Subject,
		"reason":          reason,
	})
	pending, err := s.enqueueReview(ctx, reviewLoop, acct.Name, subject, reviewTypeMessage, summary)
	if err != nil {
		return "", fmt.Errorf("%s: queueing message uid %d of account %q for review failed (%w), and nothing was queued. %s", tool, uid, acct.Name, err, flagInstead)
	}
	s.logger.Info("email message escalated for review",
		"account", acct.Name,
		"folder", folder,
		"uid", uid,
		"message_id", messageID,
		"review_loop", reviewLoop,
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
	s.recordOp(tool, acct.Name, folder, subject)
	return marshalResponse(escalateResponse{Queued: true, Subject: subject, PendingForReview: pending})
}
