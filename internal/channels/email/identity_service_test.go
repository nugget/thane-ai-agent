package email

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// identityService builds a Service over one in-memory IMAP server and
// the SMTP fake, with the contact stub and optional seams wired, so
// the identity layer can be exercised end to end through the handlers.
func identityService(t *testing.T, deps ServiceDependencies) (*Service, *memIMAP, *smtpFake) {
	t.Helper()
	return identityServiceWith(t, deps, nil)
}

// identityServiceWith is identityService with a hook to adjust the
// configuration before the service is built.
func identityServiceWith(t *testing.T, deps ServiceDependencies, tweak func(*Config)) (*Service, *memIMAP, *smtpFake) {
	t.Helper()
	imap := newMemIMAP(t)
	smtp := newSMTPFake(t, nil)
	cfg := Config{Accounts: []AccountConfig{{
		Name:        "primary",
		IMAP:        imap.imapConfig(),
		SMTP:        smtp.config("thane", "pw"),
		DefaultFrom: "Thane <thane@example.com>",
	}}}
	if tweak != nil {
		tweak(&cfg)
	}
	deps.State = testOpstate(t)
	deps.MessageBus = messages.NewBus(nil)
	if deps.Logger == nil {
		deps.Logger = quietSlog()
	}
	svc, err := NewService(cfg, deps)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc, imap, smtp
}

func identityStub() *stubContacts {
	return &stubContacts{
		zones:  map[string]string{"alice@example.com": "trusted", "operator@example.com": "admin"},
		owners: map[string]bool{"operator@example.com": true},
		ambiguous: map[string][]ContactCandidate{
			"twins@example.com": {{ID: "t1", Name: "Twin One", TrustZone: "trusted"}, {ID: "t2", Name: "Twin Two", TrustZone: "known"}},
		},
		failing: map[string]error{"broken@example.com": errors.New("database is locked")},
	}
}

// TestListAndReadTagEveryAddressWithTheDirectoryAnswer pins the model
// facing identity block: every address in a list or read result says
// how it resolved, carries the matched contact when there is one, and
// never asks the model to infer a person from a display name.
func TestListAndReadTagEveryAddressWithTheDirectoryAnswer(t *testing.T) {
	contacts := identityStub()
	svc, imap, _ := identityService(t, ServiceDependencies{Contacts: contacts})
	imap.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi",
		"Cc: Twins <twins@example.com>, broken@example.com, Someone <nobody@example.com>, Operator <operator@example.com>"))

	out, err := svc.ToolProvider().HandleList(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("HandleList: %v", err)
	}
	var resp listResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("list is not JSON: %v\n%s", err, out)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("messages = %d", len(resp.Messages))
	}
	from := resp.Messages[0].From
	if from.ContactStatus != ContactMatched || from.TrustZone != "trusted" || from.Contact == nil || from.Contact.ID != "id-alice" || from.Contact.Name != "Alice" || from.Contact.IsOwner {
		t.Errorf("from = %+v", from)
	}
	byAddr := map[string]addressView{}
	for _, cc := range resp.Messages[0].Cc {
		byAddr[cc.Address] = cc
	}
	if v := byAddr["twins@example.com"]; v.ContactStatus != ContactAmbiguous || v.TrustZone != "known" || v.Contact != nil || len(v.Candidates) != 2 {
		t.Errorf("ambiguous cc = %+v", v)
	}
	if v := byAddr["broken@example.com"]; v.ContactStatus != ContactLookupFailed || v.TrustZone != ZoneUnknown || v.Contact != nil {
		t.Errorf("lookup-failed cc = %+v", v)
	}
	if v := byAddr["nobody@example.com"]; v.ContactStatus != ContactUnmatched || v.TrustZone != ZoneUnknown || v.Contact != nil {
		t.Errorf("stranger cc = %+v", v)
	}
	if v := byAddr["operator@example.com"]; v.ContactStatus != ContactMatched || !v.Contact.IsOwner || v.TrustZone != "admin" {
		t.Errorf("operator cc = %+v", v)
	}
	// The unmatched address renders its contact as an explicit null,
	// not an omitted key, so the model sees the absence.
	mustContain(t, out, `"contact":null`, `"contact_status":"unmatched"`, `"contact_status":"lookup_failed"`, `"candidates":[`)
	if contacts.calls != 6 {
		t.Errorf("resolver calls = %d, want one per distinct address (from, to, four cc)", contacts.calls)
	}

	uid := resp.Messages[0].UID
	out, err = svc.ToolProvider().HandleRead(context.Background(), map[string]any{"uid": float64(uid)})
	if err != nil {
		t.Fatalf("HandleRead: %v", err)
	}
	headerJSON, _, _ := strings.Cut(out, bodySeparator)
	var header readResponse
	if err := json.Unmarshal([]byte(headerJSON), &header); err != nil {
		t.Fatalf("header is not JSON: %v", err)
	}
	if header.From == nil || header.From.ContactStatus != ContactMatched || header.From.Contact == nil || header.From.Contact.ID != "id-alice" {
		t.Errorf("read from = %+v", header.From)
	}
	// Nothing checked the signature, and the result says so without
	// suspicion: absent, not failed.
	if header.Authentication.Method != AuthMethodNone || header.Authentication.Status != AuthAbsent || header.Authentication.Verified {
		t.Errorf("authentication = %+v, want absent", header.Authentication)
	}
	mustContain(t, headerJSON, `"authentication":{"method":"none","status":"absent","verified":false}`)
}

// recordingAuthenticator captures what the service hands an
// Authenticator and answers with a scripted result.
type recordingAuthenticator struct {
	mu     sync.Mutex
	seen   []InboundMessage
	result Authentication
	err    error
}

func (a *recordingAuthenticator) Authenticate(_ context.Context, msg InboundMessage) (Authentication, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen = append(a.seen, msg)
	return a.result, a.err
}

// TestAuthenticatorSeesRawMessageAndItsAnswerIsNormalized pins the
// seam a future S/MIME or PGP verifier plugs into: it receives the
// complete raw message and the From it must bind to, its answer is
// rendered on the read result, and a claim it cannot make (DKIM
// verified) is demoted rather than trusted.
func TestAuthenticatorSeesRawMessageAndItsAnswerIsNormalized(t *testing.T) {
	auth := &recordingAuthenticator{result: Authentication{Method: AuthMethodPGP, Status: AuthVerified, Principal: "alice@example.com", PrincipalKind: "address", KeyFingerprint: "sha256:ab"}}
	svc, imap, _ := identityService(t, ServiceDependencies{Contacts: identityStub(), Authenticator: auth})
	uid := imap.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Signed", "hi"))

	out, err := svc.ToolProvider().HandleRead(context.Background(), map[string]any{"uid": float64(uid)})
	if err != nil {
		t.Fatalf("HandleRead: %v", err)
	}
	if len(auth.seen) != 1 || auth.seen[0].Account != "primary" || auth.seen[0].From.Address != "alice@example.com" || auth.seen[0].Truncated {
		t.Fatalf("authenticator input = %+v", auth.seen)
	}
	if !strings.Contains(string(auth.seen[0].Raw), "Subject: Signed") || !strings.Contains(string(auth.seen[0].Raw), "\r\n") {
		t.Errorf("authenticator must see the raw RFC 5322 message with CRLF, got %q", auth.seen[0].Raw)
	}
	mustContain(t, out, `"method":"pgp"`, `"status":"verified"`, `"verified":true`, `"principal":"alice@example.com"`, `"key_fingerprint":"sha256:ab"`)

	auth.result = Authentication{Method: AuthMethodDKIM, Status: AuthVerified, Verified: true, Principal: "example.com", PrincipalKind: "domain"}
	out, err = svc.ToolProvider().HandleRead(context.Background(), map[string]any{"uid": float64(uid)})
	if err != nil {
		t.Fatalf("HandleRead: %v", err)
	}
	mustContain(t, out, `"method":"dkim"`, `"status":"failed"`, `"verified":false`)

	auth.result, auth.err = Authentication{}, errors.New("keyring unreadable")
	out, err = svc.ToolProvider().HandleRead(context.Background(), map[string]any{"uid": float64(uid)})
	if err != nil {
		t.Fatalf("HandleRead: %v", err)
	}
	mustContain(t, out, `"status":"unavailable"`, `"verified":false`, "keyring unreadable")
}

// prefixSigner is a Signer that marks the message so a test can see
// the signed bytes were what went on the wire.
type prefixSigner struct{ seen []OutboundMessage }

func (s *prefixSigner) Sign(_ context.Context, msg OutboundMessage) ([]byte, error) {
	s.seen = append(s.seen, msg)
	return append([]byte("X-Test-Signed: "+msg.Account+"\r\n"), msg.Message...), nil
}

type fixedSigners struct{ signer Signer }

func (f fixedSigners) SignerFor(string) Signer { return f.signer }

// TestSendAppliesSignerAndReportsRecipients pins the outbound seam:
// the signer sees the composed message and the account, its output is
// what SMTP delivers, and the result says the message was signed and
// how each recipient was assessed.
func TestSendAppliesSignerAndReportsRecipients(t *testing.T) {
	signer := &prefixSigner{}
	svc, _, smtp := identityService(t, ServiceDependencies{Contacts: identityStub(), Signers: fixedSigners{signer: signer}})

	out, err := svc.ToolProvider().HandleSend(attendedCtx(), map[string]any{
		"to": []any{"operator@example.com"}, "subject": "hi", "body": "hello",
	})
	if err != nil {
		t.Fatalf("HandleSend: %v", err)
	}
	if len(signer.seen) != 1 || signer.seen[0].Account != "primary" || signer.seen[0].From.Address != "thane@example.com" {
		t.Fatalf("signer input = %+v", signer.seen)
	}
	got := smtp.received()
	if len(got) != 1 || !strings.HasPrefix(got[0].Data, "X-Test-Signed: primary") {
		t.Fatalf("SMTP must deliver the signed bytes, got %+v", got)
	}
	var resp sendResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("send result is not JSON: %v\n%s", err, out)
	}
	if !resp.Signed || resp.Disposition != DispositionSent || len(resp.Decision.Recipients) != 1 || !resp.Decision.Recipients[0].Allowed || (resp.Decision.Recipients[0].Contact == nil || resp.Decision.Recipients[0].Contact.ID != "id-operator") || resp.Decision.Recipients[0].TrustZone != "admin" {
		t.Errorf("send response = %+v", resp)
	}

	// An unsigned send says so, and a refused send names the gate's
	// reason for every recipient.
	svc2, _, _ := identityService(t, ServiceDependencies{Contacts: identityStub()})
	out, err = svc2.ToolProvider().HandleSend(attendedCtx(), map[string]any{
		"to": []any{"operator@example.com"}, "subject": "hi", "body": "hello",
	})
	if err != nil {
		t.Fatalf("HandleSend unsigned: %v", err)
	}
	mustContain(t, out, `"signed":false`)

	_, err = svc2.ToolProvider().HandleSend(attendedCtx(), map[string]any{
		"to": []any{"alice@example.com", "twins@example.com", "broken@example.com"}, "subject": "hi", "body": "hello",
	})
	if err == nil {
		t.Fatal("ambiguous and lookup-failed recipients must refuse the send")
	}
	mustContain(t, err.Error(), "twins@example.com", "2 contact records", "broken@example.com", "could not be consulted")
}

// recordingInteractions captures what the poller records.
type recordingInteractions struct {
	mu   sync.Mutex
	seen []Interaction
	err  error
}

func (r *recordingInteractions) RecordEmailInteraction(_ context.Context, in Interaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, in)
	return r.err
}

// TestPollerRecordsOneInboundInteractionPerContact pins the recording
// rule: once per matched contact per delivered batch, at the newest
// message's date, inbound only; strangers and ambiguous addresses
// record nothing; a recorder failure does not fail the poll.
func TestPollerRecordsOneInboundInteractionPerContact(t *testing.T) {
	state := testOpstate(t)
	cfg := Config{Accounts: []AccountConfig{{
		Name:        "primary",
		IMAP:        IMAPConfig{Host: "imap.test.com", Port: 993, Username: "me"},
		DefaultFrom: "me@example.com",
	}}}
	mgr := NewManager(cfg, quietSlog())
	bus, _ := recordingBus()
	recorder := &recordingInteractions{}
	p := NewPoller(mgr, state, quietSlog(),
		WithMessageBus(bus),
		WithContactResolver(identityStub()),
		WithInteractionRecorder(recorder),
	)

	older := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	newer := older.Add(2 * time.Hour)
	batch := []Envelope{
		{UID: 104, From: addr("twins@example.com"), Subject: "ambiguous", Date: newer},
		{UID: 103, From: addr("nobody@example.com"), Subject: "stranger", Date: newer},
		{UID: 102, From: addr("Alice <alice@example.com>"), Subject: "second", Date: newer, MessageID: "second@example.com"},
		{UID: 101, From: addr("alice@example.com"), Subject: "first", Date: older, MessageID: "first@example.com"},
	}
	if _, err := p.dispatchAccountBatches(context.Background(), "primary", "primary:INBOX", highWaterMark{UIDValidity: 1, UID: 100}, batch); err != nil {
		t.Fatalf("dispatchAccountBatches: %v", err)
	}
	if len(recorder.seen) != 1 {
		t.Fatalf("interactions = %+v, want exactly one for alice", recorder.seen)
	}
	got := recorder.seen[0]
	if got.ContactID != "id-alice" || got.Direction != DirectionInbound || got.Account != "primary" || !got.At.Equal(newer) || got.MessageID != "second@example.com" {
		t.Errorf("interaction = %+v", got)
	}

	recorder.err = errors.New("store closed")
	if _, err := p.dispatchAccountBatches(context.Background(), "primary", "primary:INBOX", highWaterMark{UIDValidity: 1, UID: 104}, batch[2:3]); err != nil {
		t.Errorf("a recorder failure must not fail the poll: %v", err)
	}
}

// TestPollerBoundsInboundInteractionByReceipt pins the Date clamp: a
// sender-controlled future Date is recorded as the receipt time, so it
// cannot pin a contact's last interaction ahead of genuine exchanges.
func TestPollerBoundsInboundInteractionByReceipt(t *testing.T) {
	cfg := Config{Accounts: []AccountConfig{{
		Name:        "primary",
		IMAP:        IMAPConfig{Host: "imap.test.com", Port: 993, Username: "me"},
		DefaultFrom: "me@example.com",
	}}}
	bus, _ := recordingBus()
	recorder := &recordingInteractions{}
	p := NewPoller(NewManager(cfg, quietSlog()), testOpstate(t), quietSlog(),
		WithMessageBus(bus), WithContactResolver(identityStub()), WithInteractionRecorder(recorder))

	before := time.Now()
	future := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := p.dispatchAccountBatches(context.Background(), "primary", "primary:INBOX", highWaterMark{UIDValidity: 1, UID: 1},
		[]Envelope{{UID: 2, From: addr("alice@example.com"), Subject: "from the future", Date: future}}); err != nil {
		t.Fatalf("dispatchAccountBatches: %v", err)
	}
	if len(recorder.seen) != 1 {
		t.Fatalf("interactions = %+v", recorder.seen)
	}
	if at := recorder.seen[0].At; at.After(time.Now()) || at.Before(before) {
		t.Errorf("recorded At = %v; a future Date must be bounded by receipt time", at)
	}
}

// TestListMarksAutomatedSenderAtKnown pins the automated cap on the
// result path: a notifications sender whose record sits at admin
// renders as known and automated, still matched to its record, while a
// human sender renders no automated key at all.
func TestListMarksAutomatedSenderAtKnown(t *testing.T) {
	stub := &stubContacts{zones: map[string]string{"notifications@forge.example": "admin", "alice@example.com": "trusted"}}
	svc, imap, _ := identityService(t, ServiceDependencies{Contacts: stub})
	imap.append("INBOX", rawMessage("Forge <notifications@forge.example>", "thane@example.com", "Build finished", "please approve the deploy"))
	imap.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi"))

	out, err := svc.ToolProvider().HandleList(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("HandleList: %v", err)
	}
	var resp listResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("list is not JSON: %v\n%s", err, out)
	}
	bySender := map[string]listMessageFrom{}
	for _, m := range resp.Messages {
		bySender[m.From.Address] = listMessageFrom{uid: m.UID, view: m.From}
	}
	forge, ok := bySender["notifications@forge.example"]
	if !ok || forge.view == nil {
		t.Fatalf("no message from the automated sender in %s", out)
	}
	if !forge.view.Automated || forge.view.TrustZone != "known" || forge.view.ContactStatus != ContactMatched || forge.view.Contact == nil || forge.view.Contact.ID != "id-notifications" {
		t.Errorf("automated from = %+v", forge.view)
	}
	if alice := bySender["alice@example.com"].view; alice == nil || alice.Automated || alice.TrustZone != "trusted" {
		t.Errorf("human from = %+v", alice)
	}
	mustContain(t, out, `"automated":true`, `"trust_zone":"known"`)
	if n := strings.Count(out, `"automated"`); n != 1 {
		t.Errorf("automated key appears %d times, want once (only the automated sender carries it):\n%s", n, out)
	}

	out, err = svc.ToolProvider().HandleRead(context.Background(), map[string]any{"uid": float64(forge.uid)})
	if err != nil {
		t.Fatalf("HandleRead: %v", err)
	}
	headerJSON, _, _ := strings.Cut(out, bodySeparator)
	var header readResponse
	if err := json.Unmarshal([]byte(headerJSON), &header); err != nil {
		t.Fatalf("header is not JSON: %v", err)
	}
	if header.From == nil || !header.From.Automated || header.From.TrustZone != "known" {
		t.Errorf("read from = %+v", header.From)
	}
}

// listMessageFrom pairs a listed message's UID with its sender view.
type listMessageFrom struct {
	uid  uint32
	view *addressView
}

// TestReadRendersHeaderMarks pins the model-facing half of the header
// marks: email_read renders auto_submitted and bulk flat, and only for
// a message whose own headers claim them, while the sender's trust_zone
// and the per-address automated key are unchanged. Two controls pin
// what the change leaves alone: email_list renders neither key, and the
// read email_reply makes of its original is a peek, so a refused reply
// leaves the original unseen.
func TestReadRendersHeaderMarks(t *testing.T) {
	svc, mem, _ := identityService(t, ServiceDependencies{Contacts: identityStub()})
	marked := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Away", "back monday",
		"Auto-Submitted: auto-replied", "List-Id: Family <family.lists.example.com>"))
	plain := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi"))
	provider := svc.ToolProvider()
	ctx := context.Background()

	out, err := provider.HandleList(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("HandleList: %v", err)
	}
	for _, key := range []string{`"auto_submitted"`, `"bulk"`, `"header_marks"`} {
		if n := strings.Count(out, key); n != 0 {
			t.Errorf("list renders %s %d times; the list is unchanged:\n%s", key, n, out)
		}
	}

	wake := tools.WithMessageOrigin(ctx, memory.OriginWake)
	_, err = provider.HandleReply(wake, map[string]any{"uid": float64(marked), "body": "thanks"})
	if refusal := refusalOf(t, err); refusal.Decision.Route != RouteAutomaticResponse {
		t.Errorf("wake reply to the marked message = %+v", refusal.Decision)
	}
	acct, _ := svc.ResolveAccount(ctx, "primary")
	unseen, err := acct.Client.ListMessages(ctx, ListOptions{Folder: "INBOX", Unseen: true})
	if err != nil {
		t.Fatalf("list unseen: %v", err)
	}
	if len(unseen.Envelopes) != 2 {
		t.Errorf("unseen = %d, want 2: the reply path must read its original with a peek", len(unseen.Envelopes))
	}

	out, err = provider.HandleRead(ctx, map[string]any{"uid": float64(marked)})
	if err != nil {
		t.Fatalf("HandleRead marked: %v", err)
	}
	headerJSON, _, _ := strings.Cut(out, bodySeparator)
	mustContain(t, headerJSON, `"auto_submitted":"auto-replied"`, `"bulk":true`)
	var header readResponse
	if err := json.Unmarshal([]byte(headerJSON), &header); err != nil {
		t.Fatalf("header is not JSON: %v", err)
	}
	if want := (HeaderMarks{AutoSubmitted: AutoSubmittedReplied, Bulk: true}); header.HeaderMarks != want {
		t.Errorf("header marks = %+v, want %+v", header.HeaderMarks, want)
	}
	if header.From == nil || header.From.TrustZone != "trusted" || header.From.Automated {
		t.Errorf("from = %+v; the marks must not move the sender's zone or set automated", header.From)
	}
	for _, key := range []string{`"automated"`, `"header_marks"`} {
		if strings.Contains(headerJSON, key) {
			t.Errorf("read header renders %s:\n%s", key, headerJSON)
		}
	}

	out, err = provider.HandleRead(ctx, map[string]any{"uid": float64(plain)})
	if err != nil {
		t.Fatalf("HandleRead plain: %v", err)
	}
	headerJSON, _, _ = strings.Cut(out, bodySeparator)
	for _, key := range []string{`"auto_submitted"`, `"bulk"`} {
		if n := strings.Count(headerJSON, key); n != 0 {
			t.Errorf("an unmarked message renders %s %d times:\n%s", key, n, headerJSON)
		}
	}
}
