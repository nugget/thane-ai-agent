package email

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// attendedCtx is a turn the operator is present for.
func attendedCtx() context.Context {
	return tools.WithMessageOrigin(context.Background(), memory.OriginAPI)
}

// policyService is identityService with the account's policy adjusted.
func policyService(t *testing.T, deps ServiceDependencies, tweak func(*Config)) (*Service, *memIMAP, *smtpFake) {
	t.Helper()
	return identityServiceWith(t, deps, tweak)
}

func sendArgs(to string) map[string]any {
	return map[string]any{"to": []any{to}, "subject": "hi", "body": "hello"}
}

func decodeSend(t *testing.T, out string) sendResponse {
	t.Helper()
	var resp sendResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("send result is not JSON: %v\n%s", err, out)
	}
	return resp
}

func refusalOf(t *testing.T, err error) *PolicyRefusal {
	t.Helper()
	var refusal *PolicyRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error is not a policy refusal: %v", err)
	}
	return refusal
}

// TestByTrustZoneRoutesEachRecipientClass pins the default delivery
// mode end to end: an admin recipient sends directly when attended,
// drafts when unattended, and a trusted recipient drafts either way;
// the draft carries the Draft flag and the audit Bcc in its header,
// and nothing reaches SMTP.
func TestByTrustZoneRoutesEachRecipientClass(t *testing.T) {
	svc, imap, smtp := policyService(t, ServiceDependencies{Contacts: identityStub()}, func(cfg *Config) {
		cfg.BccOwner = "Audit <audit@example.com>"
	})
	send := svc.ToolProvider().HandleSend

	out, err := send(attendedCtx(), sendArgs("operator@example.com"))
	if err != nil {
		t.Fatalf("attended admin send: %v", err)
	}
	resp := decodeSend(t, out)
	if resp.Disposition != DispositionSent || resp.Decision.Route != RouteTrustZone || !resp.Decision.Attended || resp.Decision.Gating != GatingAllowed || resp.BccCount != 1 {
		t.Errorf("attended admin = %+v", resp)
	}
	if got := smtp.received(); len(got) != 1 || strings.Contains(got[0].Data, "Bcc:") || len(got[0].To) != 2 {
		t.Fatalf("SMTP delivery = %+v; a delivered message carries no Bcc header but the audit copy is in the envelope", got)
	}

	out, err = send(context.Background(), sendArgs("operator@example.com"))
	if err != nil {
		t.Fatalf("unattended admin send: %v", err)
	}
	resp = decodeSend(t, out)
	if resp.Disposition != DispositionDrafted || resp.Decision.Route != RouteUnattendedFloor || resp.Decision.Attended || resp.DraftsFolder != "Drafts" || resp.Note == "" {
		t.Errorf("unattended admin = %+v", resp)
	}

	out, err = send(attendedCtx(), sendArgs("alice@example.com"))
	if err != nil {
		t.Fatalf("attended trusted send: %v", err)
	}
	resp = decodeSend(t, out)
	if resp.Disposition != DispositionDrafted || resp.Decision.Route != RouteTrustZone || resp.Decision.Gating != GatingConfirmation {
		t.Errorf("attended trusted = %+v", resp)
	}
	if len(smtp.received()) != 1 {
		t.Fatal("drafted messages must not reach SMTP")
	}

	acct, _ := svc.ResolveAccount(context.Background(), "primary")
	listed, err := acct.Client.ListMessages(context.Background(), ListOptions{Folder: "Drafts"})
	if err != nil {
		t.Fatalf("list drafts: %v", err)
	}
	if len(listed.Envelopes) != 2 {
		t.Fatalf("drafts = %d, want 2", len(listed.Envelopes))
	}
	for _, env := range listed.Envelopes {
		if !strings.Contains(strings.Join(env.Flags, " "), `\Draft`) {
			t.Errorf("draft uid %d flags = %v, want \\Draft", env.UID, env.Flags)
		}
		msg, err := acct.Client.ReadMessage(context.Background(), ReadOptions{Folder: "Drafts", UID: env.UID, Peek: true})
		if err != nil {
			t.Fatalf("read draft: %v", err)
		}
		if !strings.Contains(string(msg.raw), "Bcc: \"Audit\" <audit@example.com>") {
			t.Errorf("a draft must carry the audit Bcc in its header so the operator's client sends it:\n%s", msg.raw)
		}
	}
	_ = imap
}

// TestFixedDeliveryModesAndRequestedDraft pins direct, drafts, and the
// draft argument.
func TestFixedDeliveryModesAndRequestedDraft(t *testing.T) {
	direct, _, smtp := policyService(t, ServiceDependencies{Contacts: identityStub()}, func(cfg *Config) {
		cfg.Accounts[0].Policy.Delivery = DeliveryDirect
	})
	out, err := direct.ToolProvider().HandleSend(context.Background(), sendArgs("alice@example.com"))
	if err != nil {
		t.Fatalf("direct: %v", err)
	}
	if resp := decodeSend(t, out); resp.Disposition != DispositionSent || resp.Decision.Route != RoutePolicyDirect || resp.Decision.Attended {
		t.Errorf("direct unattended trusted = %+v", resp)
	}
	if len(smtp.received()) != 1 {
		t.Fatal("direct delivery must reach SMTP")
	}

	args := sendArgs("operator@example.com")
	args["draft"] = true
	out, err = direct.ToolProvider().HandleSend(attendedCtx(), args)
	if err != nil {
		t.Fatalf("requested draft: %v", err)
	}
	if resp := decodeSend(t, out); resp.Disposition != DispositionDrafted || resp.Decision.Route != RouteRequestedDraft || !resp.Decision.DraftRequested {
		t.Errorf("requested draft = %+v", resp)
	}

	drafts, _, smtp2 := policyService(t, ServiceDependencies{Contacts: identityStub()}, func(cfg *Config) {
		cfg.Accounts[0].Policy.Delivery = DeliveryDrafts
		cfg.Accounts[0].DraftsFolder = "Archive"
	})
	out, err = drafts.ToolProvider().HandleSend(attendedCtx(), sendArgs("operator@example.com"))
	if err != nil {
		t.Fatalf("drafts mode: %v", err)
	}
	if resp := decodeSend(t, out); resp.Disposition != DispositionDrafted || resp.Decision.Route != RoutePolicyDrafts || resp.DraftsFolder != "Archive" {
		t.Errorf("drafts mode = %+v", resp)
	}
	if len(smtp2.received()) != 0 {
		t.Fatal("drafts mode must never reach SMTP")
	}
}

// TestAccessLevelsGateEachTool pins access: organize refuses sends and
// drafts with a sentence naming the account, read additionally refuses
// flags and moves and reads without marking seen.
func TestAccessLevelsGateEachTool(t *testing.T) {
	organize, _, _ := policyService(t, ServiceDependencies{Contacts: identityStub()}, func(cfg *Config) {
		cfg.Accounts[0].Policy.Access = AccessOrganize
	})
	_, err := organize.ToolProvider().HandleSend(attendedCtx(), sendArgs("operator@example.com"))
	refusal := refusalOf(t, err)
	if refusal.Decision.Route != RouteAccess || refusal.Decision.Access != AccessOrganize {
		t.Errorf("organize refusal = %+v", refusal.Decision)
	}
	mustContain(t, err.Error(), `"primary"`, `policy.access is "organize"`, "flagging, and moving")
	if strings.Contains(err.Error(), "no smtp configured") {
		t.Error("an account with smtp configured must not blame smtp")
	}

	readOnly, imap, _ := policyService(t, ServiceDependencies{Contacts: identityStub()}, func(cfg *Config) {
		cfg.Accounts[0].Policy.Access = AccessRead
	})
	uid := imap.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi"))
	provider := readOnly.ToolProvider()
	_, err = provider.HandleReply(attendedCtx(), map[string]any{"uid": float64(uid), "body": "x"})
	if refusalOf(t, err).Decision.Route != RouteAccess {
		t.Errorf("read-only reply = %v", err)
	}
	_, err = provider.HandleMark(context.Background(), map[string]any{"uid": float64(uid), "flag": "flagged"})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("read-only mark = %v", err)
	}
	_, err = provider.HandleMove(context.Background(), map[string]any{"uid": float64(uid), "destination": "Archive"})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("read-only move = %v", err)
	}
	out, err := provider.HandleRead(context.Background(), map[string]any{"uid": float64(uid)})
	if err != nil {
		t.Fatalf("read-only read: %v", err)
	}
	mustContain(t, out, `"marked_seen":false`, `"access_note":"`)
	listed, _ := (func() (ListResult, error) {
		acct, _ := readOnly.ResolveAccount(context.Background(), "primary")
		return acct.Client.ListMessages(context.Background(), ListOptions{Folder: "INBOX", Unseen: true})
	})()
	if len(listed.Envelopes) != 1 {
		t.Error("a read-only read must leave the message unseen")
	}
}

// objectingInspector refuses messages whose subject contains a word.
type objectingInspector struct {
	word string
	err  error
	seen []OutboundReview
}

func (i *objectingInspector) Inspect(_ context.Context, review OutboundReview) (string, error) {
	i.seen = append(i.seen, review)
	if i.err != nil {
		return "", i.err
	}
	if strings.Contains(review.Subject, i.word) {
		return "subject mentions " + i.word, nil
	}
	return "", nil
}

// TestInspectorCanOnlyRefuse pins the inspection seam: it sees the
// decision and the composed message, an objection refuses with the
// inspector route, an error fails closed, and silence changes nothing.
func TestInspectorCanOnlyRefuse(t *testing.T) {
	inspector := &objectingInspector{word: "wire transfer"}
	svc, _, smtp := policyService(t, ServiceDependencies{Contacts: identityStub(), Inspector: inspector}, nil)

	args := sendArgs("operator@example.com")
	args["subject"] = "urgent wire transfer"
	_, err := svc.ToolProvider().HandleSend(attendedCtx(), args)
	refusal := refusalOf(t, err)
	if refusal.Decision.Route != RouteInspector || !strings.Contains(refusal.Message, "subject mentions wire transfer") {
		t.Errorf("inspector refusal = %+v", refusal)
	}
	if len(inspector.seen) != 1 || inspector.seen[0].Decision.Disposition != DispositionSent || inspector.seen[0].From.Address != "thane@example.com" || len(inspector.seen[0].To) != 1 {
		t.Errorf("inspector saw %+v", inspector.seen)
	}

	out, err := svc.ToolProvider().HandleSend(attendedCtx(), sendArgs("operator@example.com"))
	if err != nil {
		t.Fatalf("silent inspector: %v", err)
	}
	if decodeSend(t, out).Disposition != DispositionSent || len(smtp.received()) != 1 {
		t.Error("a silent inspector must not change the decision")
	}

	inspector.err = errors.New("classifier offline")
	_, err = svc.ToolProvider().HandleSend(attendedCtx(), sendArgs("operator@example.com"))
	if refusal := refusalOf(t, err); refusal.Decision.Route != RouteInspector || !strings.Contains(refusal.Message, "classifier offline") {
		t.Errorf("inspector error must fail closed: %+v", refusal)
	}
	if len(smtp.received()) != 1 {
		t.Error("a failed inspection must not deliver")
	}
}

// TestDomainRulesAndOutboundInteractions pins the per-account domain
// lists on the wire and that only delivered mail records an outbound
// interaction.
func TestDomainRulesAndOutboundInteractions(t *testing.T) {
	recorder := &recordingInteractions{}
	svc, _, _ := policyService(t, ServiceDependencies{Contacts: identityStub(), Interactions: recorder}, func(cfg *Config) {
		cfg.Accounts[0].Policy.DeniedRecipientDomains = []string{"example.org"}
		cfg.Accounts[0].Policy.Delivery = DeliveryDirect
	})
	_, err := svc.ToolProvider().HandleSend(attendedCtx(), map[string]any{"to": []any{"operator@example.com", "operator@example.org"}, "subject": "hi", "body": "x"})
	refusal := refusalOf(t, err)
	if refusal.Decision.Route != RouteTrustGate || !strings.Contains(refusal.Message, "operator@example.org") || strings.Contains(refusal.Message, "operator@example.com,") {
		t.Errorf("domain refusal = %+v", refusal)
	}
	if len(recorder.seen) != 0 {
		t.Error("a refused send records no interaction")
	}

	out, err := svc.ToolProvider().HandleSend(context.Background(), sendArgs("operator@example.com"))
	if err != nil {
		t.Fatalf("direct send: %v", err)
	}
	resp := decodeSend(t, out)
	if len(recorder.seen) != 1 || recorder.seen[0].Direction != DirectionOutbound || recorder.seen[0].ContactID != "id-operator" || recorder.seen[0].MessageID != resp.MessageID {
		t.Errorf("outbound interaction = %+v", recorder.seen)
	}

	args := sendArgs("operator@example.com")
	args["draft"] = true
	if _, err := svc.ToolProvider().HandleSend(context.Background(), args); err != nil {
		t.Fatalf("draft: %v", err)
	}
	if len(recorder.seen) != 1 {
		t.Error("a draft is not an exchange and must not record an interaction")
	}
}

// TestContextBlockCarriesPolicyAndRouting pins the Email Accounts
// block's policy fields: access, delivery, the drafts folder, and the
// per-turn zone routing lists, which differ between an attended and an
// unattended turn.
func TestContextBlockCarriesPolicyAndRouting(t *testing.T) {
	svc, _, _ := policyService(t, ServiceDependencies{Contacts: identityStub()}, nil)
	provider := svc.ContextProvider()

	got, err := provider.TagContext(attendedCtx(), agentctxRequest())
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	mustContain(t, got, `"attended":true`, `"access":"send"`, `"delivery":"by_trust_zone"`, `"can_send":true`,
		`"sends_directly_to":["admin","household"]`, `"drafts_for":["trusted"]`, `"refuses":["known","unknown"]`)

	got, _ = provider.TagContext(context.Background(), agentctxRequest())
	mustContain(t, got, `"attended":false`, `"sends_directly_to":[]`, `"drafts_for":["admin","household","trusted"]`)
}
