package email

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// TestImplicitTLSHandshakeIsBoundWithoutADeadline pins the fallback
// bound on the implicit-TLS path: a peer that accepts TCP and never
// speaks TLS cannot hold the account past dialTimeout.
func TestImplicitTLSHandshakeIsBoundWithoutADeadline(t *testing.T) {
	saved := dialTimeout
	dialTimeout = 300 * time.Millisecond
	t.Cleanup(func() { dialTimeout = saved })

	host, port := hungListener(t)
	c := NewClient("primary", IMAPConfig{Host: host, Port: port, Username: "a", Password: "b", TLS: true}, nil)
	t.Cleanup(func() { _ = c.Close() })
	start := time.Now()
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("a handshake that never completes must fail")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the implicit-TLS handshake held the account for %v", elapsed)
	}
}

// TestReconnectClearsAStaleWatchdogFlag pins that a watchdog verdict
// from the previous connection does not classify the next one's
// failures: a refused dial reads as unavailable, not as a timeout.
func TestReconnectClearsAStaleWatchdogFlag(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	c := NewClient("primary", IMAPConfig{Host: "127.0.0.1", Port: port, Username: "a", Password: "b", TLS: true}, nil)
	t.Cleanup(func() { _ = c.Close() })
	c.mu.Lock()
	c.watchdogFired = true
	err = c.connectLocked(context.Background())
	c.mu.Unlock()
	if kind := FailureKindOf(err); kind != FailureUnavailable {
		t.Errorf("kind = %q, want %q; a stale watchdog flag must not reclassify a refused dial (%v)", kind, FailureUnavailable, err)
	}
}

// TestClientMoveReportsOnlyTheUIDsThatMoved pins the COPYUID reading: a
// requested UID the folder no longer holds is not reported as moved.
func TestClientMoveReportsOnlyTheUIDsThatMoved(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	uid := m.append("INBOX", rawMessage("a@example.com", "b@example.com", "one", "1"))
	stale := uid + 500

	res, err := c.MoveMessages(context.Background(), MoveOptions{UIDs: []uint32{uid, stale}, Destination: "Archive"})
	if err != nil {
		t.Fatalf("MoveMessages: %v", err)
	}
	if !res.DestUIDsKnown || fmtUIDs(res.UIDs) != fmtUIDs([]uint32{uid}) || len(res.DestUIDs) != 1 {
		t.Errorf("result = %+v; want only uid %d confirmed moved", res, uid)
	}
	if missing := missingUIDs([]uint32{uid, stale}, res.UIDs); fmtUIDs(missing) != fmtUIDs([]uint32{stale}) {
		t.Errorf("missing = %v, want the stale uid", missing)
	}
}

// TestPollerPersistsAnAdoptedLegacyMarkWhenIdle pins that a legacy
// bare-UID mark gains its validity on disk even when no new mail
// arrives to advance it.
func TestPollerPersistsAnAdoptedLegacyMarkWhenIdle(t *testing.T) {
	m := newMemIMAP(t)
	mgr := NewManager(Config{Accounts: []AccountConfig{{Name: "personal", IMAP: m.imapConfig()}}}, quietSlog())
	t.Cleanup(mgr.Close)
	state := testOpstate(t)
	bus, _ := recordingBus()
	p := NewPoller(mgr, state, quietSlog(), WithMessageBus(bus))

	uid := m.append("INBOX", rawMessage("a@example.com", "thane@example.com", "only", "x"))
	if err := state.Set(pollNamespace, "personal:INBOX", fmtUID(uid)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CheckNewMessages(context.Background()); err != nil {
		t.Fatalf("CheckNewMessages: %v", err)
	}
	raw, _ := state.Get(pollNamespace, "personal:INBOX")
	mark, err := parseHighWaterMark(raw)
	if err != nil || !strings.HasPrefix(raw, "{") || mark.UIDValidity == 0 || mark.UID != uid {
		t.Errorf("stored mark = %q (%+v, %v); want the adopted validity persisted", raw, mark, err)
	}
}

// TestBodyTruncatedDescribesTheChosenPart pins per-part truncation: an
// oversized HTML alternative does not mark a complete plain body cut,
// while an oversized HTML-only body is.
func TestBodyTruncatedDescribesTheChosenPart(t *testing.T) {
	big := strings.Repeat("h", maxBodySize+100)
	alt := "From: a@example.com\r\nContent-Type: multipart/alternative; boundary=YY\r\n\r\n" +
		"--YY\r\nContent-Type: text/plain; charset=utf-8\r\n\r\ncomplete plain\r\n" +
		"--YY\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>" + big + "</p>\r\n--YY--\r\n"
	msg := &Message{}
	if err := testClient().parseBody(msg, strings.NewReader(alt)); err != nil {
		t.Fatalf("parseBody: %v", err)
	}
	finishBody(msg)
	if msg.BodySource != "text" || msg.BodyTruncated {
		t.Errorf("plain body with an oversized HTML alternative: source %q truncated %v", msg.BodySource, msg.BodyTruncated)
	}

	htmlOnly := "From: a@example.com\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>" + big + "</p>\r\n"
	msg = &Message{}
	if err := testClient().parseBody(msg, strings.NewReader(htmlOnly)); err != nil {
		t.Fatalf("parseBody: %v", err)
	}
	finishBody(msg)
	if msg.BodySource != "html" || !msg.BodyTruncated {
		t.Errorf("oversized HTML-only body: source %q truncated %v", msg.BodySource, msg.BodyTruncated)
	}
}

// TestAttachmentsAreCappedAndCounted pins the attachment bound.
func TestAttachmentsAreCappedAndCounted(t *testing.T) {
	var b strings.Builder
	b.WriteString("From: a@example.com\r\nContent-Type: multipart/mixed; boundary=XX\r\n\r\n--XX\r\nContent-Type: text/plain\r\n\r\nbody\r\n")
	for i := 0; i < maxAttachments+10; i++ {
		fmt.Fprintf(&b, "--XX\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=\"f%d.bin\"\r\n\r\nx\r\n", i)
	}
	b.WriteString("--XX--\r\n")
	msg := &Message{}
	if err := testClient().parseBody(msg, strings.NewReader(b.String())); err != nil {
		t.Fatalf("parseBody: %v", err)
	}
	if len(msg.Attachments) != maxAttachments || msg.AttachmentsOmitted != 10 {
		t.Errorf("attachments = %d listed, %d omitted", len(msg.Attachments), msg.AttachmentsOmitted)
	}
	mustContain(t, formatMessage(msg), "and 10 more part(s) not listed")
}

// TestProseResultsAreCapped pins the AGENTS.md output caps on the list
// and read formatters, with explicit markers, and the raw-cut notice.
func TestProseResultsAreCapped(t *testing.T) {
	envs := make([]Envelope, 0, 100)
	for i := 0; i < 100; i++ {
		envs = append(envs, Envelope{UID: uint32(i + 1), From: Address{Name: strings.Repeat("N", 200), Address: "a@example.com"}, Subject: strings.Repeat("s", 4000), Date: time.Now()})
	}
	list := formatEnvelopeList(ListResult{Folder: "INBOX", TotalMatched: 100, Envelopes: envs})
	if len(list) > maxListOutput {
		t.Errorf("list output = %d bytes, cap %d", len(list), maxListOutput)
	}
	mustContain(t, list, "more message(s) not shown", "capped at 16 KB")

	to := make([]Address, 0, 3000)
	for i := 0; i < 3000; i++ {
		to = append(to, Address{Address: fmt.Sprintf("r%04d@example.com", i)})
	}
	msg := &Message{Envelope: Envelope{UID: 1, From: Address{Address: "a@example.com"}, To: to}, TextBody: strings.Repeat("b", 30*1024), BodySource: "text", RawTruncated: true}
	read := formatMessage(msg)
	if len(read) > maxReadOutput {
		t.Errorf("read output = %d bytes, cap %d", len(read), maxReadOutput)
	}
	mustContain(t, read, "[output capped at 32 KB]")
	short := formatMessage(&Message{Envelope: Envelope{UID: 2, From: Address{Address: "a@example.com"}}, TextBody: "hi", BodySource: "text", RawTruncated: true})
	mustContain(t, short, "parts past the cut were not parsed")
}

// TestSendMailHonorsCancellationWithoutDeadline pins that cancelling a
// context with no deadline ends a stalled SMTP exchange promptly and
// reads as cancelled.
func TestSendMailHonorsCancellationWithoutDeadline(t *testing.T) {
	host, port := hungListener(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	start := time.Now()
	err := sendMail(ctx, "primary", SMTPConfig{Host: host, Port: port, StartTLS: true}, "a@example.com", []string{"b@example.com"}, []byte("x"))
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("a cancelled send took %v", elapsed)
	}
	if kind := DeliveryKindOf(err); kind != DeliveryCancelled {
		t.Errorf("kind = %q, want %q (%v)", kind, DeliveryCancelled, err)
	}
}

// TestStartTLSTemporaryFailureStaysTransient pins that a 454 reply to
// STARTTLS is a retryable outage, not a permanent TLS refusal.
func TestStartTLSTemporaryFailureStaysTransient(t *testing.T) {
	fake := newSMTPFake(t, func(f *smtpFake) { f.starttlsReply = "454 4.7.0 TLS not available due to temporary reason" })
	err := sendMail(context.Background(), "primary", fake.config("alice@example.com", "secret"), "alice@example.com", []string{"bob@example.com"}, []byte("x"))
	if kind := DeliveryKindOf(err); kind != DeliveryTransient {
		t.Errorf("kind = %q, want %q (%v)", kind, DeliveryTransient, err)
	}
	if len(fake.received()) != 0 {
		t.Error("nothing may be delivered when STARTTLS did not happen")
	}
}
