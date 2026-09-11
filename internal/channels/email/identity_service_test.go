package email

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// identityService builds a Service over one in-memory IMAP server and
// the SMTP fake, with the contact stub and optional seams wired, so
// the identity layer can be exercised end to end through the handlers.
func identityService(t *testing.T, deps ServiceDependencies) (*Service, *memIMAP, *smtpFake) {
	t.Helper()
	imap := newMemIMAP(t)
	smtp := newSMTPFake(t, nil)
	cfg := Config{Accounts: []AccountConfig{{
		Name:        "primary",
		IMAP:        imap.imapConfig(),
		SMTP:        smtp.config("thane", "pw"),
		DefaultFrom: "Thane <thane@example.com>",
	}}}
	deps.State = testOpstate(t)
	deps.Logger = quietSlog()
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

	out, err := svc.ToolProvider().HandleSend(context.Background(), map[string]any{
		"to": []any{"alice@example.com"}, "subject": "hi", "body": "hello",
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
	if !resp.Signed || resp.Disposition != "sent" || len(resp.Recipients) != 1 || !resp.Recipients[0].Allowed || resp.Recipients[0].ContactID != "id-alice" || resp.Recipients[0].TrustZone != "trusted" {
		t.Errorf("send response = %+v", resp)
	}

	// An unsigned send says so, and a refused send names the gate's
	// reason for every recipient.
	svc2, _, _ := identityService(t, ServiceDependencies{Contacts: identityStub()})
	out, err = svc2.ToolProvider().HandleSend(context.Background(), map[string]any{
		"to": []any{"alice@example.com"}, "subject": "hi", "body": "hello",
	})
	if err != nil {
		t.Fatalf("HandleSend unsigned: %v", err)
	}
	mustContain(t, out, `"signed":false`)

	_, err = svc2.ToolProvider().HandleSend(context.Background(), map[string]any{
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
