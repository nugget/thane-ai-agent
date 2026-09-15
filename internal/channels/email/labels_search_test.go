package email

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// searchLabels is the vocabulary the search and row tests use: the
// contact label, and a red label with no keyword.
var searchLabels = map[string]LabelConfig{"contact": contactLabel, "urgent": {Meaning: "Urgent", Color: "red"}}

// TestSearchFindsLabelsAndUnflagged pins email_search's label and
// unflagged criteria.
func TestSearchFindsLabelsAndUnflagged(t *testing.T) {
	svc, mem, _ := labelService(t, searchLabels, nil)
	labelled := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Labelled", "hi"), imapFlags("thane-contact", flagFlag, colorBit2)...)
	plain := mem.append("INBOX", rawMessage("Stranger <stranger@example.com>", "thane@example.com", "Plain", "hi"))
	flaggedUID := mem.append("INBOX", rawMessage("Stranger <stranger@example.com>", "thane@example.com", "Flagged", "hi"), imap.FlagFlagged)
	tests := []struct {
		name string
		args map[string]any
		want []uint32
	}{
		{"label finds its keyword", map[string]any{"label": "contact"}, []uint32{labelled}},
		{"unflagged", map[string]any{"unflagged": true}, []uint32{plain}},
		{"flagged counts a label's flag", map[string]any{"flagged": true}, []uint32{flaggedUID, labelled}},
		{"label and unflagged combine", map[string]any{"label": "contact", "unflagged": true}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := svc.ToolProvider().HandleSearch(context.Background(), tt.args)
			if err != nil {
				t.Fatalf("HandleSearch: %v", err)
			}
			var resp listResponse
			if err := json.Unmarshal([]byte(out), &resp); err != nil {
				t.Fatalf("result is not JSON: %v", err)
			}
			var got []uint32
			for _, m := range resp.Messages {
				got = append(got, m.UID)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("uids = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSearchRefusesWhatItDoesNotDeclare pins that email_search refuses
// an argument its schema does not declare, naming it and every argument
// it takes, rather than searching without it, and refuses a label the
// account's mailbox does not carry.
func TestSearchRefusesWhatItDoesNotDeclare(t *testing.T) {
	svc, _, _ := labelService(t, searchLabels, nil)
	uncarried, _, _ := labelService(t, searchLabels, func(a *AccountConfig) { a.Mailbox.Labels = nil })
	bare, _, _ := twoAccountService(t)
	withLabel := "It takes only account, before, flagged, folder, from, in_reply_to, label, limit, message_id, query, since, subject, to, unflagged, unseen"
	withoutLabel := "It takes only account, before, flagged, folder, from, in_reply_to, limit, message_id, query, since, subject, to, unflagged, unseen"
	tests := []struct {
		name         string
		svc          *Service
		args         map[string]any
		wantErr      []string
		wantRejected []string
	}{
		{"an undeclared argument", svc, map[string]any{"has_attachment": true}, []string{`email_search does not take the argument "has_attachment"`, "nothing was searched", withLabel}, []string{"has_attachment"}},
		{"several undeclared arguments", svc, map[string]any{"sender": "a", "cc": "b", "from": "c"}, []string{`does not take the arguments "cc", "sender"`}, []string{"cc", "sender"}},
		{"label where no label is declared", bare, map[string]any{"label": "contact"}, []string{`does not take the argument "label"`, withoutLabel}, []string{"label"}},
		{"a label with no keyword", svc, map[string]any{"label": "urgent"}, []string{`label "urgent" is not one email_search can find (one of contact)`}, nil},
		{"a label the account does not carry", uncarried, map[string]any{"label": "contact"}, []string{`email_search cannot search label "contact" on account "primary": its mailbox carries no label`, "nothing was searched"}, nil},
		{"flagged and unflagged together", svc, map[string]any{"flagged": true, "unflagged": true}, []string{"flagged and unflagged cannot both be true"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := tt.svc.ToolProvider().HandleSearch(context.Background(), tt.args)
			if err == nil {
				t.Fatalf("HandleSearch = %s, want a refusal", out)
			}
			mustContain(t, err.Error(), tt.wantErr...)
			if got := toolargs.RejectedArguments(err); !slices.Equal(got, tt.wantRejected) {
				t.Errorf("rejected arguments = %v, want %v", got, tt.wantRejected)
			}
		})
	}
	for _, name := range []string{"label", "unflagged"} {
		if _, ok := svc.ToolProvider().searchProperties()[name]; !ok {
			t.Errorf("email_search's schema does not declare %s", name)
		}
	}
}

// TestListAndReadRowsShowLabels pins labels and flag_label on list and
// read rows beside the raw flags: flag_label names a label only where
// Thane's record says Thane wrote the flag, never from its colour, and
// neither key renders on an account that carries no label.
func TestListAndReadRowsShowLabels(t *testing.T) {
	svc, mem, _ := labelService(t, searchLabels, nil)
	poll(t, svc)
	labelled := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Labelled", "hi"))
	operatorBlue := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Operator blue", "hi"), imapFlags(flagFlag, colorBit2)...)
	red := mem.append("INBOX", rawMessage("Stranger <stranger@example.com>", "thane@example.com", "Red", "hi"), imap.FlagFlagged)
	plain := mem.append("INBOX", rawMessage("Stranger <stranger@example.com>", "thane@example.com", "Plain", "hi"))
	poll(t, svc)

	rows := listRows(t, svc)
	tests := []struct {
		name      string
		uid       uint32
		wantLabel []string
		wantFlag  string
	}{
		{"a message Go labelled", labelled, []string{"contact"}, "contact"},
		{"a contact's message the operator flagged blue", operatorBlue, []string{"contact"}, ""},
		{"a plain flag", red, nil, ""},
		{"an unflagged message", plain, nil, ""},
	}
	for _, tt := range tests {
		row := rows[tt.uid]
		if !slices.Equal(row.Labels, tt.wantLabel) || row.FlagLabel != tt.wantFlag {
			t.Errorf("%s: labels %v, flag_label %q; want %v, %q", tt.name, row.Labels, row.FlagLabel, tt.wantLabel, tt.wantFlag)
		}
	}
	if !stringFlagIn(rows[labelled].Flags, "thane-contact") {
		t.Errorf("raw flags %v must stay beside labels", rows[labelled].Flags)
	}

	read, err := svc.ToolProvider().HandleRead(context.Background(), map[string]any{"uid": float64(labelled), "mark_seen": false})
	if err != nil {
		t.Fatalf("HandleRead: %v", err)
	}
	var header readResponse
	if err := json.Unmarshal([]byte(strings.SplitN(read, bodySeparator, 2)[0]), &header); err != nil {
		t.Fatalf("read header is not JSON: %v", err)
	}
	if !slices.Equal(header.Labels, []string{"contact"}) || header.FlagLabel != "contact" {
		t.Errorf("read header labels %v, flag_label %q", header.Labels, header.FlagLabel)
	}

	for name, s := range map[string]func() (*Service, *memIMAP){
		"a site without labels": func() (*Service, *memIMAP) { svc, mem, _ := twoAccountService(t); return svc, mem },
		"an account that carries no label": func() (*Service, *memIMAP) {
			svc, mem, _ := labelService(t, searchLabels, func(a *AccountConfig) { a.Mailbox.Labels = nil })
			return svc, mem
		},
	} {
		svc, mem := s()
		mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Labelled", "hi"), imapFlags("thane-contact", flagFlag, colorBit2)...)
		out, err := svc.ToolProvider().HandleList(context.Background(), map[string]any{})
		if err != nil {
			t.Fatalf("%s: HandleList: %v", name, err)
		}
		if strings.Contains(out, `"labels":`) || strings.Contains(out, `"flag_label":`) {
			t.Errorf("%s renders labels or flag_label: %s", name, out)
		}
	}
}

// listRows lists INBOX of the primary account, keyed by UID.
func listRows(t *testing.T, svc *Service) map[uint32]messageSummaryView {
	t.Helper()
	out, err := svc.ToolProvider().HandleList(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("HandleList: %v", err)
	}
	var resp listResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("list is not JSON: %v", err)
	}
	rows := make(map[uint32]messageSummaryView)
	for _, m := range resp.Messages {
		rows[m.UID] = m
	}
	return rows
}

// TestEmailAccountsEntryRendersLabels pins the entry: every label the
// account carries with its meaning and how it shows, apply on a label Go
// applies, INBOX's keyword verdict once a read-write SELECT has reported
// it, and nothing on an account that carries no label or whose access is
// read.
func TestEmailAccountsEntryRendersLabels(t *testing.T) {
	wantLabels := []any{
		map[string]any{"label": "contact", "meaning": "The sender matches a contact record", "shows_as": "blue flag and keyword thane-contact", "apply": "contact_matched"},
		map[string]any{"label": "waiting", "meaning": "Waiting on someone else", "shows_as": "yellow flag and keyword thane-waiting"},
	}
	tests := []struct {
		name      string
		keep      bool
		permanent []imap.Flag
		want      string
	}{
		{"a server that keeps keywords", false, nil, "permanent"},
		{"a server that keeps them only for the session", true, []imap.Flag{imap.FlagSeen, imap.FlagFlagged}, "session_only"},
		{"a server that lists no permanent flags", true, nil, "unsupported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem, reader, own := newMemIMAP(t), newMemIMAP(t), newMemIMAP(t)
			if tt.keep {
				mem.keepPermanentFlags(tt.permanent...)
			}
			zero := 0
			cfg := Config{PollInterval: &zero, Labels: map[string]LabelConfig{"contact": contactLabel, "waiting": waitingLabel}, Accounts: []AccountConfig{
				{Name: "primary", IMAP: mem.imapConfig(), DefaultFrom: "Alice <alice@example.org>", Policy: PolicyConfig{Access: AccessOrganize}, Mailbox: MailboxConfig{Owner: "operator", Labels: []string{"contact", "waiting"}}},
				{Name: "reader", IMAP: reader.imapConfig(), Policy: PolicyConfig{Access: AccessRead}},
				{Name: "thane", IMAP: own.imapConfig(), DefaultFrom: "Thane <thane@example.com>", Policy: PolicyConfig{Access: AccessOrganize}},
			}}
			svc, err := NewService(cfg, ServiceDependencies{State: testOpstate(t), Contacts: identityStub(), Logger: quietSlog()})
			if err != nil {
				t.Fatalf("NewService: %v", err)
			}
			t.Cleanup(svc.Close)

			entries := accountEntries(t, svc)
			if !reflect.DeepEqual(entries[0]["labels"], wantLabels) {
				t.Errorf("labels = %#v, want %#v", entries[0]["labels"], wantLabels)
			}
			if v, ok := entries[0]["keywords"]; ok {
				t.Errorf("keywords = %v before any read-write SELECT; the render must not ask the server", v)
			}
			for _, i := range []int{1, 2} {
				for _, key := range []string{"labels", "keywords"} {
					if v, ok := entries[i][key]; ok {
						t.Errorf("account %v renders %s = %v, but carries no label", entries[i]["name"], key, v)
					}
				}
			}

			uid := mem.append("INBOX", rawMessage("Stranger <stranger@example.com>", "alice@example.org", "Hello", "hi"))
			_, _ = svc.ToolProvider().HandleMark(context.Background(), markArgs(uid, "label", "waiting"))
			if got := accountEntries(t, svc)[0]["keywords"]; got != tt.want {
				t.Errorf("keywords = %v, want %s", got, tt.want)
			}
		})
	}
}
