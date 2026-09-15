package email

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

// TestPollAppliesTheContactLabel pins the poll-time derived label: a
// sender the directory matches gets the keyword and, on a message nobody
// flagged, the colour; anyone else gets nothing; the operator's flag and
// colour are never written over; a folder that does not keep keywords
// gets nothing; a read-only account is never written; nothing is marked
// seen; and the wake shows the message as it arrived.
func TestPollAppliesTheContactLabel(t *testing.T) {
	alice := "Alice <alice@example.com>"
	blue := labelMarks{Keywords: []string{"thane-contact"}, Color: "blue", ColorBits: []string{colorBit2}, SetFlagged: true}
	nothing := []string{"thane-contact", flagFlag, colorBit2, flagSeen}
	tests := []struct {
		name        string
		from        string
		initial     []string
		access      string
		uncarried   bool
		override    bool
		permanent   []imap.Flag
		present     []string
		absent      []string
		wantMarks   labelMarks
		wantVerdict any
	}{
		{
			name: "a matched sender gets the keyword and the colour", from: alice,
			present: []string{"thane-contact", flagFlag, colorBit2}, absent: []string{colorBit0, colorBit1, flagSeen},
			wantMarks: blue, wantVerdict: "permanent",
		},
		{name: "an unmatched sender gets nothing", from: "Stranger <stranger@example.com>", absent: nothing},
		{name: "an ambiguous sender gets nothing", from: "Twins <twins@example.com>", absent: nothing},
		{name: "a sender the directory could not look up gets nothing", from: "broken@example.com", absent: nothing},
		{
			name: "the operator's flag and colour are kept", from: alice, initial: []string{flagFlag, colorBit0},
			present: []string{"thane-contact", flagFlag, colorBit0}, absent: []string{colorBit2, flagSeen},
			wantMarks: labelMarks{Keywords: []string{"thane-contact"}}, wantVerdict: "permanent",
		},
		{
			name: "stray colour bits on an unflagged message give way", from: alice, initial: []string{colorBit0},
			present: []string{"thane-contact", flagFlag, colorBit2}, absent: []string{colorBit0, flagSeen},
			wantMarks: blue, wantVerdict: "permanent",
		},
		{
			name: "a folder that keeps keywords only for the session gets nothing", from: alice,
			override: true, permanent: []imap.Flag{imap.FlagSeen, imap.FlagFlagged},
			absent: nothing, wantVerdict: "session_only",
		},
		{
			name: "a folder that lists no permanent flags gets nothing", from: alice,
			override: true, absent: nothing, wantVerdict: "unsupported",
		},
		{name: "a read-only account is never written", from: alice, access: AccessRead, uncarried: true, absent: nothing},
		{name: "an account that carries no label is never written", from: alice, uncarried: true, absent: nothing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem, delivered := labelService(t, map[string]LabelConfig{"contact": contactLabel}, func(a *AccountConfig) {
				if tt.access != "" {
					a.Policy.Access = tt.access
				}
				if tt.uncarried {
					a.Mailbox.Labels = nil
				}
			})
			if tt.override {
				mem.keepPermanentFlags(tt.permanent...)
			}
			poll(t, svc) // seeds the high-water mark
			uid := mem.append("INBOX", rawMessage(tt.from, "thane@example.com", "Hello", "hi"), imapFlags(tt.initial...)...)
			if got := poll(t, svc); got != 1 {
				t.Fatalf("delivered %d wake events, want 1", got)
			}

			checkFlags(t, flagsOf(t, svc, "INBOX", uid), tt.present, tt.absent)
			got := marksFor(t, svc, "Hello")
			if !slices.Equal(got.Keywords, tt.wantMarks.Keywords) || got.Color != tt.wantMarks.Color || !slices.Equal(got.ColorBits, tt.wantMarks.ColorBits) || got.SetFlagged != tt.wantMarks.SetFlagged {
				t.Errorf("record = %+v, want %+v", got, tt.wantMarks)
			}

			envs := delivered()
			payload, ok := envs[len(envs)-1].Payload.(messages.LoopNotifyPayload)
			if !ok || len(payload.Events) != 1 {
				t.Fatalf("wake payload = %#v", envs[len(envs)-1].Payload)
			}
			wakeFlags := payload.Events[0].Metadata["flags"]
			if stringFlagIn(strings.Split(wakeFlags, ","), "thane-contact") || (len(tt.initial) == 0 && wakeFlags != "") {
				t.Errorf("wake flags = %q; the wake must show the message as it arrived, without Go's label", wakeFlags)
			}
			if entry := accountEntries(t, svc)[0]; entry["keywords"] != tt.wantVerdict {
				t.Errorf("entry keywords = %v, want %v", entry["keywords"], tt.wantVerdict)
			}
		})
	}
}

// TestPollLabelsSurviveAFailedStore pins that a STORE the server refuses
// costs only that message's label: the other message is labelled, every
// wake is delivered, and the high-water mark advances.
func TestPollLabelsSurviveAFailedStore(t *testing.T) {
	svc, mem, _ := labelService(t, map[string]LabelConfig{"contact": contactLabel}, nil)
	poll(t, svc)
	first := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "First", "one"))
	second := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Second", "two"))
	var stores atomic.Int32
	mem.onStore(func(*imap.StoreFlags) error {
		if stores.Add(1) == 1 {
			return &imap.Error{Type: imap.StatusResponseTypeNo, Text: "store refused"}
		}
		return nil
	})

	if got := poll(t, svc); got != 2 {
		t.Fatalf("delivered %d wake events, want 2: a failed label must not cost a wake", got)
	}
	checkFlags(t, flagsOf(t, svc, "INBOX", first), nil, []string{"thane-contact", flagFlag, colorBit2})
	checkFlags(t, flagsOf(t, svc, "INBOX", second), []string{"thane-contact", flagFlag, colorBit2}, []string{flagSeen})
	if m := marksFor(t, svc, "First"); !m.empty() {
		t.Errorf("a message whose STORE failed has record %+v, want none", m)
	}
	raw, err := svc.state.Get(pollNamespace, "primary:INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if mark, err := parseHighWaterMark(raw); err != nil || mark.UID != second {
		t.Errorf("high-water mark = %q (%v), want uid %d", raw, err, second)
	}
}

// flakyLabelService is labelService with a bus that refuses every wake
// while fail is set, so the poller lists the same messages again.
func flakyLabelService(t *testing.T) (*Service, *memIMAP, *atomic.Bool, func() []messages.Envelope) {
	t.Helper()
	mem := newMemIMAP(t)
	var fail atomic.Bool
	var mu sync.Mutex
	var got []messages.Envelope
	bus := messages.NewBus(nil)
	bus.RegisterRoute(messages.DestinationLoop, func(_ context.Context, env messages.Envelope) (messages.DeliveryResult, error) {
		if fail.Load() {
			return messages.DeliveryResult{}, errors.New("loop unavailable")
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
		Mailbox:     MailboxConfig{Owner: "operator", Labels: []string{"contact"}},
	}
	svc, err := NewService(Config{Accounts: []AccountConfig{acct}, Labels: map[string]LabelConfig{"contact": contactLabel}}, ServiceDependencies{
		State: testOpstate(t), MessageBus: bus, Contacts: identityStub(), Logger: quietSlog(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc, mem, &fail, func() []messages.Envelope {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

// TestPollRetryNeverUndoesTheOperator pins a message the poller lists
// again because its wake could not be delivered: Go never applies the
// label a second time, so what the operator took off stays off; Thane
// stops claiming what the operator changed; and the retried wake's flags
// carry no mark Thane set, so Go's flag never reads as someone's.
func TestPollRetryNeverUndoesTheOperator(t *testing.T) {
	tests := []struct {
		name        string
		op          imap.StoreFlagsOp
		operator    []string
		present     []string
		absent      []string
		wantFlagged bool
		wakeHas     []string
	}{
		{name: "nothing changed", present: []string{"thane-contact", flagFlag, colorBit2}, wantFlagged: true},
		{name: "the operator unflagged", op: imap.StoreFlagsDel, operator: []string{flagFlag, colorBit2}, present: []string{"thane-contact"}, absent: []string{flagFlag, colorBit2}},
		{name: "the operator removed the keyword", op: imap.StoreFlagsDel, operator: []string{"thane-contact"}, present: []string{flagFlag, colorBit2}, absent: []string{"thane-contact"}, wantFlagged: true},
		{name: "the operator recoloured", op: imap.StoreFlagsAdd, operator: []string{colorBit0}, present: []string{"thane-contact", flagFlag, colorBit0, colorBit2}, wakeHas: []string{flagFlag, colorBit0, colorBit2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem, fail, delivered := flakyLabelService(t)
			poll(t, svc)
			uid := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi"))
			fail.Store(true)
			_, _ = svc.CheckNewMessages(context.Background())
			checkFlags(t, flagsOf(t, svc, "INBOX", uid), []string{"thane-contact", flagFlag, colorBit2}, nil)
			if len(tt.operator) > 0 {
				mem.operator().setFlags("INBOX", uid, tt.op, imapFlags(tt.operator...)...)
			}

			fail.Store(false)
			if _, err := svc.CheckNewMessages(context.Background()); err != nil {
				t.Fatalf("retry: %v", err)
			}
			checkFlags(t, flagsOf(t, svc, "INBOX", uid), tt.present, tt.absent)
			if m := marksFor(t, svc, "Hello"); m.SetFlagged != tt.wantFlagged || !m.hasDerived("contact") {
				t.Errorf("record = %+v, want set_flagged %v and contact derived", m, tt.wantFlagged)
			}
			envs := delivered()
			if len(envs) != 1 {
				t.Fatalf("delivered %d wakes, want the retried one", len(envs))
			}
			payload := envs[0].Payload.(messages.LoopNotifyPayload)
			wake := strings.Split(payload.Events[0].Metadata["flags"], ",")
			for _, f := range []string{"thane-contact", flagFlag, colorBit2} {
				if stringFlagIn(wake, f) != stringFlagIn(tt.wakeHas, f) {
					t.Errorf("wake flags %v: %s present = %v, want %v", wake, f, stringFlagIn(wake, f), stringFlagIn(tt.wakeHas, f))
				}
			}
		})
	}
}

// TestPollReadsEachMessageJustBeforeWritingIt pins that a flag the
// operator sets while the poller works through a batch is seen: the
// poller reads each message's flags right before writing it, not once
// for the batch.
func TestPollReadsEachMessageJustBeforeWritingIt(t *testing.T) {
	svc, mem, _ := labelService(t, map[string]LabelConfig{"contact": contactLabel}, nil)
	poll(t, svc)
	first := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "First", "one"))
	second := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Second", "two"))
	op := mem.operator()
	if _, err := op.c.Select("INBOX", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	var fired atomic.Bool
	mem.onStore(func(*imap.StoreFlags) error {
		if !fired.CompareAndSwap(false, true) {
			return nil
		}
		set := imap.UIDSet{}
		set.AddNum(imap.UID(second))
		return op.c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagFlagged}}, nil).Close()
	})
	poll(t, svc)

	checkFlags(t, flagsOf(t, svc, "INBOX", first), []string{"thane-contact", flagFlag, colorBit2}, nil)
	checkFlags(t, flagsOf(t, svc, "INBOX", second), []string{"thane-contact", flagFlag}, []string{colorBit2})
	if m := marksFor(t, svc, "Second"); m.SetFlagged {
		t.Errorf("the record claims the flag the operator set mid-batch: %+v", m)
	}
}

// TestPollLabelsOneCopyPerMessageID pins two copies of one message in
// INBOX, a direct one and a list's, under one Message-ID: Go labels the
// first, never the second, and flagging the second leaves the colour
// the operator gave it.
func TestPollLabelsOneCopyPerMessageID(t *testing.T) {
	svc, mem, _ := labelService(t, map[string]LabelConfig{"contact": contactLabel}, nil)
	poll(t, svc)
	direct := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "direct"))
	viaList := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "via the list, with its footer"))
	poll(t, svc)
	checkFlags(t, flagsOf(t, svc, "INBOX", direct), []string{"thane-contact", flagFlag, colorBit2}, nil)
	checkFlags(t, flagsOf(t, svc, "INBOX", viaList), nil, []string{"thane-contact", flagFlag, colorBit2})

	mem.operator().setFlags("INBOX", viaList, imap.StoreFlagsAdd, imap.FlagFlagged, imap.Flag(colorBit2))
	out, err := svc.ToolProvider().HandleMark(context.Background(), markArgs(viaList, "flag", "flagged"))
	if err != nil {
		t.Fatalf("HandleMark: %v", err)
	}
	checkFlags(t, flagsOf(t, svc, "INBOX", viaList), []string{flagFlag, colorBit2}, nil)
	if strings.Contains(out, "thane_color_cleared") {
		t.Errorf("flagging the list copy cleared a colour: %s", out)
	}
	if m := marksFor(t, svc, "Hello"); !m.SetFlagged {
		t.Errorf("Thane stopped claiming its flag on the direct copy: %+v", m)
	}
}

// TestPollLabelIsIdempotent pins a second pass over a labelled message,
// which a failed dispatch causes: nothing changes, and the record still
// claims what Thane set the first time.
func TestPollLabelIsIdempotent(t *testing.T) {
	svc, mem, _ := labelService(t, map[string]LabelConfig{"contact": contactLabel}, nil)
	poll(t, svc)
	uid := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi"))
	poll(t, svc)
	acct, _ := svc.ResolveAccount(t.Context(), "primary")
	envs := []Envelope{{UID: uid, From: addr("alice@example.com"), MessageID: messageIDFor("Hello")}}
	svc.poller.applyDerivedLabels(t.Context(), "primary", acct.Client, envs)

	checkFlags(t, flagsOf(t, svc, "INBOX", uid), []string{"thane-contact", flagFlag, colorBit2}, []string{colorBit0, colorBit1})
	if m := marksFor(t, svc, "Hello"); !m.SetFlagged || m.Color != "blue" || !slices.Equal(m.Keywords, []string{"thane-contact"}) {
		t.Errorf("record after a second pass = %+v", m)
	}
}
