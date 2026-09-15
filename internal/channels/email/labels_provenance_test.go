package email

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

// labelServiceOn is labelService over a given server and state store,
// so a test can restart Thane with another configuration, and with a bus
// that refuses every wake while fail is set, so the poller lists the
// same messages again. closeSvc closes the service once.
func labelServiceOn(t *testing.T, mem *memIMAP, state *opstate.Store, labels map[string]LabelConfig, tweak func(*AccountConfig)) (*Service, *atomic.Bool, func() []messages.Envelope, func()) {
	t.Helper()
	var fail atomic.Bool
	var mu sync.Mutex
	var got []messages.Envelope
	bus := messages.NewBus(nil)
	bus.RegisterRoute(messages.DestinationLoop, func(_ context.Context, env messages.Envelope) (messages.DeliveryResult, error) {
		if fail.Load() {
			return messages.DeliveryResult{}, errLoopUnavailable
		}
		mu.Lock()
		defer mu.Unlock()
		got = append(got, env)
		return messages.DeliveryResult{Route: "test", Status: messages.DeliveryDelivered}, nil
	})
	acct := AccountConfig{
		Name:        "primary",
		IMAP:        mem.imapConfig(),
		DefaultFrom: "Thane <thane@example.com>",
		Policy:      PolicyConfig{Access: AccessOrganize},
		Mailbox:     MailboxConfig{Owner: "operator", Labels: labelNames(labels)},
	}
	if tweak != nil {
		tweak(&acct)
	}
	svc, err := NewService(Config{Accounts: []AccountConfig{acct}, Labels: labels}, ServiceDependencies{
		State: state, MessageBus: bus, Contacts: identityStub(), Logger: quietSlog(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	closeSvc := sync.OnceFunc(svc.Close)
	t.Cleanup(closeSvc)
	return svc, &fail, func() []messages.Envelope {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}, closeSvc
}

// errLoopUnavailable is the refusal labelServiceOn's bus gives while
// fail is set.
var errLoopUnavailable = errors.New("loop unavailable")

// wakeFlagsFor returns the flags of the latest wake event for uid, or
// fails the test when no wake names it.
func wakeFlagsFor(t *testing.T, envs []messages.Envelope, uid uint32) []string {
	t.Helper()
	want := strconv.FormatUint(uint64(uid), 10)
	for i := len(envs) - 1; i >= 0; i-- {
		payload, ok := envs[i].Payload.(messages.LoopNotifyPayload)
		if !ok {
			continue
		}
		for _, ev := range payload.Events {
			if ev.Metadata["uid"] != want {
				continue
			}
			if ev.Metadata["flags"] == "" {
				return nil
			}
			return strings.Split(ev.Metadata["flags"], ",")
		}
	}
	t.Fatalf("no wake names uid %d among %d envelopes", uid, len(envs))
	return nil
}

// checkWake fails unless the wake flags carry every present flag and
// none of the absent ones.
func checkWake(t *testing.T, flags []string, present, absent []string) {
	t.Helper()
	for _, f := range present {
		if !stringFlagIn(flags, f) {
			t.Errorf("wake flags %v lack %s", flags, f)
		}
	}
	for _, f := range absent {
		if stringFlagIn(flags, f) {
			t.Errorf("wake flags %v carry %s", flags, f)
		}
	}
}

// thaneBlue is every mark the contact label writes.
var thaneBlue = []string{"thane-contact", flagFlag, colorBit2}

// TestWakeStripsThaneMarksAfterTheLabelIsGone pins that the wake's flags
// leave out Thane's recorded marks whatever the account carries now: the
// operator takes the label off the account and restarts Thane, and the
// message Thane marked is delivered again with none of Thane's marks in
// its wake, while the marks stay on the message.
func TestWakeStripsThaneMarksAfterTheLabelIsGone(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]LabelConfig
		tweak  func(*AccountConfig)
	}{
		{"the mailbox no longer names the label", map[string]LabelConfig{"contact": contactLabel}, func(a *AccountConfig) { a.Mailbox.Labels = nil }},
		{"no label is declared at all", nil, func(a *AccountConfig) { a.Mailbox.Labels = nil }},
		{"the account is now read-only", nil, func(a *AccountConfig) { a.Mailbox.Labels, a.Policy.Access = nil, AccessRead }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem, state := newMemIMAP(t), testOpstate(t)
			before, fail, _, closeBefore := labelServiceOn(t, mem, state, map[string]LabelConfig{"contact": contactLabel}, nil)
			poll(t, before)
			uid := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi"))
			fail.Store(true)
			_, _ = before.CheckNewMessages(context.Background())
			checkFlags(t, flagsOf(t, before, "INBOX", uid), thaneBlue, nil)
			closeBefore()

			after, _, delivered, _ := labelServiceOn(t, mem, state, tt.labels, tt.tweak)
			if got := poll(t, after); got != 1 {
				t.Fatalf("delivered %d wakes after the restart, want the retried one", got)
			}
			checkWake(t, wakeFlagsFor(t, delivered(), uid), nil, thaneBlue)
			checkFlags(t, flagsOf(t, after, "INBOX", uid), thaneBlue, nil)
		})
	}
}

// TestLongMessageIDRowsAgreeWithRead pins a Message-ID longer than a
// row's message_id: list, search, and read all find Thane's record under
// the whole Message-ID and name the same flag_label.
func TestLongMessageIDRowsAgreeWithRead(t *testing.T) {
	svc, mem, _ := labelService(t, map[string]LabelConfig{"contact": contactLabel}, nil)
	poll(t, svc)
	long := strings.Repeat("x", maxMessageIDOutput+100) + "@example.com"
	raw := "From: Alice <alice@example.com>\r\nTo: thane@example.com\r\nSubject: Long\r\nMessage-ID: <" + long + ">\r\n" +
		"Date: Mon, 01 Sep 2026 10:00:00 +0000\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nhi\r\n"
	uid := mem.append("INBOX", raw)
	poll(t, svc)
	checkFlags(t, flagsOf(t, svc, "INBOX", uid), thaneBlue, nil)

	ctx := context.Background()
	tools := svc.ToolProvider()
	rows := listRows(t, svc)
	if rows[uid].MessageID == long {
		t.Fatalf("the row's message_id is whole; the test needs one longer than a row shows")
	}
	if rows[uid].FlagLabel != "contact" {
		t.Errorf("list flag_label = %q, want contact", rows[uid].FlagLabel)
	}

	out, err := tools.HandleSearch(ctx, map[string]any{"from": "alice@example.com"})
	if err != nil {
		t.Fatalf("HandleSearch: %v", err)
	}
	var found listResponse
	if err := json.Unmarshal([]byte(out), &found); err != nil {
		t.Fatalf("search is not JSON: %v", err)
	}
	if len(found.Messages) != 1 || found.Messages[0].FlagLabel != "contact" {
		t.Errorf("search rows = %+v, want one with flag_label contact", found.Messages)
	}

	read, err := tools.HandleRead(ctx, map[string]any{"uid": float64(uid), "mark_seen": false})
	if err != nil {
		t.Fatalf("HandleRead: %v", err)
	}
	var header readResponse
	if err := json.Unmarshal([]byte(strings.SplitN(read, bodySeparator, 2)[0]), &header); err != nil {
		t.Fatalf("read header is not JSON: %v", err)
	}
	if header.MessageID != long || header.FlagLabel != "contact" {
		t.Errorf("read message_id length %d, flag_label %q; want the whole id and contact", len(header.MessageID), header.FlagLabel)
	}
}
