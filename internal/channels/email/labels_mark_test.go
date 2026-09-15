package email

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// TestMarkLabelTouchesOnlyThanesMarks pins email_mark label: it writes
// the keyword and the colour on a message nobody flagged, refuses a
// colour over the operator's flag, and removing it takes away only what
// Thane recorded setting.
func TestMarkLabelTouchesOnlyThanesMarks(t *testing.T) {
	labels := map[string]LabelConfig{"contact": contactLabel, "waiting": waitingLabel}
	yellow := labelMarks{Keywords: []string{"thane-waiting"}, Color: "yellow", ColorBits: []string{colorBit1}, SetFlagged: true}
	tests := []struct {
		name       string
		initial    []string
		prep       bool
		operatorDo []string
		add        bool
		wantAction string
		present    []string
		absent     []string
		wantMarks  labelMarks
	}{
		{
			name: "applies the keyword and colour to an unflagged message", add: true, wantAction: "label_added",
			present: []string{"thane-waiting", flagFlag, colorBit1}, absent: []string{colorBit0, colorBit2, flagSeen}, wantMarks: yellow,
		},
		{
			name: "refuses a colour over the operator's flag", initial: []string{flagFlag}, add: true, wantAction: "refused",
			present: []string{flagFlag}, absent: []string{"thane-waiting", colorBit1},
		},
		{
			name: "removes what Thane set", prep: true, add: false, wantAction: "label_removed",
			absent: []string{"thane-waiting", flagFlag, colorBit1},
		},
		{
			name: "leaves the operator's own keyword and colour", initial: []string{"thane-waiting", flagFlag, colorBit1}, add: false, wantAction: "refused",
			present: []string{"thane-waiting", flagFlag, colorBit1},
		},
		{
			name: "removes Thane's keyword and keeps a flag the operator recoloured", prep: true, operatorDo: []string{colorBit1}, add: false, wantAction: "label_removed",
			present: []string{flagFlag}, absent: []string{"thane-waiting", colorBit1},
		},
		{
			name: "removes Thane's colour and keeps the operator's keyword", initial: []string{"thane-waiting"}, prep: true, add: false, wantAction: "label_removed",
			present: []string{"thane-waiting"}, absent: []string{flagFlag, colorBit1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			svc, mem, _ := labelService(t, labels, nil)
			uid := mem.append("INBOX", rawMessage("Stranger <stranger@example.com>", "thane@example.com", "Hello", "hi"), imapFlags(tt.initial...)...)
			tools := svc.ToolProvider()
			if tt.prep {
				if _, err := tools.HandleMark(ctx, markArgs(uid, "label", "waiting")); err != nil {
					t.Fatalf("prep: %v", err)
				}
			}
			if len(tt.operatorDo) > 0 {
				mem.operator().setFlags("INBOX", uid, imap.StoreFlagsDel, imapFlags(tt.operatorDo...)...)
			}

			out, err := tools.HandleMark(ctx, markArgs(uid, "label", "waiting", "add", tt.add))
			if err != nil {
				t.Fatalf("HandleMark: %v", err)
			}
			var resp labelMarkResponse
			if err := json.Unmarshal([]byte(out), &resp); err != nil {
				t.Fatalf("result is not JSON: %v\n%s", err, out)
			}
			if resp.Action != tt.wantAction || resp.Label != "waiting" || resp.Account != "primary" || resp.Folder != "INBOX" {
				t.Errorf("result = %s, want action %s", out, tt.wantAction)
			}
			refused := slices.ContainsFunc(resp.Refused, func(r labelRefusal) bool { return r.UID == uid && r.Reason != "" })
			if refused != (tt.wantAction == "refused") || slices.Contains(resp.UIDsAffected, uid) == refused {
				t.Errorf("result = %s: a message is either affected or refused with a reason", out)
			}
			checkFlags(t, flagsOf(t, svc, "INBOX", uid), tt.present, tt.absent)
			got := marksFor(t, svc, "Hello")
			if !slices.Equal(got.Keywords, tt.wantMarks.Keywords) || got.Color != tt.wantMarks.Color || !slices.Equal(got.ColorBits, tt.wantMarks.ColorBits) || got.SetFlagged != tt.wantMarks.SetFlagged {
				t.Errorf("record = %+v, want %+v", got, tt.wantMarks)
			}
		})
	}
}

// TestMarkLabelRefusals pins the calls email_mark refuses before any
// message changes, each naming what to do instead.
func TestMarkLabelRefusals(t *testing.T) {
	labels := map[string]LabelConfig{"contact": contactLabel, "waiting": waitingLabel}
	tests := []struct {
		name    string
		args    []any
		keep    bool
		wantErr []string
	}{
		{"a label Go applies", []any{"label", "contact"}, false, []string{`label "contact" is applied by Go (apply: contact_matched`, "never by email_mark", "nothing was changed"}},
		{"flag and label together", []any{"label", "waiting", "flag", "flagged"}, false, []string{"pass flag or label, not both"}},
		{"an undeclared label", []any{"label", "urgent"}, false, []string{`label "urgent" is not declared (email_mark takes one of waiting)`}},
		{"neither flag nor label", nil, false, []string{"flag (one of seen, flagged, answered) or label (one of waiting) is required"}},
		{"a folder that keeps keywords only for the session", []any{"label", "waiting"}, true, []string{`email_mark cannot apply label "waiting" in folder "INBOX" of account "primary"`, `PERMANENTFLAGS (\Seen \Flagged)`, `lack \*`, "nothing was changed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem, _ := labelService(t, labels, nil)
			uid := mem.append("INBOX", rawMessage("Stranger <stranger@example.com>", "thane@example.com", "Hello", "hi"))
			if tt.keep {
				mem.keepPermanentFlags(imap.FlagSeen, imap.FlagFlagged)
			}
			out, err := svc.ToolProvider().HandleMark(context.Background(), markArgs(uid, tt.args...))
			if err == nil {
				t.Fatalf("HandleMark = %s, want a refusal", out)
			}
			mustContain(t, err.Error(), tt.wantErr...)
			checkFlags(t, flagsOf(t, svc, "INBOX", uid), nil, []string{"thane-waiting", flagFlag, colorBit1})
		})
	}
}

// TestFlaggedClearsOnlyThanesColour pins flag "flagged" on a labelled
// message: a colour Thane wrote gives way so the flag reads as the
// operator's attention flag, and a colour the operator set, or changed,
// stays.
func TestFlaggedClearsOnlyThanesColour(t *testing.T) {
	alice, stranger := "Alice <alice@example.com>", "Stranger <stranger@example.com>"
	tests := []struct {
		name        string
		from        string
		initial     []string
		operatorAdd []string
		wantCleared bool
		present     []string
		absent      []string
	}{
		{"Thane's contact colour becomes a plain flag", alice, nil, nil, true, []string{flagFlag, "thane-contact"}, []string{colorBit0, colorBit1, colorBit2}},
		{"the operator's own colour stays", stranger, []string{flagFlag, colorBit2}, nil, false, []string{flagFlag, colorBit2}, nil},
		{"a flag the operator recoloured stays", alice, nil, []string{colorBit0}, false, []string{flagFlag, colorBit0, colorBit2}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem, _ := labelService(t, map[string]LabelConfig{"contact": contactLabel}, nil)
			poll(t, svc)
			uid := mem.append("INBOX", rawMessage(tt.from, "thane@example.com", "Hello", "hi"), imapFlags(tt.initial...)...)
			poll(t, svc)
			if len(tt.operatorAdd) > 0 {
				mem.operator().setFlags("INBOX", uid, imap.StoreFlagsAdd, imapFlags(tt.operatorAdd...)...)
			}

			out, err := svc.ToolProvider().HandleMark(context.Background(), markArgs(uid, "flag", "flagged"))
			if err != nil {
				t.Fatalf("HandleMark: %v", err)
			}
			var resp markResponse
			if err := json.Unmarshal([]byte(out), &resp); err != nil {
				t.Fatalf("result is not JSON: %v\n%s", err, out)
			}
			if got := slices.Contains(resp.ThaneColorCleared, uid); got != tt.wantCleared || resp.Note != "" {
				t.Errorf("result = %s, want thane_color_cleared to list uid %d: %v", out, uid, tt.wantCleared)
			}
			checkFlags(t, flagsOf(t, svc, "INBOX", uid), tt.present, tt.absent)
			if tt.wantCleared {
				if m := marksFor(t, svc, "Hello"); m.SetFlagged || m.Color != "" || !slices.Equal(m.Keywords, []string{"thane-contact"}) {
					t.Errorf("record = %+v: Thane must stop claiming the flag, and keep claiming its keyword", m)
				}
			}
		})
	}
}

// TestUnflagTakesThanesColourWithIt pins removing flagged: a flag Thane
// wrote for a label goes with its colour keywords and Thane stops
// claiming it, so a flag the operator sets later is theirs, plain, and
// survives removing the label; the operator's own coloured flag loses
// only \Flagged.
func TestUnflagTakesThanesColourWithIt(t *testing.T) {
	labels := map[string]LabelConfig{"contact": contactLabel, "waiting": waitingLabel}
	tests := []struct {
		name        string
		initial     []string
		label       bool
		wantCleared bool
		present     []string
		absent      []string
	}{
		{name: "a flag Thane wrote for a label", label: true, wantCleared: true, present: []string{"thane-waiting"}, absent: []string{flagFlag, colorBit1}},
		{name: "the operator's coloured flag", initial: []string{flagFlag, colorBit1}, present: []string{colorBit1}, absent: []string{flagFlag}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			svc, mem, _ := labelService(t, labels, nil)
			uid := mem.append("INBOX", rawMessage("Stranger <stranger@example.com>", "thane@example.com", "Hello", "hi"), imapFlags(tt.initial...)...)
			tools := svc.ToolProvider()
			if tt.label {
				if _, err := tools.HandleMark(ctx, markArgs(uid, "label", "waiting")); err != nil {
					t.Fatalf("label: %v", err)
				}
			}
			out, err := tools.HandleMark(ctx, markArgs(uid, "flag", "flagged", "add", false))
			if err != nil {
				t.Fatalf("unflag: %v", err)
			}
			var resp markResponse
			if err := json.Unmarshal([]byte(out), &resp); err != nil {
				t.Fatalf("result is not JSON: %v\n%s", err, out)
			}
			if got := slices.Contains(resp.ThaneColorCleared, uid); got != tt.wantCleared || resp.Action != "flag_removed" || !slices.Contains(resp.UIDsAffected, uid) {
				t.Errorf("result = %s, want flag_removed with thane_color_cleared listing uid %d: %v", out, uid, tt.wantCleared)
			}
			checkFlags(t, flagsOf(t, svc, "INBOX", uid), tt.present, tt.absent)
			if m := marksFor(t, svc, "Hello"); m.SetFlagged || m.Color != "" {
				t.Errorf("record after unflagging = %+v, want no claim on a flag", m)
			}
			if !tt.label {
				return
			}

			// The operator flags the message in their own client: a
			// plain flag, which removing the label must leave.
			mem.operator().setFlags("INBOX", uid, imap.StoreFlagsAdd, imap.FlagFlagged)
			if _, err := tools.HandleMark(ctx, markArgs(uid, "label", "waiting", "add", false)); err != nil {
				t.Fatalf("remove label: %v", err)
			}
			checkFlags(t, flagsOf(t, svc, "INBOX", uid), []string{flagFlag}, []string{"thane-waiting", colorBit1})
		})
	}
}

// TestColourLabelNeverReplacesAnotherFlag pins that a model-applied
// colour label goes only on a message without a flag, or one already
// carrying its own: it never recolours the flag Go wrote for the
// contact label, while a keyword-only label goes on a flagged message.
func TestColourLabelNeverReplacesAnotherFlag(t *testing.T) {
	labels := map[string]LabelConfig{"contact": contactLabel, "waiting": waitingLabel, "later": {Meaning: "Read later", Keyword: "thane-later"}}
	tests := []struct {
		name       string
		from       string
		first      string
		label      string
		wantAction string
		wantReason string
		present    []string
		absent     []string
	}{
		{name: "over Go's contact flag", from: "Alice <alice@example.com>", label: "waiting", wantAction: "refused", wantReason: "it already carries the blue flag Thane wrote for label contact",
			present: []string{"thane-contact", flagFlag, colorBit2}, absent: []string{"thane-waiting", colorBit1}},
		{name: "over its own flag", from: "Stranger <stranger@example.com>", first: "waiting", label: "waiting", wantAction: "label_added",
			present: []string{"thane-waiting", flagFlag, colorBit1}, absent: []string{colorBit0, colorBit2}},
		{name: "a keyword-only label over Go's flag", from: "Alice <alice@example.com>", label: "later", wantAction: "label_added",
			present: []string{"thane-later", "thane-contact", flagFlag, colorBit2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			svc, mem, _ := labelService(t, labels, nil)
			poll(t, svc)
			uid := mem.append("INBOX", rawMessage(tt.from, "thane@example.com", "Hello", "hi"))
			poll(t, svc)
			tools := svc.ToolProvider()
			if tt.first != "" {
				if _, err := tools.HandleMark(ctx, markArgs(uid, "label", tt.first)); err != nil {
					t.Fatalf("first label: %v", err)
				}
			}
			out, err := tools.HandleMark(ctx, markArgs(uid, "label", tt.label))
			if err != nil {
				t.Fatalf("HandleMark: %v", err)
			}
			var resp labelMarkResponse
			if err := json.Unmarshal([]byte(out), &resp); err != nil {
				t.Fatalf("result is not JSON: %v\n%s", err, out)
			}
			if resp.Action != tt.wantAction || (tt.wantReason != "" && (len(resp.Refused) != 1 || !strings.Contains(resp.Refused[0].Reason, tt.wantReason))) {
				t.Errorf("result = %s, want action %s with reason %q", out, tt.wantAction, tt.wantReason)
			}
			checkFlags(t, flagsOf(t, svc, "INBOX", uid), tt.present, tt.absent)
		})
	}
}

// TestMarkLabelOnAnAccountThatDoesNotCarryIt pins that email_mark refuses
// a label the account's mailbox does not name, before anything changes.
func TestMarkLabelOnAnAccountThatDoesNotCarryIt(t *testing.T) {
	for _, add := range []bool{true, false} {
		svc, mem, _ := labelService(t, map[string]LabelConfig{"waiting": waitingLabel}, func(a *AccountConfig) { a.Mailbox.Labels = nil })
		uid := mem.append("INBOX", rawMessage("Stranger <stranger@example.com>", "thane@example.com", "Hello", "hi"))
		out, err := svc.ToolProvider().HandleMark(context.Background(), markArgs(uid, "label", "waiting", "add", add))
		if err == nil {
			t.Fatalf("add %v: HandleMark = %s, want a refusal", add, out)
		}
		mustContain(t, err.Error(), `label "waiting" on account "primary": its mailbox carries no label`, "nothing was changed")
		checkFlags(t, flagsOf(t, svc, "INBOX", uid), nil, []string{"thane-waiting", flagFlag, colorBit1})
	}
}

// TestAnsweredRefusedOnDraftsAccount pins that answered is refused,
// added or removed, where delivery is drafts, and unchanged elsewhere.
func TestAnsweredRefusedOnDraftsAccount(t *testing.T) {
	tests := []struct {
		name     string
		delivery string
		add      bool
		wantErr  bool
	}{
		{"adding answered on a drafts account", DeliveryDrafts, true, true},
		{"clearing answered on a drafts account", DeliveryDrafts, false, true},
		{"adding answered on a by_trust_zone account", DeliveryByTrustZone, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem, _ := policyService(t, ServiceDependencies{Contacts: identityStub()}, func(cfg *Config) {
				cfg.Accounts[0].Policy.Delivery = tt.delivery
			})
			var initial []imap.Flag
			if !tt.add {
				initial = []imap.Flag{imap.FlagAnswered}
			}
			uid := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi"), initial...)
			out, err := svc.ToolProvider().HandleMark(context.Background(), markArgs(uid, "flag", "answered", "add", tt.add))
			answered := stringFlagIn(flagsOf(t, svc, "INBOX", uid), string(imap.FlagAnswered))
			if !tt.wantErr {
				if err != nil || !answered {
					t.Fatalf("HandleMark = %s, %v; answered = %v", out, err, answered)
				}
				return
			}
			if err == nil {
				t.Fatalf("HandleMark = %s, want a refusal", out)
			}
			mustContain(t, err.Error(), `email_mark cannot set or clear answered on account "primary": its delivery is drafts`, "nothing was changed")
			if answered != !tt.add {
				t.Errorf("answered after a refusal = %v, want it unchanged", answered)
			}
		})
	}
}
