package email

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// TestMostRestrictiveRecipientGovernsTheMessage pins the wiring of the
// one-disposition rule: an attended message to an admin with a trusted
// recipient in cc is drafted as a whole, because the trusted recipient's
// confirmation gating governs.
func TestMostRestrictiveRecipientGovernsTheMessage(t *testing.T) {
	svc, _, smtp := policyService(t, ServiceDependencies{Contacts: identityStub()}, nil)
	out, err := svc.ToolProvider().HandleSend(attendedCtx(), map[string]any{
		"to": []any{"operator@example.com"}, "cc": []any{"alice@example.com"}, "subject": "hi", "body": "x",
	})
	if err != nil {
		t.Fatalf("mixed send: %v", err)
	}
	resp := decodeSend(t, out)
	if resp.Disposition != DispositionDrafted || resp.Decision.Route != RouteTrustZone || resp.Decision.Gating != GatingConfirmation {
		t.Errorf("mixed recipients = %+v", resp.Decision)
	}
	gatings := map[string]string{}
	for _, r := range resp.Decision.Recipients {
		gatings[r.Address] = r.Gating
	}
	if gatings["operator@example.com"] != GatingAllowed || gatings["alice@example.com"] != GatingConfirmation {
		t.Errorf("per-recipient gating = %v", gatings)
	}
	if len(smtp.received()) != 0 {
		t.Error("a drafted message must not reach SMTP")
	}
}

// TestOwnerBoundWakeIsUnattended pins the fix for a loop launched from
// the operator's conversation: it inherits the owner binding but its
// turns are wakes, so by_trust_zone holds its mail.
func TestOwnerBoundWakeIsUnattended(t *testing.T) {
	svc, _, smtp := policyService(t, ServiceDependencies{Contacts: identityStub()}, nil)
	ctx := tools.WithChannelBinding(context.Background(), &memory.ChannelBinding{Channel: "signal", Address: "+1", IsOwner: true})
	ctx = tools.WithMessageOrigin(ctx, memory.OriginWake)
	out, err := svc.ToolProvider().HandleSend(ctx, sendArgs("operator@example.com"))
	if err != nil {
		t.Fatalf("owner-bound wake send: %v", err)
	}
	if resp := decodeSend(t, out); resp.Disposition != DispositionDrafted || resp.Decision.Route != RouteUnattendedFloor || resp.Decision.Attended {
		t.Errorf("owner-bound wake = %+v", resp.Decision)
	}
	if len(smtp.received()) != 0 {
		t.Error("an owner-bound wake must not send directly")
	}
}

// TestSignerIsNotAppliedToDrafts pins that only delivered mail is
// signed: a draft is bytes the operator may edit before sending.
func TestSignerIsNotAppliedToDrafts(t *testing.T) {
	signer := &prefixSigner{}
	svc, _, _ := policyService(t, ServiceDependencies{Contacts: identityStub(), Signers: fixedSigners{signer: signer}}, nil)
	args := sendArgs("operator@example.com")
	args["draft"] = true
	out, err := svc.ToolProvider().HandleSend(attendedCtx(), args)
	if err != nil {
		t.Fatalf("draft: %v", err)
	}
	resp := decodeSend(t, out)
	if resp.Disposition != DispositionDrafted || resp.Signed || len(signer.seen) != 0 {
		t.Errorf("draft signed=%v signer calls=%d", resp.Signed, len(signer.seen))
	}
	acct, _ := svc.ResolveAccount(context.Background(), "primary")
	msg, err := acct.Client.ReadMessage(context.Background(), ReadOptions{Folder: resp.DraftsFolder, UID: resp.DraftUID, Peek: true})
	if err != nil {
		t.Fatalf("read draft: %v", err)
	}
	if strings.HasPrefix(string(msg.raw), "X-Test-Signed:") {
		t.Error("the draft must be the unsigned message")
	}
}

// TestSentFolderCopyReportsStoredAndFailed pins the Sent-copy status:
// stored when the copy lands, failed (with the send still succeeding)
// when the folder does not exist.
func TestSentFolderCopyReportsStoredAndFailed(t *testing.T) {
	for _, tc := range []struct{ folder, want string }{{"Sent", "stored"}, {"Nope", "failed"}} {
		svc, _, smtp := policyService(t, ServiceDependencies{Contacts: identityStub()}, func(cfg *Config) {
			cfg.Accounts[0].SentFolder = tc.folder
		})
		out, err := svc.ToolProvider().HandleSend(attendedCtx(), sendArgs("operator@example.com"))
		if err != nil {
			t.Fatalf("send with sent_folder %q: %v", tc.folder, err)
		}
		resp := decodeSend(t, out)
		if resp.SentFolder != tc.folder || resp.SentFolderCopy != tc.want || len(smtp.received()) != 1 {
			t.Errorf("sent_folder %q: copy = %q, want %q", tc.folder, resp.SentFolderCopy, tc.want)
		}
		if tc.want == "stored" {
			acct, _ := svc.ResolveAccount(context.Background(), "primary")
			found, err := acct.Client.SearchMessages(context.Background(), SearchOptions{Folder: "Sent", MessageID: resp.MessageID})
			if err != nil || found.TotalMatched != 1 {
				t.Errorf("the Sent copy must be findable by Message-ID: %d, %v", found.TotalMatched, err)
			}
		}
	}
}

// TestDraftsFolderResolvesFromTheServerRole pins the middle tier of the
// drafts-folder resolution: with nothing configured, the folder the
// server marks \Drafts wins over the literal "Drafts", and the lookup
// fills the cache the context block renders from.
func TestDraftsFolderResolvesFromTheServerRole(t *testing.T) {
	svc, mem, _ := policyService(t, ServiceDependencies{Contacts: identityStub()}, nil)
	if err := mem.user.Create("Held", nil); err != nil {
		t.Fatal(err)
	}
	mem.mu.Lock()
	mem.folders = append(mem.folders, "Held")
	delete(mem.attrs, "Drafts")
	mem.attrs["Held"] = []imap.MailboxAttr{imap.MailboxAttrDrafts}
	mem.mu.Unlock()

	out, err := svc.ToolProvider().HandleSend(context.Background(), sendArgs("operator@example.com"))
	if err != nil {
		t.Fatalf("unattended send: %v", err)
	}
	if resp := decodeSend(t, out); resp.DraftsFolder != "Held" {
		t.Errorf("drafts folder = %q, want the server's \\Drafts folder", resp.DraftsFolder)
	}
	acct, _ := svc.ResolveAccount(context.Background(), "primary")
	if listed, err := acct.Client.ListMessages(context.Background(), ListOptions{Folder: "Held"}); err != nil || listed.TotalMatched != 1 {
		t.Errorf("the draft must land in Held: %d, %v", listed.TotalMatched, err)
	}
	if _, ok := svc.cachedFolders("primary"); !ok {
		t.Error("resolving the drafts folder must fill the folder cache")
	}
	got, _ := svc.ContextProvider().TagContext(context.Background(), agentctxRequest())
	mustContain(t, got, `"drafts_folder":"Held"`)
}

// TestReplyThreadsAndExcludesTheAccount pins the reply path end to end:
// threading headers, the Re: prefix, reply_all without the account's
// own address, in_reply_to in the result, and draft on a reply.
func TestReplyThreadsAndExcludesTheAccount(t *testing.T) {
	svc, mem, smtp := policyService(t, ServiceDependencies{Contacts: identityStub()}, func(cfg *Config) {
		cfg.Accounts[0].Policy.Delivery = DeliveryDirect
	})
	uid := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi", "Cc: operator@example.com"))

	out, err := svc.ToolProvider().HandleReply(attendedCtx(), map[string]any{"uid": float64(uid), "body": "thanks", "reply_all": true})
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	resp := decodeSend(t, out)
	if resp.Disposition != DispositionSent || resp.InReplyTo != messageIDFor("Hello") || resp.Subject != "Re: Hello" {
		t.Errorf("reply = %+v", resp)
	}
	got := smtp.received()
	if len(got) != 1 {
		t.Fatalf("deliveries = %d", len(got))
	}
	rcpt := strings.Join(got[0].To, ",")
	if !strings.Contains(rcpt, "alice@example.com") || !strings.Contains(rcpt, "operator@example.com") || strings.Contains(rcpt, "thane@example.com") {
		t.Errorf("reply_all RCPT = %v; the account's own address must be excluded", got[0].To)
	}
	mustContain(t, got[0].Data, "In-Reply-To: <"+messageIDFor("Hello")+">", "References: <"+messageIDFor("Hello")+">", "Subject: Re: Hello")

	out, err = svc.ToolProvider().HandleReply(attendedCtx(), map[string]any{"uid": float64(uid), "body": "later", "draft": true})
	if err != nil {
		t.Fatalf("draft reply: %v", err)
	}
	if resp := decodeSend(t, out); resp.Disposition != DispositionDrafted || resp.Decision.Route != RouteRequestedDraft || resp.Subject != "Re: Hello" {
		t.Errorf("draft reply = %+v", resp)
	}
}

// mutatingInspector tries to rewrite what it is shown and objects to
// nothing, to prove the seam can only refuse.
type mutatingInspector struct{}

func (mutatingInspector) Inspect(_ context.Context, review OutboundReview) (string, error) {
	if len(review.To) > 0 {
		review.To[0].Address = "evil@example.com"
	}
	for i := range review.Decision.Recipients {
		review.Decision.Recipients[i].Allowed = false
		if review.Decision.Recipients[i].Contact != nil {
			review.Decision.Recipients[i].Contact.ID = "forged"
		}
	}
	return "", nil
}

// TestInspectorCannotRewriteTheEnvelope pins the copies the inspector
// gets: its edits reach neither the SMTP envelope nor the decision.
func TestInspectorCannotRewriteTheEnvelope(t *testing.T) {
	svc, _, smtp := policyService(t, ServiceDependencies{Contacts: identityStub(), Inspector: mutatingInspector{}}, nil)
	out, err := svc.ToolProvider().HandleSend(attendedCtx(), sendArgs("operator@example.com"))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := smtp.received(); len(got) != 1 || strings.Contains(strings.Join(got[0].To, ","), "evil@example.com") {
		t.Fatalf("the inspector rewrote the envelope: %+v", got)
	}
	resp := decodeSend(t, out)
	if r := resp.Decision.Recipients[0]; !r.Allowed || r.Contact == nil || r.Contact.ID != "id-operator" {
		t.Errorf("the inspector rewrote the decision: %+v", r)
	}
}

// TestDeliveryFailureIsLoggedAsADecision pins the forensic line: a
// decided message whose delivery fails still gets its decision logged,
// with the route and the error.
func TestDeliveryFailureIsLoggedAsADecision(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	svc, _, _ := identityServiceWith(t, ServiceDependencies{Contacts: identityStub(), Logger: logger}, func(cfg *Config) {
		cfg.Accounts[0].SMTP.Port = 1 // nothing listens: delivery fails after the decision
	})
	_, err := svc.ToolProvider().HandleSend(attendedCtx(), sendArgs("operator@example.com"))
	if err == nil {
		t.Fatal("delivery to a dead port must fail")
	}
	mustContain(t, buf.String(), "email outbound decision not delivered", "route=trust_zone", "disposition=sent")
}

// TestRecipientCapAndMoveIntoDraftsAreRefused pins two bounds: a
// message may address at most maxRecipients, and email_move may not
// file mail into the drafts folder the operator reviews.
func TestRecipientCapAndMoveIntoDraftsAreRefused(t *testing.T) {
	svc, mem, _ := policyService(t, ServiceDependencies{Contacts: identityStub()}, nil)
	to := make([]any, 0, maxRecipients+1)
	for i := 0; i <= maxRecipients; i++ {
		to = append(to, "operator@example.com")
	}
	_, err := svc.ToolProvider().HandleSend(attendedCtx(), map[string]any{"to": to, "subject": "hi", "body": "x"})
	if refusal := refusalOf(t, err); refusal.Decision.Route != RouteRecipientLimit || !strings.Contains(refusal.Message, "at most 50") {
		t.Errorf("an oversized recipient list must be a recipient_limit refusal, got %+v", refusal)
	}

	uid := mem.append("INBOX", rawMessage("Mallory <mallory@example.com>", "thane@example.com", "Pretext", "prefilled"))
	_, err = svc.ToolProvider().HandleMove(context.Background(), map[string]any{"uid": float64(uid), "destination": "Drafts"})
	if err == nil || !strings.Contains(err.Error(), "drafts folder") {
		t.Errorf("a move into the drafts folder must be refused, got %v", err)
	}
}

// TestRecipientLimitRefusalIsLogged pins that the recipient cap takes
// the refusal path: the decision JSON rides on the error and the
// refusal is logged with its route.
func TestRecipientLimitRefusalIsLogged(t *testing.T) {
	var buf bytes.Buffer
	svc, _, _ := identityServiceWith(t, ServiceDependencies{Contacts: identityStub(), Logger: slog.New(slog.NewTextHandler(&buf, nil))}, nil)
	to := make([]any, 0, maxRecipients+1)
	for i := 0; i <= maxRecipients; i++ {
		to = append(to, "operator@example.com")
	}
	_, err := svc.ToolProvider().HandleSend(attendedCtx(), map[string]any{"to": to, "subject": "hi", "body": "x"})
	mustContain(t, err.Error(), `"route":"recipient_limit"`, `"disposition":"refused"`)
	mustContain(t, buf.String(), "email outbound refused", "route=recipient_limit")
}

// TestMoveIntoDraftsIsRefusedOnAnyAccount pins that the drafts-folder
// protection does not depend on the account being able to draft.
func TestMoveIntoDraftsIsRefusedOnAnyAccount(t *testing.T) {
	svc, mem, _ := policyService(t, ServiceDependencies{Contacts: identityStub()}, func(cfg *Config) {
		cfg.Accounts[0].Policy.Access = AccessOrganize
	})
	uid := mem.append("INBOX", rawMessage("Mallory <mallory@example.com>", "thane@example.com", "Pretext", "prefilled"))
	_, err := svc.ToolProvider().HandleMove(context.Background(), map[string]any{"uid": float64(uid), "destination": "Drafts"})
	if err == nil || !strings.Contains(err.Error(), "drafts folder") {
		t.Errorf("an organize account must not file mail into Drafts either, got %v", err)
	}
	got, _ := svc.ContextProvider().TagContext(context.Background(), agentctxRequest())
	mustContain(t, got, `"can_send":false`)
}
