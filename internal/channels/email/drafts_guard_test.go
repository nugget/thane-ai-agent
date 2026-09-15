package email

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// draftsRoleFolder is the folder the drafts-guard tests mark \Drafts.
// Its name is one no server ships, so a test passes only when Go found
// the drafts folder by its role rather than by a name it remembered.
const draftsRoleFolder = "Pending Letters"

// failLists makes the next n LIST commands answer NO.
func (m *memIMAP) failLists(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listFailures = n
}

// moveDraftsRole takes the drafts role off the harness's standard
// drafts folder, leaving it an ordinary folder, and gives the role to a
// new folder named to, unless to is "".
func (m *memIMAP) moveDraftsRole(to string) {
	m.t.Helper()
	m.mu.Lock()
	delete(m.attrs, "Drafts")
	m.mu.Unlock()
	if to != "" {
		m.addFolder(to, imap.MailboxAttrDrafts)
	}
}

// TestDraftsProtectionResolvesByRoleAndFailsClosed pins the drafts
// protections of email_move and email_mark: the drafts folder is found
// by its role whatever it is called, one resolution per call serves
// both the source and the destination check, a LIST that fails refuses
// the call rather than fall back to a remembered name, and an account
// with no drafts folder has nothing to protect. The LIST count tells
// one resolution from two.
func TestDraftsProtectionResolvesByRoleAndFailsClosed(t *testing.T) {
	const persistent = 1000
	unresolved := []string{`cannot tell which folder is account "primary"'s drafts folder, because listing the account's folders failed`, "LIST unavailable", "so the call stops rather than guess"}
	moved := slices.Concat(unresolved, []string{"nothing was moved"})
	changed := slices.Concat(unresolved, []string{"nothing was changed"})
	dest := func(name string) map[string]any { return map[string]any{"destination": name} }
	flagged := map[string]any{"flag": "flagged"}
	byRole := map[string]any{"destination_role": "drafts"}

	tests := []struct {
		name     string
		roleTo   string // the folder holding the drafts role; "" means none does
		failures int    // how many LIST commands answer NO
		source   string
		call     func(*Tools, context.Context, map[string]any) (string, error)
		args     map[string]any
		wantErr  []string // nil means the call goes through
	}{
		{"move out of the drafts folder by role", draftsRoleFolder, 0, draftsRoleFolder, (*Tools).HandleMove, dest("Receipts"), []string{`email_move cannot act on mail in "Pending Letters": it is account "primary"'s drafts folder`, "nothing was moved"}},
		{"move into the drafts folder by role", draftsRoleFolder, 0, "INBOX", (*Tools).HandleMove, dest(draftsRoleFolder), []string{`email_move cannot file mail into "Pending Letters"`}},
		{"destination_role drafts meets the folder with the role", draftsRoleFolder, 0, "INBOX", (*Tools).HandleMove, byRole, []string{`email_move cannot file mail into "Pending Letters"`}},
		{"mark in the drafts folder by role", draftsRoleFolder, 0, draftsRoleFolder, (*Tools).HandleMark, flagged, []string{`email_mark cannot act on mail in "Pending Letters"`, "nothing was changed"}},
		{"a folder without the role is ordinary, whatever its name", draftsRoleFolder, 0, "Drafts", (*Tools).HandleMove, dest("Receipts"), nil},
		{"no drafts folder: a move goes through", "", 0, "Drafts", (*Tools).HandleMove, dest("Receipts"), nil},
		{"no drafts folder: a mark goes through", "", 0, "Drafts", (*Tools).HandleMark, flagged, nil},
		{"no drafts folder: destination_role drafts is refused", "", 0, "INBOX", (*Tools).HandleMove, byRole, []string{`email_move cannot file mail by destination_role "drafts" on account "primary": the drafts folder is never a destination`, "nothing was moved"}},
		{"failed LIST refuses a move out of the drafts folder", draftsRoleFolder, persistent, draftsRoleFolder, (*Tools).HandleMove, dest("Receipts"), moved},
		{"failed LIST refuses a move out of INBOX", draftsRoleFolder, persistent, "INBOX", (*Tools).HandleMove, dest("Receipts"), moved},
		{"failed LIST refuses a mark in the drafts folder", draftsRoleFolder, persistent, draftsRoleFolder, (*Tools).HandleMark, flagged, changed},
		{"failed LIST refuses a mark in INBOX", draftsRoleFolder, persistent, "INBOX", (*Tools).HandleMark, flagged, changed},
		// Resolving once per call is what refuses here: a second
		// resolution after the transient failure would name the drafts
		// folder only after the destination check had already passed.
		{"a transient failure is not resolved twice", draftsRoleFolder, 1, "INBOX", (*Tools).HandleMove, dest(draftsRoleFolder), moved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem := filingService(t, identityStub(), true, nil)
			mem.moveDraftsRole(tt.roleTo)
			uid := mem.append(tt.source, rawMessage("Alice <alice@example.org>", "bob@example.org", "held", "x"), imap.FlagSeen)
			args := map[string]any{"folder": tt.source, "uid": float64(uid)}
			for k, v := range tt.args {
				args[k] = v
			}

			mem.failLists(tt.failures)
			before := mem.listCount()
			out, err := tt.call(svc.ToolProvider(), attendedCtx(), args)
			if got := mem.listCount() - before; got != 1 {
				t.Errorf("LIST commands = %d, want 1: the call resolves its drafts folder once", got)
			}
			mem.failLists(0)

			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("call = %v, want it to go through", err)
				}
				if !strings.Contains(out, `"action":"moved"`) && !strings.Contains(out, `"action":"flag_added"`) {
					t.Fatalf("result = %s, want it moved or flagged", out)
				}
				return
			}
			if err == nil {
				t.Fatalf("result = %s, want a refusal", out)
			}
			mustContain(t, err.Error(), tt.wantErr...)
			acct, _ := svc.ResolveAccount(context.Background(), "primary")
			listed, err := acct.Client.ListMessages(context.Background(), ListOptions{Folder: tt.source})
			if err != nil || len(listed.Envelopes) != 1 || !slices.Equal(listed.Envelopes[0].Flags, []string{string(imap.FlagSeen)}) {
				t.Errorf("%s after a refusal = %+v, %v; the message and its flags must be unchanged", tt.source, listed.Envelopes, err)
			}
		})
	}
}
