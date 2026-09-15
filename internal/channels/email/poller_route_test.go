package email

import (
	"context"
	"strings"
	"testing"
)

// TestPollerRoutesEachAccountToItsWakeLoop pins per-account routing: an
// operator mailbox wakes the owner triage pass by default, an assistant
// mailbox keeps the default handler, and an explicit wake_loop wins on
// either. Every account shares one manager, so a route that ignored the
// account would send them all to the same loop.
func TestPollerRoutesEachAccountToItsWakeLoop(t *testing.T) {
	accounts := []struct {
		name    string
		mailbox MailboxConfig
		want    string
	}{
		{"thane", MailboxConfig{}, DefaultHandlerLoopName},
		{"personal", MailboxConfig{Owner: "operator"}, OwnerTriageLoopName},
		{"family", MailboxConfig{Owner: "operator", WakeLoop: DefaultHandlerLoopName}, DefaultHandlerLoopName},
		{"shop", MailboxConfig{WakeLoop: "inbox-sorter"}, "inbox-sorter"},
	}
	cfg := Config{}
	for _, a := range accounts {
		cfg.Accounts = append(cfg.Accounts, AccountConfig{
			Name:        a.name,
			IMAP:        IMAPConfig{Host: "imap.test.com", Port: 993, Username: a.name},
			DefaultFrom: a.name + "@example.com",
			Mailbox:     a.mailbox,
		})
	}
	bus, delivered := recordingBus()
	p := NewPoller(NewManager(cfg, quietSlog()), testOpstate(t), quietSlog(), WithMessageBus(bus))

	for i, a := range accounts {
		t.Run(a.name, func(t *testing.T) {
			msgs := []Envelope{{UID: 7, From: addr("bob@example.com"), Subject: "Hello", MessageID: a.name + "-7@example.com"}}
			if _, err := p.dispatchAccountBatches(context.Background(), a.name, a.name+":INBOX", highWaterMark{UIDValidity: 1, UID: 6}, msgs); err != nil {
				t.Fatalf("dispatchAccountBatches: %v", err)
			}
			envs := delivered()
			if len(envs) != i+1 {
				t.Fatalf("envelopes = %d, want %d", len(envs), i+1)
			}
			if got := envs[i].To.Target; got != a.want {
				t.Errorf("account %s woke %q, want %q", a.name, got, a.want)
			}
		})
	}
}

// TestPollerRefusesToRouteAnUnknownAccount pins the fail-closed route:
// an account the manager does not know leaves the mark where it was
// instead of waking some default loop.
func TestPollerRefusesToRouteAnUnknownAccount(t *testing.T) {
	cfg := Config{Accounts: []AccountConfig{{Name: "thane", IMAP: IMAPConfig{Host: "imap.test.com", Port: 993, Username: "thane"}}}}
	bus, delivered := recordingBus()
	p := NewPoller(NewManager(cfg, quietSlog()), testOpstate(t), quietSlog(), WithMessageBus(bus))
	msgs := []Envelope{{UID: 7, From: addr("bob@example.com"), Subject: "Hello"}}
	if _, err := p.dispatchAccountBatches(context.Background(), "ghost", "ghost:INBOX", highWaterMark{UIDValidity: 1, UID: 6}, msgs); err == nil || !strings.Contains(err.Error(), `route new mail for account "ghost"`) {
		t.Fatalf("dispatch for an unknown account = %v, want a routing error", err)
	}
	if n := len(delivered()); n != 0 {
		t.Errorf("envelopes = %d, want none", n)
	}
}

// TestWakeMetadataCarriesFlags pins the flags key: the message's flags
// joined with commas as the server spells them, and no key at all when
// the message has none.
func TestWakeMetadataCarriesFlags(t *testing.T) {
	p := NewPoller(NewManager(Config{}, quietSlog()), nil, quietSlog())
	tests := []struct {
		name    string
		flags   []string
		want    string
		present bool
	}{
		{"two flags", []string{`\Seen`, `\Flagged`}, `\Seen,\Flagged`, true},
		{"keyword", []string{"$Forwarded"}, "$Forwarded", true},
		{"blank dropped", []string{" ", `\Answered`}, `\Answered`, true},
		{"none", nil, "", false},
		{"only blanks", []string{""}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := newIdentityLookup(context.Background(), nil, quietSlog())
			events, _ := p.buildBatchEvents("personal", []Envelope{{UID: 3, From: addr("bob@example.com"), Flags: tt.flags}}, lookup)
			got, ok := events[0].Metadata["flags"]
			if ok != tt.present || got != tt.want {
				t.Errorf("flags = %q (present %v), want %q (present %v)", got, ok, tt.want, tt.present)
			}
		})
	}
}
