package email

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	_ "github.com/mattn/go-sqlite3"
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

// TestSenderTag pins the contacts-zone → wake-tag mapping documented
// on senderTag. A new trust zone added to the contacts model defaults
// to "stranger" instead of silently promoting senders.
func TestSenderTag(t *testing.T) {
	cases := []struct {
		zone string
		want string
	}{
		{"admin", "owner"},
		{"household", "household"},
		{"trusted", "trusted"},
		{"known", "known"},
		{"", "stranger"},
		{"newzone", "stranger"},
	}
	for _, tc := range cases {
		if got := senderTag(tc.zone); got != tc.want {
			t.Errorf("senderTag(%q) = %q, want %q", tc.zone, got, tc.want)
		}
	}
}

// TestPollerDispatchesPerMessageEventsWithTags pins the per-message
// dispatch shape: each new IMAP message becomes one LoopEventPayload,
// the envelope's wake_loop.Tags is the deduplicated union of
// trust-zone-derived sender tags, and the envelope is delivered to the
// configured wake target (default: DefaultHandlerLoopName).
func TestPollerDispatchesPerMessageEventsWithTags(t *testing.T) {
	state := testOpstate(t)
	cfg := Config{Accounts: []AccountConfig{{
		Name:        "personal",
		IMAP:        IMAPConfig{Host: "imap.test.com", Port: 993, Username: "me"},
		DefaultFrom: "me@example.com",
	}}}
	mgr := NewManager(cfg, slog.Default())
	bus, delivered := recordingBus()
	contacts := stubContacts{zones: map[string]string{
		"boss@example.com":   "admin",
		"friend@example.com": "trusted",
	}}
	p := NewPoller(mgr, state, slog.Default(),
		WithMessageBus(bus),
		WithContactResolver(contacts),
	)

	new := []Envelope{
		{UID: 101, From: "boss@example.com", Subject: "Urgent"},
		{UID: 102, From: "friend@example.com", Subject: "Hi"},
		{UID: 103, From: "spammer@example.com", Subject: "Buy now"},
	}
	sent, err := p.dispatchAccountMessages(context.Background(), "personal", new)
	if err != nil {
		t.Fatalf("dispatchAccountMessages: %v", err)
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
	tagSet := map[string]bool{}
	for _, tag := range payload.Tags {
		tagSet[tag] = true
	}
	if !tagSet["owner"] || !tagSet["trusted"] || !tagSet["stranger"] {
		t.Errorf("payload.Tags = %v, want owner+trusted+stranger", payload.Tags)
	}

	// Per-event metadata should carry the per-sender tag too, so the
	// receiving loop can correlate each event with its sender's zone.
	tagsByUID := map[string]string{}
	for _, ev := range payload.Events {
		tagsByUID[ev.Metadata["uid"]] = ev.Metadata["tag"]
	}
	if tagsByUID["101"] != "owner" || tagsByUID["102"] != "trusted" || tagsByUID["103"] != "stranger" {
		t.Errorf("per-event tags = %v, want 101=owner 102=trusted 103=stranger", tagsByUID)
	}
}

// TestPollerNoBusAdvancesQuietly verifies the no-op-on-missing-bus
// behavior: an event observed without a bus configured doesn't error,
// just logs and continues. The next poll won't re-deliver these
// messages because the high-water mark already moved.
func TestPollerNoBusAdvancesQuietly(t *testing.T) {
	state := testOpstate(t)
	cfg := Config{Accounts: []AccountConfig{{
		Name: "readonly",
		IMAP: IMAPConfig{Host: "imap.test.com", Port: 993, Username: "me"},
	}}}
	mgr := NewManager(cfg, slog.Default())
	p := NewPoller(mgr, state, slog.Default())

	sent, err := p.dispatchAccountMessages(context.Background(), "readonly", []Envelope{
		{UID: 200, From: "x@example.com"},
	})
	if err != nil {
		t.Fatalf("dispatchAccountMessages without bus: %v", err)
	}
	if sent != 0 {
		t.Errorf("delivered = %d, want 0 when bus is nil", sent)
	}
}

func TestPollerHighWaterMark_FirstRunSeeds(t *testing.T) {
	state := testOpstate(t)

	// Verify no stored value initially.
	val, err := state.Get(pollNamespace, "test:INBOX")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "" {
		t.Errorf("expected empty initial state, got %q", val)
	}

	// Simulate what checkAccount does on first run: seed without reporting.
	if err := state.Set(pollNamespace, "test:INBOX", "500"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err = state.Get(pollNamespace, "test:INBOX")
	if err != nil {
		t.Fatalf("Get after seed: %v", err)
	}
	if val != "500" {
		t.Errorf("stored value = %q, want %q", val, "500")
	}
}

func TestPollerHighWaterMark_UpdateOnNewMessages(t *testing.T) {
	state := testOpstate(t)

	// Seed initial high-water mark.
	if err := state.Set(pollNamespace, "test:INBOX", "100"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Simulate new messages arriving (highest UID = 105).
	if err := state.Set(pollNamespace, "test:INBOX", "105"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	val, err := state.Get(pollNamespace, "test:INBOX")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "105" {
		t.Errorf("stored value = %q, want %q", val, "105")
	}
}

func TestPollerHighWaterMark_NamespaceIsolation(t *testing.T) {
	state := testOpstate(t)

	if err := state.Set(pollNamespace, "personal:INBOX", "200"); err != nil {
		t.Fatalf("Set personal: %v", err)
	}
	if err := state.Set(pollNamespace, "work:INBOX", "300"); err != nil {
		t.Fatalf("Set work: %v", err)
	}

	personal, err := state.Get(pollNamespace, "personal:INBOX")
	if err != nil {
		t.Fatalf("Get personal: %v", err)
	}
	work, err := state.Get(pollNamespace, "work:INBOX")
	if err != nil {
		t.Fatalf("Get work: %v", err)
	}

	if personal != "200" {
		t.Errorf("personal = %q, want %q", personal, "200")
	}
	if work != "300" {
		t.Errorf("work = %q, want %q", work, "300")
	}
}

func TestNewPoller(t *testing.T) {
	state := testOpstate(t)
	// NewPoller with nil manager is valid — it just won't check anything.
	// This tests that the constructor doesn't panic.
	p := NewPoller(nil, state, nil)
	if p == nil {
		t.Error("NewPoller returned nil")
	}
}

func TestAdvanceHighWaterMark_Increases(t *testing.T) {
	state := testOpstate(t)
	p := NewPoller(nil, state, nil)

	if err := state.Set(pollNamespace, "test:INBOX", "100"); err != nil {
		t.Fatal(err)
	}

	if err := p.advanceHighWaterMark("test", "test:INBOX", 100, []Envelope{
		{UID: 105},
		{UID: 103},
	}); err != nil {
		t.Fatalf("advanceHighWaterMark: %v", err)
	}

	val, _ := state.Get(pollNamespace, "test:INBOX")
	if val != "105" {
		t.Errorf("high-water mark = %q, want %q", val, "105")
	}
}

func TestAdvanceHighWaterMark_NeverDecreases(t *testing.T) {
	state := testOpstate(t)
	p := NewPoller(nil, state, nil)

	if err := state.Set(pollNamespace, "test:INBOX", "391"); err != nil {
		t.Fatal(err)
	}

	// Simulate messages with lower UIDs (e.g., after moves/deletes
	// changed what's in INBOX).
	if err := p.advanceHighWaterMark("test", "test:INBOX", 391, []Envelope{
		{UID: 286},
		{UID: 200},
	}); err != nil {
		t.Fatalf("advanceHighWaterMark: %v", err)
	}

	val, _ := state.Get(pollNamespace, "test:INBOX")
	if val != "391" {
		t.Errorf("high-water mark should not decrease: got %q, want %q", val, "391")
	}
}

func TestAdvanceHighWaterMark_EmptyMessages(t *testing.T) {
	state := testOpstate(t)
	p := NewPoller(nil, state, nil)

	if err := state.Set(pollNamespace, "test:INBOX", "100"); err != nil {
		t.Fatal(err)
	}

	// Empty message list should not change the mark.
	if err := p.advanceHighWaterMark("test", "test:INBOX", 100, nil); err != nil {
		t.Fatalf("advanceHighWaterMark: %v", err)
	}

	val, _ := state.Get(pollNamespace, "test:INBOX")
	if val != "100" {
		t.Errorf("high-water mark should not change with empty messages: got %q, want %q", val, "100")
	}
}

func TestFilterSelfSent(t *testing.T) {
	// Create a minimal manager with a configured account for testing.
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
	mgr := NewManager(cfg, slog.Default())

	p := NewPoller(mgr, nil, nil)

	messages := []Envelope{
		{UID: 105, From: "alice@example.com", Subject: "Hello"},
		{UID: 106, From: "Thane Agent <thane@example.com>", Subject: "Re: Hello"},
		{UID: 107, From: "bob@example.com", Subject: "Meeting"},
		{UID: 108, From: "thane@example.com", Subject: "Re: Meeting"},
	}

	filtered := p.filterSelfSent("work", messages)

	if len(filtered) != 2 {
		t.Fatalf("expected 2 messages after filtering, got %d", len(filtered))
	}
	if filtered[0].UID != 105 {
		t.Errorf("first message UID = %d, want 105", filtered[0].UID)
	}
	if filtered[1].UID != 107 {
		t.Errorf("second message UID = %d, want 107", filtered[1].UID)
	}
}

func TestFilterSelfSent_NoDefaultFrom(t *testing.T) {
	// When DefaultFrom is empty (no SMTP configured), all messages pass through.
	cfg := Config{
		Accounts: []AccountConfig{
			{
				Name: "readonly",
				IMAP: IMAPConfig{Host: "imap.test.com", Port: 993, Username: "user"},
				// No SMTP, no DefaultFrom.
			},
		},
	}
	mgr := NewManager(cfg, slog.Default())

	p := NewPoller(mgr, nil, nil)

	messages := []Envelope{
		{UID: 100, From: "anyone@example.com"},
	}

	filtered := p.filterSelfSent("readonly", messages)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 message (no filtering without DefaultFrom), got %d", len(filtered))
	}
}
