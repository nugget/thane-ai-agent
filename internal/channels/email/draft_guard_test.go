package email

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

// TestThaneDraftAnnotationSurvivesARestart pins that thane_draft does not
// depend on a warm folder cache: after a restart, on an account that
// finds its drafts folder by the server's special-use mark rather than
// drafts_folder, email_list still marks Thane's open draft.
func TestThaneDraftAnnotationSurvivesARestart(t *testing.T) {
	mem := newMemIMAP(t)
	smtp := newSMTPFake(t, nil)
	store := testOpstate(t)
	cfg := Config{Accounts: []AccountConfig{{Name: "primary", IMAP: mem.imapConfig(), SMTP: smtp.config("thane", "pw"), DefaultFrom: "Thane <thane@example.com>"}}}
	cfg.Accounts[0].Policy.Delivery = DeliveryDrafts
	start := func() *Service {
		svc, err := NewService(cfg, ServiceDependencies{State: store, MessageBus: messages.NewBus(nil), Contacts: identityStub(), Logger: quietSlog()})
		if err != nil {
			t.Fatalf("NewService: %v", err)
		}
		t.Cleanup(svc.Close)
		return svc
	}
	resp, _ := draftFixture{svc: start(), mem: mem}.replyDraft(t, context.Background(), "Restart", "Still here.")

	out, err := start().ToolProvider().HandleList(context.Background(), map[string]any{"folder": "Drafts"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var list listResponse
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("list result is not JSON: %v", err)
	}
	if len(list.Messages) != 1 || list.Messages[0].ThaneDraft == nil || list.Messages[0].ThaneDraft.DraftID != resp.DraftID {
		t.Errorf("rows after a restart = %+v, want Thane's draft marked %s", list.Messages, resp.DraftID)
	}
}

// TestReplySentDirectlyNamesTheOpenDraft pins the note a reply sent
// directly carries while one of Thane's drafts answering the same
// message is still open, so that draft can be withdrawn before the
// operator sends it as a second answer; a reply with no such draft
// carries no note.
func TestReplySentDirectlyNamesTheOpenDraft(t *testing.T) {
	svc, mem, _ := policyService(t, ServiceDependencies{Contacts: identityStub()}, nil)
	reply := func(ctx context.Context, uid uint32) sendResponse {
		t.Helper()
		out, err := svc.ToolProvider().HandleReply(ctx, map[string]any{"uid": float64(uid), "body": "Saturday at ten."})
		if err != nil {
			t.Fatalf("reply: %v", err)
		}
		return decodeSend(t, out)
	}
	plan := mem.append("INBOX", rawMessage("Operator <operator@example.com>", "thane@example.com", "Plan", "What is the plan?"))
	draft := reply(loopCtx("email-owner-triage"), plan)
	if draft.Disposition != DispositionDrafted || draft.DraftID == "" {
		t.Fatalf("the wake's reply = %+v, want a draft", draft)
	}

	sent := reply(attendedCtx(), plan)
	if sent.Disposition != DispositionSent {
		t.Fatalf("the operator's reply = %+v, want sent", sent)
	}
	mustContain(t, sent.Note, draft.DraftID, "email_draft_withdraw", "second answer")

	other := mem.append("INBOX", rawMessage("Operator <operator@example.com>", "thane@example.com", "Other", "Another question?"))
	if plain := reply(attendedCtx(), other); plain.Disposition != DispositionSent || plain.Note != "" {
		t.Errorf("a reply with no open draft = %+v, want sent with no note", plain)
	}
}
