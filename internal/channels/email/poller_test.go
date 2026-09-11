package email

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

// stubContacts implements ContactResolver from a fixed address→zone map.
type stubContacts struct {
	zones map[string]string
}

func (s stubContacts) ResolveTrustZone(addr string) (string, bool, error) {
	zone, ok := s.zones[addr]
	return zone, ok, nil
}

// recordingBus is a [messages.Bus] wired with a stub loop destination
// that records every delivered envelope so tests can assert dispatch
// shape without a live registry.
func recordingBus() (*messages.Bus, func() []messages.Envelope) {
	bus := messages.NewBus(nil)
	var (
		mu        sync.Mutex
		delivered []messages.Envelope
	)
	bus.RegisterRoute(messages.DestinationLoop, func(_ context.Context, env messages.Envelope) (messages.DeliveryResult, error) {
		mu.Lock()
		defer mu.Unlock()
		delivered = append(delivered, env)
		return messages.DeliveryResult{Route: "test", Status: messages.DeliveryDelivered}, nil
	})
	return bus, func() []messages.Envelope {
		mu.Lock()
		defer mu.Unlock()
		cp := make([]messages.Envelope, len(delivered))
		copy(cp, delivered)
		return cp
	}
}

func testOpstate(t *testing.T) *opstate.Store {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	s, err := opstate.NewStore(db, nil)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func quietSlog() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

func addr(s string) Address {
	a, err := parseAddress(s)
	if err != nil {
		panic(err)
	}
	return a
}

func TestParseHighWaterMark(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    highWaterMark
		wantErr bool
	}{
		{"legacy bare uid", "391", highWaterMark{UID: 391}, false},
		{"json", `{"uid_validity":7,"uid":391}`, highWaterMark{UIDValidity: 7, UID: 391}, false},
		{"empty", "", highWaterMark{}, true},
		{"garbage", "not-a-number", highWaterMark{}, true},
		{"bad json", `{"uid":`, highWaterMark{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseHighWaterMark(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("parseHighWaterMark(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
	round := highWaterMark{UIDValidity: 3, UID: 9}
	back, err := parseHighWaterMark(round.encode())
	if err != nil || back != round {
		t.Errorf("encode/parse round trip = %+v, %v", back, err)
	}
}

// TestPollerDispatchesPerMessageEvents pins the per-message dispatch
// shape: each new IMAP message becomes one LoopEventPayload whose
// metadata carries account, folder, uid, sender, and trust zone, no
// per-wake tags are stamped, and the envelope is delivered to the
// configured wake target (default: DefaultHandlerLoopName).
func TestPollerDispatchesPerMessageEvents(t *testing.T) {
	state := testOpstate(t)
	cfg := Config{Accounts: []AccountConfig{{
		Name:        "personal",
		IMAP:        IMAPConfig{Host: "imap.test.com", Port: 993, Username: "me"},
		DefaultFrom: "me@example.com",
	}}}
	mgr := NewManager(cfg, quietSlog())
	bus, delivered := recordingBus()
	contacts := stubContacts{zones: map[string]string{
		"boss@example.com":   "admin",
		"friend@example.com": "trusted",
	}}
	p := NewPoller(mgr, state, quietSlog(),
		WithMessageBus(bus),
		WithContactResolver(contacts),
	)

	// Order: newest-first, matching what fetchEnvelopes returns from IMAP.
	new := []Envelope{
		{UID: 103, From: addr("spammer@example.com"), Subject: "Buy now", MessageID: "buy@example.com"},
		{UID: 102, From: addr("Friend <Friend@Example.com>"), Subject: "Hi"},
		{UID: 101, From: addr("boss@example.com"), Subject: "Urgent"},
	}
	sent, err := p.dispatchAccountBatches(context.Background(), "personal", "personal:INBOX", highWaterMark{UIDValidity: 1, UID: 100}, new)
	if err != nil {
		t.Fatalf("dispatchAccountBatches: %v", err)
	}
	if sent != 3 {
		t.Fatalf("delivered events = %d, want 3", sent)
	}

	envs := delivered()
	if len(envs) != 1 {
		t.Fatalf("envelope count = %d, want 1 (one per account-poll cycle)", len(envs))
	}
	got := envs[0]
	if got.To.Target != DefaultHandlerLoopName {
		t.Errorf("envelope target = %q, want %q", got.To.Target, DefaultHandlerLoopName)
	}
	payload, ok := got.Payload.(messages.LoopNotifyPayload)
	if !ok {
		t.Fatalf("payload type = %T, want LoopNotifyPayload", got.Payload)
	}
	if len(payload.Events) != 3 {
		t.Fatalf("event count in envelope = %d, want 3", len(payload.Events))
	}
	if len(payload.Tags) != 0 {
		t.Errorf("the poller must stamp no per-wake tags (identity rides in metadata), got %v", payload.Tags)
	}

	byUID := map[string]messages.LoopEventPayload{}
	for _, ev := range payload.Events {
		byUID[ev.Metadata["uid"]] = ev
	}
	if byUID["101"].Metadata["trust_zone"] != "admin" || byUID["102"].Metadata["trust_zone"] != "trusted" || byUID["103"].Metadata["trust_zone"] != "unknown" {
		t.Errorf("per-event trust zones wrong: %v", byUID)
	}
	if _, present := byUID["101"].Metadata["tag"]; present {
		t.Error("the tag metadata key is retired")
	}
	if byUID["102"].Metadata["from_name"] != "Friend" {
		t.Errorf("from_name = %q", byUID["102"].Metadata["from_name"])
	}
	if _, present := byUID["101"].Metadata["from_name"]; present {
		t.Error("from_name must be omitted when the header carries no display name")
	}
	// The lookup key is the lowercase bare address even when the
	// header spelled it with a name and mixed case.
	if byUID["102"].Metadata["from_address"] != "friend@example.com" {
		t.Errorf("from_address = %q", byUID["102"].Metadata["from_address"])
	}
	if byUID["102"].Metadata["account"] != "personal" || byUID["102"].Metadata["folder"] != "INBOX" {
		t.Errorf("account/folder metadata = %v", byUID["102"].Metadata)
	}
	if byUID["103"].Metadata["message_id"] != "buy@example.com" {
		t.Errorf("message_id metadata = %q", byUID["103"].Metadata["message_id"])
	}
	if _, present := byUID["102"].Metadata["message_id"]; present {
		t.Error("message_id must be omitted when the message has none")
	}
	if strings.Contains(byUID["101"].Summary, "personal/INBOX") {
		t.Errorf("Summary must not spell account and folder as a path: %q", byUID["101"].Summary)
	}
	mustContain(t, byUID["101"].Summary, "account personal", "folder INBOX")

	hwm, _ := state.Get(pollNamespace, "personal:INBOX")
	if hwm != (highWaterMark{UIDValidity: 1, UID: 103}).encode() {
		t.Errorf("high-water mark = %q, want validity 1 uid 103", hwm)
	}
}

// TestPollerNoBusAdvancesQuietly verifies the no-op-on-missing-bus
// behavior: an event observed without a bus configured doesn't error,
// just logs and continues.
func TestPollerNoBusAdvancesQuietly(t *testing.T) {
	state := testOpstate(t)
	cfg := Config{Accounts: []AccountConfig{{
		Name: "readonly",
		IMAP: IMAPConfig{Host: "imap.test.com", Port: 993, Username: "me"},
	}}}
	mgr := NewManager(cfg, quietSlog())
	p := NewPoller(mgr, state, quietSlog())

	sent, err := p.dispatchAccountBatches(context.Background(), "readonly", "readonly:INBOX", highWaterMark{}, []Envelope{
		{UID: 200, From: addr("x@example.com")},
	})
	if err != nil {
		t.Fatalf("dispatchAccountBatches without bus: %v", err)
	}
	if sent != 0 {
		t.Errorf("delivered = %d, want 0 when bus is nil", sent)
	}
}

// TestPollerBatchesAtMaxLoopEventsPerWake pins the batching contract: a
// window with more than MaxLoopEventsPerWake new messages is split into
// multiple envelopes, and the high-water mark lands on the newest UID
// after every batch succeeds.
func TestPollerBatchesAtMaxLoopEventsPerWake(t *testing.T) {
	state := testOpstate(t)
	cfg := Config{Accounts: []AccountConfig{{
		Name: "personal",
		IMAP: IMAPConfig{Host: "imap.test.com", Port: 993, Username: "me"},
	}}}
	mgr := NewManager(cfg, quietSlog())
	bus, delivered := recordingBus()
	p := NewPoller(mgr, state, quietSlog(), WithMessageBus(bus))

	total := messages.MaxLoopEventsPerWake + 2
	newMessages := make([]Envelope, total)
	for i := 0; i < total; i++ {
		newMessages[i] = Envelope{UID: uint32(1000 - i), From: addr("sender@example.com"), Subject: "msg"}
	}

	sent, err := p.dispatchAccountBatches(context.Background(), "personal", "personal:INBOX", highWaterMark{UIDValidity: 5}, newMessages)
	if err != nil {
		t.Fatalf("dispatchAccountBatches: %v", err)
	}
	if sent != total {
		t.Fatalf("delivered = %d, want %d", sent, total)
	}
	if envs := delivered(); len(envs) != 2 {
		t.Fatalf("envelope count = %d, want 2 (one for each batch)", len(envs))
	}
	hwm, _ := state.Get(pollNamespace, "personal:INBOX")
	if hwm != (highWaterMark{UIDValidity: 5, UID: 1000}).encode() {
		t.Errorf("high-water mark = %q, want validity 5 uid 1000", hwm)
	}
}

// TestPollerBatchFailurePreservesPartialProgress pins the retry safety:
// when a later batch's Send fails, the high-water mark reflects the
// last successful batch's max UID — not the pre-poll value, and not
// the never-delivered batch's UIDs.
func TestPollerBatchFailurePreservesPartialProgress(t *testing.T) {
	state := testOpstate(t)
	cfg := Config{Accounts: []AccountConfig{{
		Name: "personal",
		IMAP: IMAPConfig{Host: "imap.test.com", Port: 993, Username: "me"},
	}}}
	mgr := NewManager(cfg, quietSlog())

	bus := messages.NewBus(nil)
	sendCount := 0
	bus.RegisterRoute(messages.DestinationLoop, func(_ context.Context, env messages.Envelope) (messages.DeliveryResult, error) {
		sendCount++
		if sendCount == 1 {
			return messages.DeliveryResult{Route: "test", Status: messages.DeliveryDelivered}, nil
		}
		return messages.DeliveryResult{}, errors.New("synthetic second-batch failure")
	})

	p := NewPoller(mgr, state, quietSlog(), WithMessageBus(bus))

	total := messages.MaxLoopEventsPerWake + 2
	newMessages := make([]Envelope, total)
	for i := 0; i < total; i++ {
		newMessages[i] = Envelope{UID: uint32(1000 - i), From: addr("sender@example.com")}
	}

	delivered, err := p.dispatchAccountBatches(context.Background(), "personal", "personal:INBOX", highWaterMark{UIDValidity: 5}, newMessages)
	if err == nil {
		t.Fatal("expected error from failing second batch")
	}
	if delivered != messages.MaxLoopEventsPerWake {
		t.Fatalf("delivered = %d, want %d (first batch only)", delivered, messages.MaxLoopEventsPerWake)
	}

	// 52 messages with UIDs 1000..949 reordered oldest-first: the first
	// batch of 50 holds 949..998, so the mark lands at 998.
	hwm, _ := state.Get(pollNamespace, "personal:INBOX")
	if hwm != (highWaterMark{UIDValidity: 5, UID: 998}).encode() {
		t.Errorf("high-water mark = %q, want validity 5 uid 998", hwm)
	}
}

func TestNewPoller(t *testing.T) {
	state := testOpstate(t)
	p := NewPoller(nil, state, nil)
	if p == nil {
		t.Error("NewPoller returned nil")
	}
}

func TestFilterSelfSent(t *testing.T) {
	cfg := Config{
		Accounts: []AccountConfig{
			{
				Name:        "work",
				IMAP:        IMAPConfig{Host: "imap.test.com", Port: 993, Username: "user"},
				SMTP:        SMTPConfig{Host: "smtp.test.com", Port: 587, Username: "user", Password: "pass"},
				DefaultFrom: "Thane Agent <thane@example.com>",
			},
		},
	}
	mgr := NewManager(cfg, quietSlog())
	p := NewPoller(mgr, nil, quietSlog())

	envelopes := []Envelope{
		{UID: 105, From: addr("alice@example.com"), Subject: "Hello"},
		{UID: 106, From: addr("Thane Agent <thane@example.com>"), Subject: "Re: Hello"},
		{UID: 107, From: addr("bob@example.com"), Subject: "Meeting"},
		{UID: 108, From: addr("THANE@Example.com"), Subject: "Re: Meeting"},
	}

	filtered := p.filterSelfSent("work", envelopes)

	if len(filtered) != 2 {
		t.Fatalf("expected 2 messages after filtering, got %d", len(filtered))
	}
	if filtered[0].UID != 105 || filtered[1].UID != 107 {
		t.Errorf("filtered UIDs = %d, %d; want 105, 107", filtered[0].UID, filtered[1].UID)
	}
}

func TestFilterSelfSent_NoDefaultFrom(t *testing.T) {
	cfg := Config{
		Accounts: []AccountConfig{
			{
				Name: "readonly",
				IMAP: IMAPConfig{Host: "imap.test.com", Port: 993, Username: "user"},
			},
		},
	}
	mgr := NewManager(cfg, quietSlog())
	p := NewPoller(mgr, nil, quietSlog())

	filtered := p.filterSelfSent("readonly", []Envelope{{UID: 100, From: addr("anyone@example.com")}})
	if len(filtered) != 1 {
		t.Fatalf("expected 1 message (no filtering without DefaultFrom), got %d", len(filtered))
	}
}

// TestPollerEndToEnd drives CheckNewMessages against the in-memory IMAP
// server: the first run seeds without dispatching, new mail becomes
// events carrying account/folder/uid/message_id, self-sent mail is
// skipped but still advances the mark, a legacy bare-UID mark is
// adopted, and a UIDVALIDITY change reseeds instead of flooding.
func TestPollerEndToEnd(t *testing.T) {
	m := newMemIMAP(t)
	imapCfg := m.imapConfig()
	cfg := Config{Accounts: []AccountConfig{{
		Name:        "personal",
		IMAP:        imapCfg,
		DefaultFrom: "Thane <thane@example.com>",
	}}}
	mgr := NewManager(cfg, quietSlog())
	t.Cleanup(mgr.Close)
	state := testOpstate(t)
	bus, delivered := recordingBus()
	p := NewPoller(mgr, state, quietSlog(), WithMessageBus(bus), WithContactResolver(stubContacts{zones: map[string]string{"alice@example.com": "trusted"}}))
	ctx := context.Background()

	preexisting := m.append("INBOX", rawMessage("old@example.com", "thane@example.com", "already there", "x"))

	// First run: seed silently at UIDNEXT-1, dispatch nothing.
	n, err := p.CheckNewMessages(ctx)
	if err != nil || n != 0 {
		t.Fatalf("first run: n=%d err=%v", n, err)
	}
	if len(delivered()) != 0 {
		t.Fatal("first run must not dispatch pre-existing mail")
	}
	raw, _ := state.Get(pollNamespace, "personal:INBOX")
	mark, err := parseHighWaterMark(raw)
	if err != nil || mark.UID != preexisting || mark.UIDValidity == 0 {
		t.Fatalf("seeded mark = %+v (%q), err %v; want uid %d with validity", mark, raw, err, preexisting)
	}

	// New mail: one from a contact, one self-sent copy.
	uidAlice := m.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Lunch", "tacos"))
	uidSelf := m.append("INBOX", rawMessage("Thane <thane@example.com>", "alice@example.com", "Re: Lunch", "yes"))

	n, err = p.CheckNewMessages(ctx)
	if err != nil || n != 1 {
		t.Fatalf("second run: n=%d err=%v, want 1 event", n, err)
	}
	envs := delivered()
	if len(envs) != 1 {
		t.Fatalf("envelopes = %d, want 1", len(envs))
	}
	payload := envs[0].Payload.(messages.LoopNotifyPayload)
	if len(payload.Events) != 1 {
		t.Fatalf("events = %d, want 1 (self-sent filtered)", len(payload.Events))
	}
	ev := payload.Events[0]
	if ev.Metadata["uid"] != fmtUID(uidAlice) || ev.Metadata["account"] != "personal" || ev.Metadata["folder"] != "INBOX" {
		t.Errorf("event metadata = %v", ev.Metadata)
	}
	if ev.Metadata["message_id"] != messageIDFor("Lunch") || ev.Metadata["trust_zone"] != "trusted" || ev.Metadata["from_address"] != "alice@example.com" {
		t.Errorf("event metadata = %v", ev.Metadata)
	}
	if ev.Title != "Lunch" {
		t.Errorf("Title = %q", ev.Title)
	}
	raw, _ = state.Get(pollNamespace, "personal:INBOX")
	mark, _ = parseHighWaterMark(raw)
	if mark.UID != uidSelf {
		t.Errorf("mark after run = %+v, want uid %d (past the self-sent copy)", mark, uidSelf)
	}

	// Legacy bare-UID mark: adopted, and polling continues from it.
	if err := state.Set(pollNamespace, "personal:INBOX", fmtUID(uidAlice)); err != nil {
		t.Fatal(err)
	}
	n, err = p.CheckNewMessages(ctx)
	if err != nil {
		t.Fatalf("legacy run: %v", err)
	}
	if n != 0 {
		t.Errorf("legacy run dispatched %d, want 0 (only the self-sent copy is newer)", n)
	}
	raw, _ = state.Get(pollNamespace, "personal:INBOX")
	mark, _ = parseHighWaterMark(raw)
	if mark.UIDValidity == 0 || mark.UID != uidSelf {
		t.Errorf("legacy mark not upgraded: %+v", mark)
	}

	// UIDVALIDITY change: reseed at the new top, dispatch nothing.
	m.recreateFolder("INBOX")
	m.append("INBOX", rawMessage("bob@example.com", "thane@example.com", "after rebuild", "x"))
	before := len(delivered())
	n, err = p.CheckNewMessages(ctx)
	if err != nil || n != 0 {
		t.Fatalf("post-rebuild run: n=%d err=%v, want reseed with no events", n, err)
	}
	if len(delivered()) != before {
		t.Error("a UIDVALIDITY change must not replay the mailbox")
	}
	raw, _ = state.Get(pollNamespace, "personal:INBOX")
	reseeded, _ := parseHighWaterMark(raw)
	if reseeded.UIDValidity == mark.UIDValidity {
		t.Errorf("mark validity did not change after rebuild: %+v", reseeded)
	}

	// And polling resumes normally under the new validity.
	uidCarol := m.append("INBOX", rawMessage("carol@example.com", "thane@example.com", "fresh", "x"))
	n, err = p.CheckNewMessages(ctx)
	if err != nil || n != 1 {
		t.Fatalf("resume run: n=%d err=%v", n, err)
	}
	last := delivered()[len(delivered())-1].Payload.(messages.LoopNotifyPayload).Events[0]
	if last.Metadata["uid"] != fmtUID(uidCarol) {
		t.Errorf("resumed event uid = %q, want %d", last.Metadata["uid"], uidCarol)
	}
}

func fmtUID(uid uint32) string {
	return fmtUIDs([]uint32{uid})[1 : len(fmtUIDs([]uint32{uid}))-1]
}
