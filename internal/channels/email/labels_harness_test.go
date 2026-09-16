package email

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// The label tests need three things the harness lacked: a folder whose
// PERMANENTFLAGS do not keep keywords, a STORE the server refuses, and
// the operator changing a message's flags in their own client.

// Select answers a SELECT, replacing PERMANENTFLAGS when a test set them.
func (s *specialUseSession) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	data, err := s.Session.Select(mailbox, options)
	s.mem.mu.Lock()
	override, permanent := s.mem.overridePermanent, slices.Clone(s.mem.permanentFlags)
	s.mem.mu.Unlock()
	if err == nil && override {
		data.PermanentFlags = permanent
	}
	return data, err
}

// Store answers a STORE unless a test's hook refuses it first: the
// one-shot hook onNextStore set, which it clears, then the standing hook
// onStore set.
func (s *specialUseSession) Store(w *imapserver.FetchWriter, numSet imap.NumSet, flags *imap.StoreFlags, options *imap.StoreOptions) error {
	s.mem.mu.Lock()
	next, hook := s.mem.nextStoreHook, s.mem.storeHook
	s.mem.nextStoreHook = nil
	s.mem.mu.Unlock()
	if next != nil {
		if err := next(); err != nil {
			return err
		}
	}
	if hook != nil {
		if err := hook(flags); err != nil {
			return err
		}
	}
	return s.Session.Store(w, numSet, flags, options)
}

// keepPermanentFlags makes every SELECT answer PERMANENTFLAGS with
// exactly flags; none at all is a server that lists nothing.
func (m *memIMAP) keepPermanentFlags(flags ...imap.Flag) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.permanentFlags, m.overridePermanent = flags, true
}

// onStore runs hook before every STORE; an error refuses the STORE.
func (m *memIMAP) onStore(hook func(*imap.StoreFlags) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.storeHook = hook
}

// setFlags changes one message's flags the way the operator's client
// does, with a silent +FLAGS or -FLAGS.
func (o *operatorClient) setFlags(folder string, uid uint32, op imap.StoreFlagsOp, flags ...imap.Flag) {
	o.t.Helper()
	if _, err := o.c.Select(folder, nil).Wait(); err != nil {
		o.t.Fatalf("operator select %s: %v", folder, err)
	}
	set := imap.UIDSet{}
	set.AddNum(imap.UID(uid))
	if err := o.c.Store(set, &imap.StoreFlags{Op: op, Silent: true, Flags: flags}, nil).Close(); err != nil {
		o.t.Fatalf("operator store: %v", err)
	}
}

// Flag names the label tests spell out.
const (
	colorBit0 = "$MailFlagBit0"
	colorBit1 = "$MailFlagBit1"
	colorBit2 = "$MailFlagBit2"
	flagFlag  = `\Flagged`
	flagSeen  = `\Seen`
)

// contactLabel is the operator's one label; waitingLabel is one the
// model applies.
var (
	contactLabel = LabelConfig{Meaning: "The sender matches a contact record", Keyword: "thane-contact", Color: "blue", Apply: LabelApplyContactMatched}
	waitingLabel = LabelConfig{Meaning: "Waiting on someone else", Keyword: "thane-waiting", Color: "yellow"}
)

// labelService is one operator mailbox at access organize, carrying
// every declared label, over the in-memory server, with polling on, a
// recording bus, and the identity stub, where alice@example.com is a
// matched contact.
func labelService(t *testing.T, labels map[string]LabelConfig, tweak func(*AccountConfig)) (*Service, *memIMAP, func() []messages.Envelope) {
	t.Helper()
	mem := newMemIMAP(t)
	bus, delivered := recordingBus()
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
		State:      testOpstate(t),
		MessageBus: bus,
		Contacts:   identityStub(),
		Logger:     quietSlog(),
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc, mem, delivered
}

// poll runs one poll cycle and fails the test on an error.
func poll(t *testing.T, svc *Service) int {
	t.Helper()
	n, err := svc.CheckNewMessages(context.Background())
	if err != nil {
		t.Fatalf("CheckNewMessages: %v", err)
	}
	return n
}

// flagsOf reads one message's flags through an EXAMINE, which changes
// nothing.
func flagsOf(t *testing.T, svc *Service, folder string, uid uint32) []string {
	t.Helper()
	acct, err := svc.ResolveAccount(context.Background(), "primary")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	listed, err := acct.Client.ListMessages(context.Background(), ListOptions{Folder: folder, Limit: MaxListLimit})
	if err != nil {
		t.Fatalf("list %s: %v", folder, err)
	}
	for _, env := range listed.Envelopes {
		if env.UID == uid {
			return env.Flags
		}
	}
	t.Fatalf("uid %d not in %s", uid, folder)
	return nil
}

// checkFlags fails unless flags carries every present flag and none of
// the absent ones, whatever case the server spells them in.
func checkFlags(t *testing.T, flags []string, present, absent []string) {
	t.Helper()
	for _, f := range present {
		if !stringFlagIn(flags, f) {
			t.Errorf("flags %v lack %s", flags, f)
		}
	}
	for _, f := range absent {
		if stringFlagIn(flags, f) {
			t.Errorf("flags %v carry %s", flags, f)
		}
	}
}

// marksFor returns Thane's label record for the message with subject.
func marksFor(t *testing.T, svc *Service, subject string) labelMarks {
	t.Helper()
	m, err := loadLabelMarks(svc.state, "primary", messageIDFor(subject))
	if err != nil {
		t.Fatalf("load label marks: %v", err)
	}
	return m
}

// imapFlags converts flag names for an append.
func imapFlags(names ...string) []imap.Flag {
	out := make([]imap.Flag, len(names))
	for i, n := range names {
		out[i] = imap.Flag(n)
	}
	return out
}

// accountEntries decodes the Email Accounts block's account entries.
func accountEntries(t *testing.T, svc *Service) []map[string]any {
	t.Helper()
	block, err := svc.ContextProvider().TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	var payload struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(block, "### Email Accounts\n\n")), &payload); err != nil {
		t.Fatalf("block = %s: %v", block, err)
	}
	return payload.Accounts
}

// labelNames returns the names of labels, sorted.
func labelNames(labels map[string]LabelConfig) []string {
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// markArgs is an email_mark call on one INBOX message.
func markArgs(uid uint32, kv ...any) map[string]any {
	args := map[string]any{"uid": float64(uid)}
	for i := 0; i+1 < len(kv); i += 2 {
		args[fmt.Sprint(kv[i])] = kv[i+1]
	}
	return args
}
