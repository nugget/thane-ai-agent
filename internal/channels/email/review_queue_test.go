package email

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/state/loopqueue"
)

const testReviewLoop = "email-draft-review"

func testReviewQueue(t *testing.T) *loopqueue.Store {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	queue, err := loopqueue.NewStore(db, nil)
	if err != nil {
		t.Fatalf("new loopqueue store: %v", err)
	}
	return queue
}

// reviewFixture is a drafts-delivery account with a review queue, and a
// review_loop unless reviewLoop is empty.
func reviewFixture(t *testing.T, reviewLoop string, tweak func(*AccountConfig)) (draftFixture, *loopqueue.Store) {
	t.Helper()
	queue := testReviewQueue(t)
	svc, mem, _ := policyService(t, ServiceDependencies{Contacts: identityStub(), Queue: queue}, func(cfg *Config) {
		cfg.Accounts[0].Policy.Delivery = DeliveryDrafts
		cfg.Accounts[0].Mailbox.ReviewLoop = reviewLoop
		if tweak != nil {
			tweak(&cfg.Accounts[0])
		}
	})
	return draftFixture{svc: svc, mem: mem}, queue
}

func pendingSubjects(t *testing.T, queue *loopqueue.Store, loop string) []string {
	t.Helper()
	items, err := queue.PeekAll(context.Background(), loop)
	if err != nil {
		t.Fatalf("peek %s: %v", loop, err)
	}
	subjects := make([]string, 0, len(items))
	for _, it := range items {
		subjects = append(subjects, it.DedupKey)
	}
	return subjects
}

// TestDraftsQueueForReview pins when a draft is queued for the review
// pass: an unattended draft by another loop is, and a draft the operator
// watched being written, a draft by the review loop itself, or a draft
// on an account with no review_loop is not.
func TestDraftsQueueForReview(t *testing.T) {
	tests := []struct {
		name       string
		reviewLoop string
		ctx        context.Context
		wantQueued bool
	}{
		{"unattended draft by the triage pass", testReviewLoop, loopCtx(OwnerTriageLoopName), true},
		{"unattended draft outside any loop", testReviewLoop, context.Background(), true},
		{"the operator's own turn", testReviewLoop, attendedCtx(), false},
		{"the review loop's own draft", testReviewLoop, loopCtx(testReviewLoop), false},
		{"no review_loop", "", loopCtx(OwnerTriageLoopName), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, queue := reviewFixture(t, tt.reviewLoop, nil)
			resp, _ := f.replyDraft(t, tt.ctx, "Saturday", "Yes, Saturday works.")
			got := pendingSubjects(t, queue, testReviewLoop)
			if !tt.wantQueued {
				if len(got) != 0 {
					t.Fatalf("queued %v, want nothing", got)
				}
				return
			}
			if want := "draft:primary:" + resp.DraftID; len(got) != 1 || got[0] != want {
				t.Fatalf("queued %v, want [%s]", got, want)
			}
			items, _ := queue.PeekAll(context.Background(), testReviewLoop)
			var payload messages.LoopNotifyPayload
			if err := json.Unmarshal(items[0].Payload, &payload); err != nil {
				t.Fatalf("payload: %v", err)
			}
			ev := payload.Events[0]
			if ev.Source != "email_review" || ev.Type != "draft" || ev.Metadata["account"] != "primary" {
				t.Errorf("queued event = %+v", ev)
			}
			mustContain(t, ev.Summary, `"draft_id":"`+resp.DraftID+`"`, `"account":"primary"`, `"message_subject":"Re: Saturday"`)
			if strings.Contains(ev.Summary, `"subject"`) {
				t.Errorf("summary %s has a subject key beside the item's own queue subject", ev.Summary)
			}
		})
	}
}

// TestReviewQueueCoalescesRepeats pins that queueing the same subject
// twice leaves one pending item, the later payload winning.
func TestReviewQueueCoalescesRepeats(t *testing.T) {
	f, queue := reviewFixture(t, testReviewLoop, nil)
	ctx := context.Background()
	for _, summary := range []string{`{"n":1}`, `{"n":2}`} {
		if _, err := f.svc.enqueueReview(ctx, testReviewLoop, "primary", draftReviewSubject("primary", "d-1"), reviewTypeDraft, summary); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	items, err := queue.PeekAll(ctx, testReviewLoop)
	if err != nil || len(items) != 1 || items[0].DedupKey != "draft:primary:d-1" {
		t.Fatalf("pending = %+v (%v), want one draft:primary:d-1", items, err)
	}
	if !strings.Contains(string(items[0].Payload), `{\"n\":2}`) {
		t.Errorf("payload = %s, want the later summary", items[0].Payload)
	}
	if c, ok := f.svc.cachedPendingReview("primary"); !ok || c.Pending != 1 {
		t.Errorf("cached pending = %+v ok=%v, want 1", c, ok)
	}
}

func decodeEscalate(t *testing.T, out string) escalateResponse {
	t.Helper()
	var resp escalateResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("escalate result is not JSON: %v\n%s", err, out)
	}
	return resp
}

// TestEscalateQueuesTheMessage pins email_escalate's success: the
// message is queued under its account and Message-ID with the reason,
// a repeat coalesces, and the count comes back.
func TestEscalateQueuesTheMessage(t *testing.T) {
	f, queue := reviewFixture(t, testReviewLoop, nil)
	uid := f.deliverOriginal("Lease renewal")
	ctx := loopCtx(OwnerTriageLoopName)
	args := map[string]any{"uid": float64(uid), "reason": "Needs the lease terms checked."}

	out, err := f.svc.ToolProvider().HandleEscalate(ctx, args)
	if err != nil {
		t.Fatalf("escalate: %v", err)
	}
	resp := decodeEscalate(t, out)
	wantSubject := "message:primary:" + normalizeMessageID(messageIDFor("Lease renewal"))
	if !resp.Queued || resp.Subject != wantSubject || resp.PendingForReview == nil || *resp.PendingForReview != 1 {
		t.Fatalf("escalate = %+v, want queued %s with 1 pending", resp, wantSubject)
	}
	items, _ := queue.PeekAll(context.Background(), testReviewLoop)
	if len(items) != 1 {
		t.Fatalf("pending = %d, want 1", len(items))
	}
	mustContain(t, string(items[0].Payload), `\"folder\":\"INBOX\"`, `\"reason\":\"Needs the lease terms checked.\"`, `\"message_subject\":\"Lease renewal\"`, `"type":"message"`)
	if strings.Contains(string(items[0].Payload), `\"subject\"`) {
		t.Errorf("summary in %s has a subject key beside the item's own queue subject", items[0].Payload)
	}

	args["reason"] = "Second look."
	if _, err := f.svc.ToolProvider().HandleEscalate(ctx, args); err != nil {
		t.Fatalf("second escalate: %v", err)
	}
	if got := pendingSubjects(t, queue, testReviewLoop); len(got) != 1 || got[0] != wantSubject {
		t.Errorf("after a repeat, pending = %v, want only %s", got, wantSubject)
	}
}

// TestEscalateRefusals pins what email_escalate refuses and that each
// refusal queues nothing and names the flag instead.
func TestEscalateRefusals(t *testing.T) {
	tests := []struct {
		name       string
		reviewLoop string
		ctx        context.Context
		args       func(uid uint32) map[string]any
		want       []string
	}{
		{"no review_loop", "", loopCtx(OwnerTriageLoopName), func(uid uint32) map[string]any {
			return map[string]any{"uid": float64(uid), "reason": "unsure"}
		}, []string{`account "primary" has no review_loop`, "nothing was queued", `email_mark {account: "primary", folder: "INBOX"`, `flag: "flagged"`}},
		{"the review pass itself", testReviewLoop, loopCtx(testReviewLoop), func(uid uint32) map[string]any {
			return map[string]any{"uid": float64(uid), "reason": "unsure"}
		}, []string{"review pass", "no pass after it", `flag: "flagged"`}},
		{"missing uid and reason", testReviewLoop, loopCtx(OwnerTriageLoopName), func(uint32) map[string]any {
			return map[string]any{}
		}, []string{"uid is required", "reason is required"}},
		{"unknown uid", testReviewLoop, loopCtx(OwnerTriageLoopName), func(uint32) map[string]any {
			return map[string]any{"uid": float64(999), "reason": "unsure"}
		}, []string{"holds no message with uid 999"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, queue := reviewFixture(t, tt.reviewLoop, nil)
			uid := f.deliverOriginal("Question")
			_, err := f.svc.ToolProvider().HandleEscalate(tt.ctx, tt.args(uid))
			if err == nil {
				t.Fatal("escalate succeeded, want a refusal")
			}
			mustContain(t, err.Error(), tt.want...)
			if got := pendingSubjects(t, queue, testReviewLoop); len(got) != 0 {
				t.Errorf("queued %v after a refusal", got)
			}
		})
	}
}

// emailAccountEntry renders the block and returns the first account's
// entry as a map.
func emailAccountEntry(t *testing.T, svc *Service) map[string]any {
	t.Helper()
	out, err := svc.ContextProvider().buildContext("", false)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var block struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(out, "### Email Accounts\n\n")), &block); err != nil {
		t.Fatalf("block is not JSON: %v\n%s", err, out)
	}
	return block.Accounts[0]
}

// TestEmailAccountsEntryRendersRouting pins the routing keys in the
// block: wake_loop names the loop the account's new mail wakes on every
// entry while mail is polled, the owner's default included, and none
// when polling is off; review_loop renders when set, polled or not; and
// pending_review comes from the cache alone.
func TestEmailAccountsEntryRendersRouting(t *testing.T) {
	tests := []struct {
		name       string
		pollingOff bool
		mailbox    MailboxConfig
		wantWake   any
		wantReview any
	}{
		{"operator at its default", false, MailboxConfig{Owner: "operator"}, OwnerTriageLoopName, nil},
		{"assistant at its default", false, MailboxConfig{}, DefaultHandlerLoopName, nil},
		{"operator kept on the default handler", false, MailboxConfig{Owner: "operator", WakeLoop: DefaultHandlerLoopName}, DefaultHandlerLoopName, nil},
		{"assistant routed to its own loop", false, MailboxConfig{WakeLoop: " mail-sorter "}, "mail-sorter", nil},
		{"assistant with a review pass", false, MailboxConfig{ReviewLoop: testReviewLoop}, DefaultHandlerLoopName, testReviewLoop},
		{"polling off, operator at its default", true, MailboxConfig{Owner: "operator"}, nil, nil},
		{"polling off, an explicit wake_loop", true, MailboxConfig{WakeLoop: "mail-sorter"}, nil, nil},
		{"polling off, a review pass still shows", true, MailboxConfig{Owner: "operator", ReviewLoop: testReviewLoop}, nil, testReviewLoop},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _ := policyService(t, ServiceDependencies{Contacts: identityStub(), Queue: testReviewQueue(t)}, func(cfg *Config) {
				cfg.Accounts[0].Mailbox = tt.mailbox
				if tt.pollingOff {
					zero := 0
					cfg.PollInterval = &zero
				}
			})
			if svc.PollingEnabled() == tt.pollingOff {
				t.Fatalf("PollingEnabled() = %v, want %v", svc.PollingEnabled(), !tt.pollingOff)
			}
			entry := emailAccountEntry(t, svc)
			if entry["wake_loop"] != tt.wantWake || entry["review_loop"] != tt.wantReview {
				t.Errorf("wake_loop = %v, review_loop = %v; want %v, %v", entry["wake_loop"], entry["review_loop"], tt.wantWake, tt.wantReview)
			}
			if _, ok := entry["pending_review"]; ok {
				t.Errorf("pending_review rendered before anything was counted: %v", entry["pending_review"])
			}
		})
	}
}

// TestPendingReviewIsCachedNotCounted pins that the block reads the
// cached count: work queued behind the service's back does not show
// until a poll recounts it.
func TestPendingReviewIsCachedNotCounted(t *testing.T) {
	f, queue := reviewFixture(t, testReviewLoop, func(a *AccountConfig) { a.Mailbox.Owner = "operator" })
	ctx := context.Background()
	if _, err := f.svc.enqueueReview(ctx, testReviewLoop, "primary", draftReviewSubject("primary", "d-1"), reviewTypeDraft, "{}"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if got := emailAccountEntry(t, f.svc)["pending_review"]; got != float64(1) {
		t.Fatalf("pending_review = %v, want 1", got)
	}

	// Queued by another producer; the render must not count it.
	payload, _ := json.Marshal(messages.LoopNotifyPayload{Events: []messages.LoopEventPayload{{Source: reviewSource, Metadata: map[string]string{"account": "primary"}}}})
	if err := queue.Enqueue(ctx, testReviewLoop, "message:primary:x@example.com", 0, payload); err != nil {
		t.Fatalf("enqueue behind the service: %v", err)
	}
	entry := emailAccountEntry(t, f.svc)
	if got := entry["pending_review"]; got != float64(1) {
		t.Fatalf("pending_review = %v after a hidden enqueue, want the cached 1", got)
	}
	if _, ok := entry["pending_review_as_of"]; !ok {
		t.Error("pending_review_as_of missing beside pending_review")
	}

	if _, err := f.svc.CheckNewMessages(ctx); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if got := emailAccountEntry(t, f.svc)["pending_review"]; got != float64(2) {
		t.Errorf("pending_review = %v after a poll, want 2", got)
	}
}

// TestPollerReconcilesTheDraftLedger pins the upkeep after a poll: a
// draft the operator discarded closes without any draft tool being
// called.
func TestPollerReconcilesTheDraftLedger(t *testing.T) {
	f, _ := reviewFixture(t, "", nil)
	resp, _ := f.replyDraft(t, loopCtx(OwnerTriageLoopName), "Dinner", "Dinner at seven works.")
	if err := f.mem.operator().discard("Drafts", resp.DraftUID); err != nil {
		t.Fatalf("operator discard: %v", err)
	}
	if got := f.entry(t, resp.DraftID).Stage; got != DraftStageOpen {
		t.Fatalf("stage before the poll = %s, want open", got)
	}
	if _, err := f.svc.CheckNewMessages(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	e := f.entry(t, resp.DraftID)
	if e.Stage != DraftStageGone || e.ClosedReason != closedVanished {
		t.Errorf("after a poll, entry = %s/%s, want gone/%s", e.Stage, e.ClosedReason, closedVanished)
	}
}
