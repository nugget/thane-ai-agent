package email

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// Review work is queued in the review loop's own loopqueue partition,
// which is the partition its queue_pull drains. Each item's subject is
// its identity, so queueing the same work again coalesces into the one
// pending item instead of adding a second:
//
//   - draft:<draft_id> for a draft an unattended turn wrote on an account
//     with a review_loop;
//   - message:<account>:<message_id> for a message email_escalate handed
//     over, keyed by Message-ID rather than UID so it survives the
//     message being filed elsewhere.
//
// Nothing about review is written to the mailbox or the draft ledger:
// the queue is the only record that work awaits review, and the review
// loop's queue_ack is what removes it.

const (
	// reviewSource names the producer on every queued review item.
	reviewSource = "email_review"

	reviewSubjectDraftPrefix   = "draft:"
	reviewSubjectMessagePrefix = "message:"

	// reviewTypeDraft and reviewTypeMessage are the item types, also
	// how the review wake counts what is pending.
	reviewTypeDraft   = "draft"
	reviewTypeMessage = "message"
)

// reviewCount is one account's measured count of queued review work and
// when it was measured.
type reviewCount struct {
	Pending int
	At      time.Time
}

// draftReviewSubject is the queue subject of a draft awaiting review.
func draftReviewSubject(draftID string) string {
	return reviewSubjectDraftPrefix + draftID
}

// messageReviewSubject is the queue subject of an escalated message.
func messageReviewSubject(account, messageID string) string {
	return reviewSubjectMessagePrefix + account + ":" + normalizeMessageID(messageID)
}

// callerLoopName names the loop whose turn is calling, or "" outside a
// loop.
func callerLoopName(ctx context.Context) string {
	return strings.TrimSpace(tools.HintsFromContext(ctx)["loop_name"])
}

// queueDraftForReview queues a draft Send just recorded for the
// account's review loop. It queues nothing when the account has no
// review loop or the service no queue, when the operator is present for
// the turn (they are reading the draft as it is written), or when the
// review loop itself wrote the draft, which would otherwise wake it to
// review its own work. A failure is logged, never returned: the draft is
// already in the folder and recorded, and a draft that misses review is
// still the operator's to send or not.
func (s *Service) queueDraftForReview(ctx context.Context, req SendRequest, decision Decision, draftID string) {
	reviewLoop := req.Account.Config.ReviewLoopName()
	if draftID == "" || reviewLoop == "" || s.queue == nil || decision.Attended || callerLoopName(ctx) == reviewLoop {
		return
	}
	subject := draftReviewSubject(draftID)
	summary := promptfmt.MarshalCompact(map[string]any{
		"account":  req.Account.Name,
		"draft_id": draftID,
		// message_subject, not subject: the item's own subject is its
		// queue key, the one queue_ack takes.
		"message_subject": req.Subject,
		"drafted_by":      draftAuthor(ctx),
	})
	if _, err := s.enqueueReview(ctx, reviewLoop, req.Account.Name, subject, reviewTypeDraft, summary); err != nil {
		s.logger.Warn("email draft not queued for review",
			"draft_id", draftID,
			"account", req.Account.Name,
			"review_loop", reviewLoop,
			"loop_id", tools.LoopIDFromContext(ctx),
			"error", err,
		)
		return
	}
	s.logger.Info("email draft queued for review",
		"draft_id", draftID,
		"account", req.Account.Name,
		"review_loop", reviewLoop,
		"subject", subject,
		"loop_id", tools.LoopIDFromContext(ctx),
		"conversation_id", tools.ConversationIDFromContext(ctx),
	)
}

// enqueueReview queues one item for reviewLoop, coalescing with any
// pending item of the same subject, and returns the account's pending
// count afterwards, or nil when it could not be counted. The error is
// the enqueue's alone.
func (s *Service) enqueueReview(ctx context.Context, reviewLoop, account, subject, kind, summary string) (*int, error) {
	payload := messages.LoopNotifyPayload{Events: []messages.LoopEventPayload{{
		Source:     reviewSource,
		Type:       kind,
		ID:         subject,
		Summary:    summary,
		ObservedAt: time.Now().UTC(),
		Metadata:   map[string]string{"account": account},
	}}}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode review item %s: %w", subject, err)
	}
	if err := s.queue.Enqueue(ctx, reviewLoop, subject, 0, raw); err != nil {
		return nil, err
	}
	counts, err := s.measurePendingReview(ctx, reviewLoop)
	if err != nil {
		s.logger.Debug("email review queue not counted after enqueue", "review_loop", reviewLoop, "error", err)
		return nil, nil
	}
	n := counts[account]
	return &n, nil
}

// measurePendingReview counts reviewLoop's queued items per account and
// records the counts for every account that names it, zero included.
func (s *Service) measurePendingReview(ctx context.Context, reviewLoop string) (map[string]int, error) {
	items, err := s.queue.PeekAll(ctx, reviewLoop)
	if err != nil {
		return nil, fmt.Errorf("count review queue %q: %w", reviewLoop, err)
	}
	counts := make(map[string]int)
	for _, item := range items {
		if account := reviewItemAccount(item.Payload); account != "" {
			counts[account]++
		}
	}
	now := time.Now()
	s.reviewMu.Lock()
	defer s.reviewMu.Unlock()
	for _, cfg := range s.AccountsInConfigOrder() {
		if cfg.ReviewLoopName() == reviewLoop {
			s.pendingReview[cfg.Name] = reviewCount{Pending: counts[cfg.Name], At: now}
		}
	}
	return counts, nil
}

// reviewItemAccount reads the account a queued review item belongs to,
// or "" for an item this package did not write.
func reviewItemAccount(raw []byte) string {
	var p messages.LoopNotifyPayload
	if err := json.Unmarshal(raw, &p); err != nil || len(p.Events) == 0 || p.Events[0].Source != reviewSource {
		return ""
	}
	return p.Events[0].Metadata["account"]
}

// refreshPendingReview recounts every review loop's queue, once per
// loop however many accounts share it.
func (s *Service) refreshPendingReview(ctx context.Context) {
	if s.queue == nil {
		return
	}
	seen := make(map[string]bool)
	for _, cfg := range s.AccountsInConfigOrder() {
		loop := cfg.ReviewLoopName()
		if loop == "" || seen[loop] {
			continue
		}
		seen[loop] = true
		if _, err := s.measurePendingReview(ctx, loop); err != nil {
			s.logger.Warn("email review queue not counted", "review_loop", loop, "error", err)
		}
	}
}

// cachedPendingReview returns an account's most recent count of queued
// review work, or ok=false when it has not been counted yet.
func (s *Service) cachedPendingReview(account string) (reviewCount, bool) {
	s.reviewMu.Lock()
	defer s.reviewMu.Unlock()
	c, ok := s.pendingReview[account]
	return c, ok
}
