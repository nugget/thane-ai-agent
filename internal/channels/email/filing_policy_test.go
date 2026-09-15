package email

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	platformconfig "github.com/nugget/thane-ai-agent/internal/platform/config"
)

// junkTestFolder is the folder the test server marks \Junk. Its name is
// one no server ships, so a test passes only when Go found it by role.
const junkTestFolder = "Bulk Mail"

// addFolder creates a folder on the server with the given special-use
// attributes.
func (m *memIMAP) addFolder(name string, attrs ...imap.MailboxAttr) {
	m.t.Helper()
	if err := m.user.Create(name, nil); err != nil {
		m.t.Fatalf("create %s: %v", name, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.folders = append(m.folders, name)
	if len(attrs) > 0 {
		m.attrs[name] = attrs
	}
}

// listCount returns how many LIST commands the server has answered.
func (m *memIMAP) listCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lists
}

// filingService builds the one-account service over a server that has,
// beside the harness folders, "Receipts" (no role) and, unless noJunk,
// the junk-marked folder. tweak adjusts the account.
func filingService(t *testing.T, contacts ContactResolver, noJunk bool, tweak func(*AccountConfig)) (*Service, *memIMAP) {
	t.Helper()
	svc, mem, _ := policyService(t, ServiceDependencies{Contacts: contacts}, func(cfg *Config) {
		if tweak != nil {
			tweak(&cfg.Accounts[0])
		}
	})
	mem.addFolder("Receipts")
	if !noJunk {
		mem.addFolder(junkTestFolder, imap.MailboxAttrJunk)
	}
	return svc, mem
}

// subjectsIn returns the subjects in a folder of the primary account,
// sorted.
func subjectsIn(t *testing.T, svc *Service, folder string) []string {
	t.Helper()
	acct, _ := svc.ResolveAccount(context.Background(), "primary")
	listed, err := acct.Client.ListMessages(context.Background(), ListOptions{Folder: folder, Limit: MaxListLimit})
	if err != nil {
		t.Fatalf("list %s: %v", folder, err)
	}
	subjects := make([]string, 0, len(listed.Envelopes))
	for _, env := range listed.Envelopes {
		subjects = append(subjects, env.Subject)
	}
	slices.Sort(subjects)
	return subjects
}

func decodeMove(t *testing.T, out string) moveResponse {
	t.Helper()
	var resp moveResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("move result is not JSON: %v\n%s", err, out)
	}
	return resp
}

// TestMoveIntoAllowsAndRefusesPerOwner pins mailbox.move_into in a turn
// the operator is not present for, the only turns it binds: an operator
// mailbox files only into what the list resolves to, by role or by
// name, and back to INBOX out of those; an assistant mailbox files
// anywhere unless its list says otherwise.
func TestMoveIntoAllowsAndRefusesPerOwner(t *testing.T) {
	byRole := func(role string) map[string]any { return map[string]any{"destination_role": role} }
	byName := func(name string) map[string]any { return map[string]any{"destination": name} }
	tests := []struct {
		name     string
		owner    string
		moveInto []string
		source   string
		target   map[string]any
		wantDest string
		wantErr  string
	}{
		{"operator default files spam by role", "operator", nil, "INBOX", byRole("junk"), junkTestFolder, ""},
		{"operator default files spam by name", "operator", nil, "INBOX", byName(junkTestFolder), junkTestFolder, ""},
		{"operator default refuses a role outside move_into", "operator", nil, "INBOX", byRole("archive"), "", `cannot file into "Archive"`},
		{"operator default refuses a plain folder", "operator", nil, "INBOX", byName("Receipts"), "", `cannot file into "Receipts"`},
		{"operator returns mail to INBOX out of junk", "operator", nil, junkTestFolder, byRole("inbox"), "INBOX", ""},
		{"operator returns mail to INBOX by name", "operator", nil, junkTestFolder, byName("INBOX"), "INBOX", ""},
		{"operator cannot pull mail into INBOX from outside move_into", "operator", nil, "Receipts", byRole("inbox"), "", `cannot return mail to INBOX from "Receipts"`},
		{"operator name token", "operator", []string{"role:junk", "Receipts"}, "INBOX", byName("Receipts"), "Receipts", ""},
		{"operator role token", "operator", []string{"role:archive"}, "INBOX", byRole("archive"), "Archive", ""},
		{"operator role token refuses junk it does not list", "operator", []string{"role:archive"}, "INBOX", byRole("junk"), "", `cannot file into "Bulk Mail"`},
		{"operator allows every folder", "operator", []string{"*"}, "INBOX", byName("Receipts"), "Receipts", ""},
		{"assistant default files anywhere", "", nil, "INBOX", byName("Receipts"), "Receipts", ""},
		{"assistant default files by role", "", nil, "INBOX", byRole("trash"), "Trash", ""},
		{"assistant limited refuses", "assistant", []string{"Receipts"}, "INBOX", byRole("trash"), "", `its mailbox.move_into allows only ["Receipts"]`},
		{"assistant limited allows its list", "assistant", []string{"Receipts"}, "INBOX", byName("Receipts"), "Receipts", ""},
		{"assistant limited returns to INBOX", "assistant", []string{"Receipts"}, "Receipts", byRole("inbox"), "INBOX", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem := filingService(t, identityStub(), false, func(a *AccountConfig) {
				a.Mailbox.Owner = tt.owner
				a.Mailbox.MoveInto = tt.moveInto
			})
			uid := mem.append(tt.source, rawMessage("Bob <bob@example.org>", "alice@example.org", "filed", "x"))
			args := map[string]any{"uid": float64(uid), "folder": tt.source}
			for k, v := range tt.target {
				args[k] = v
			}
			out, err := svc.ToolProvider().HandleMove(context.Background(), args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("move = %s, want a refusal", out)
				}
				mustContain(t, err.Error(), tt.wantErr, "nothing was moved")
				if got := subjectsIn(t, svc, tt.source); !slices.Equal(got, []string{"filed"}) {
					t.Errorf("%s after a refused move = %v; the message must stay", tt.source, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("HandleMove: %v", err)
			}
			resp := decodeMove(t, out)
			if resp.Action != "moved" || resp.DestinationFolder != tt.wantDest || len(resp.Moved) != 1 {
				t.Fatalf("move = %+v, want moved into %q", resp, tt.wantDest)
			}
			if got := subjectsIn(t, svc, tt.wantDest); !slices.Contains(got, "filed") {
				t.Errorf("%s = %v, want the moved message", tt.wantDest, got)
			}
		})
	}
}

// TestFilingRefusalTeachesWhatInboxIs pins the refusal text word for
// word: on an operator mailbox it says what INBOX is to the operator and
// where mail may go instead, naming a role no folder holds as a gap;
// elsewhere it names the limit. Only a turn the operator is not present
// for is refused, so every refusal says so and names their own
// conversation as the way the move can still happen.
func TestFilingRefusalTeachesWhatInboxIs(t *testing.T) {
	tests := []struct {
		name   string
		owner  string
		noJunk bool
		source string // "" is INBOX, moving into Receipts; otherwise a return to INBOX
		want   string
	}{
		{"operator mailbox", "operator", false, "", `email_move cannot file into "Receipts" on account "primary": it is the operator's own mailbox, where INBOX is their worklist and the server keeps its own filing tree, so in a turn the operator is not present for, mail may move only into ["Bulk Mail"], and back to INBOX from there. Flag it instead; if it needs filing, the operator can make the move themselves or ask for it in their own conversation, where move_into does not apply; nothing was moved`},
		{"operator mailbox without a junk folder", "operator", true, "", `email_move cannot file into "Receipts" on account "primary": it is the operator's own mailbox, where INBOX is their worklist and the server keeps its own filing tree, so in a turn the operator is not present for, mail may move only into [the junk role, which no folder here has], and back to INBOX from there. Flag it instead; if it needs filing, the operator can make the move themselves or ask for it in their own conversation, where move_into does not apply; nothing was moved`},
		{"limited assistant mailbox", "assistant", false, "", `email_move cannot file into "Receipts" on account "primary": in a turn the operator is not present for, its mailbox.move_into allows only ["Bulk Mail"], and back to INBOX from there. Choose one of those, or leave the mail and report the need: the operator can make the move themselves or ask for it in their own conversation, where move_into does not apply; nothing was moved`},
		{"a return to INBOX names the unlisted source", "operator", false, "Receipts", `email_move cannot return mail to INBOX from "Receipts" on account "primary": in a turn the operator is not present for, mail goes back to INBOX only out of a folder its mailbox.move_into lists, ["Bulk Mail"], and "Receipts" is not one of them. Leave the mail where it is: the operator can make the move themselves or ask for it in their own conversation, where move_into does not apply; nothing was moved`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem := filingService(t, identityStub(), tt.noJunk, func(a *AccountConfig) {
				a.Mailbox.Owner = tt.owner
				a.Mailbox.MoveInto = []string{"role:junk"}
			})
			args := map[string]any{"destination": "Receipts"}
			source := "INBOX"
			if tt.source != "" {
				source = tt.source
				args = map[string]any{"folder": source, "destination_role": "inbox"}
			}
			args["uid"] = float64(mem.append(source, rawMessage("Bob <bob@example.org>", "alice@example.org", "keep", "x")))
			_, err := svc.ToolProvider().HandleMove(context.Background(), args)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("refusal = %v\nwant       %s", err, tt.want)
			}
			if m := siteFolderName.FindString(err.Error()); m != "" {
				t.Errorf("refusal names the site folder %q", m)
			}
		})
	}
}

// TestDestinationRoleResolution pins the resolver's order: the
// configured key, then the role in the cached listing, then one fresh
// LIST, then a refusal that names the gap. The LIST count tells a
// cached answer from a round trip.
func TestDestinationRoleResolution(t *testing.T) {
	tests := []struct {
		name      string
		role      string
		source    string
		tweak     func(*AccountConfig)
		junk      string // "before" adds the junk folder before the cache warms, "after" after it, "" never
		warm      bool
		wantDest  string
		wantLists int
		wantErr   []string
	}{
		{"configured key beats special use", "junk", "INBOX", func(a *AccountConfig) { a.JunkFolder = "Receipts" }, "before", true, "Receipts", 0, nil},
		{"configured key needs no listing", "junk", "INBOX", func(a *AccountConfig) { a.JunkFolder = "Receipts" }, "before", false, "Receipts", 0, nil},
		{"trash key beats special use", "trash", "INBOX", func(a *AccountConfig) { a.TrashFolder = "Receipts" }, "", true, "Receipts", 0, nil},
		{"special use from the cached listing", "junk", "INBOX", nil, "before", true, junkTestFolder, 0, nil},
		{"one fresh LIST when the cache lacks the role", "junk", "INBOX", nil, "after", true, junkTestFolder, 1, nil},
		{"one LIST on a cold cache", "junk", "INBOX", nil, "before", false, junkTestFolder, 1, nil},
		{"inbox needs no key", "inbox", junkTestFolder, nil, "before", false, "INBOX", 0, nil},
		{"unresolved junk names its key", "junk", "INBOX", nil, "", true, "", 1, []string{`email_move cannot resolve destination_role "junk" on account "primary": no folder there has the junk special-use role and the account sets no junk_folder`, "until the operator configures junk_folder", "Nothing was moved"}},
		{"unresolved role without a key", "important", "INBOX", nil, "", true, "", 1, []string{"no folder there has the important special-use role; pass destination with a folder name", "Nothing was moved"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem := filingService(t, identityStub(), true, func(a *AccountConfig) {
				a.DraftsFolder = "Drafts" // keeps the drafts check off the wire
				if tt.tweak != nil {
					tt.tweak(a)
				}
			})
			if tt.junk == "before" {
				mem.addFolder(junkTestFolder, imap.MailboxAttrJunk)
			}
			if tt.warm {
				if _, err := svc.ToolProvider().HandleFolders(context.Background(), map[string]any{}); err != nil {
					t.Fatalf("HandleFolders: %v", err)
				}
			}
			if tt.junk == "after" {
				mem.addFolder(junkTestFolder, imap.MailboxAttrJunk)
			}
			uid := mem.append(tt.source, rawMessage("Bob <bob@example.org>", "alice@example.org", "resolved", "x"))
			before := mem.listCount()
			out, err := svc.ToolProvider().HandleMove(attendedCtx(), map[string]any{"uid": float64(uid), "folder": tt.source, "destination_role": tt.role})
			if got := mem.listCount() - before; got != tt.wantLists {
				t.Errorf("LIST commands = %d, want %d", got, tt.wantLists)
			}
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("move = %s, want a refusal", out)
				}
				mustContain(t, err.Error(), tt.wantErr...)
				if got := subjectsIn(t, svc, tt.source); !slices.Equal(got, []string{"resolved"}) {
					t.Errorf("%s after a refused move = %v", tt.source, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("HandleMove: %v", err)
			}
			if resp := decodeMove(t, out); resp.DestinationFolder != tt.wantDest {
				t.Errorf("destination_folder = %q, want %q", resp.DestinationFolder, tt.wantDest)
			}
		})
	}
}

// TestMoveTakesExactlyOneTarget pins destination and destination_role
// as alternatives: both or neither is refused in one sentence, an
// unknown role lists the roles, every problem is reported together, and
// nothing moves.
func TestMoveTakesExactlyOneTarget(t *testing.T) {
	tests := []struct {
		name  string
		args  map[string]any
		noUID bool
		want  []string
	}{
		{"both", map[string]any{"destination": "Receipts", "destination_role": "junk"}, false, []string{"pass destination or destination_role, not both", "nothing was moved"}},
		{"neither", map[string]any{"folder": "INBOX"}, false, []string{"destination or destination_role is required; folder is the source", "destination_role as a special-use role such as junk or trash", "nothing was moved"}},
		{"unknown role", map[string]any{"destination_role": "spam"}, false, []string{`destination_role "spam" is not a special-use role; use one of inbox, sent, trash, junk, archive, all, flagged, important`}},
		{"every problem together", map[string]any{"destination": "Receipts", "destination_role": "junk"}, true, []string{"uids is required", "not both"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem := filingService(t, identityStub(), false, nil)
			uid := mem.append("INBOX", rawMessage("Bob <bob@example.org>", "alice@example.org", "stays", "x"))
			args := map[string]any{}
			if !tt.noUID {
				args["uid"] = float64(uid)
			}
			for k, v := range tt.args {
				args[k] = v
			}
			_, err := svc.ToolProvider().HandleMove(context.Background(), args)
			if err == nil {
				t.Fatal("the move must be refused")
			}
			mustContain(t, err.Error(), tt.want...)
			if got := subjectsIn(t, svc, "INBOX"); !slices.Equal(got, []string{"stays"}) {
				t.Errorf("INBOX after a refused move = %v", got)
			}
		})
	}
}

// TestDraftsFolderRefusedAsSource pins that email_move and email_mark
// leave the drafts folder alone as a source, however the drafts folder
// is known, and that destination_role drafts meets the destination
// refusal.
func TestDraftsFolderRefusedAsSource(t *testing.T) {
	tests := []struct {
		name   string
		drafts string // configured drafts_folder; "" means the server's \Drafts
		source string
		call   func(*Tools, context.Context, map[string]any) (string, error)
		args   map[string]any
		want   []string
	}{
		{"move out of the special-use drafts folder", "", "Drafts", (*Tools).HandleMove, map[string]any{"destination": "Receipts"}, []string{`email_move cannot act on mail in "Drafts": it is account "primary"'s drafts folder`, "nothing was moved"}},
		{"mark in the special-use drafts folder", "", "Drafts", (*Tools).HandleMark, map[string]any{"flag": "flagged"}, []string{`email_mark cannot act on mail in "Drafts"`, "nothing was changed"}},
		{"mark in a configured drafts folder", "Receipts", "Receipts", (*Tools).HandleMark, map[string]any{"flag": "seen", "add": false}, []string{`email_mark cannot act on mail in "Receipts"`, "nothing was changed"}},
		{"move out of a configured drafts folder by role", "Receipts", "Receipts", (*Tools).HandleMove, map[string]any{"destination_role": "inbox"}, []string{`email_move cannot act on mail in "Receipts"`}},
		{"destination_role drafts is the drafts destination", "", "INBOX", (*Tools).HandleMove, map[string]any{"destination_role": "drafts"}, []string{`email_move cannot file mail into "Drafts"`, "drafts folder"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem := filingService(t, identityStub(), false, func(a *AccountConfig) { a.DraftsFolder = tt.drafts })
			source := tt.source
			uid := mem.append(source, rawMessage("Alice <alice@example.org>", "bob@example.org", "draft", "x"), imap.FlagSeen)
			args := map[string]any{"folder": source, "uid": float64(uid)}
			for k, v := range tt.args {
				args[k] = v
			}
			out, err := tt.call(svc.ToolProvider(), attendedCtx(), args)
			if err == nil {
				t.Fatalf("result = %s, want a refusal", out)
			}
			mustContain(t, err.Error(), tt.want...)
			acct, _ := svc.ResolveAccount(context.Background(), "primary")
			listed, err := acct.Client.ListMessages(context.Background(), ListOptions{Folder: source})
			if err != nil || len(listed.Envelopes) != 1 || !slices.Equal(listed.Envelopes[0].Flags, []string{string(imap.FlagSeen)}) {
				t.Errorf("%s after a refusal = %+v, %v; the message and its flags must be unchanged", source, listed.Envelopes, err)
			}
		})
	}

	// A control: the same calls on INBOX go through.
	svc, mem := filingService(t, identityStub(), false, nil)
	uid := mem.append("INBOX", rawMessage("Alice <alice@example.org>", "bob@example.org", "inbox", "x"))
	if _, err := svc.ToolProvider().HandleMark(attendedCtx(), map[string]any{"uid": float64(uid), "flag": "flagged"}); err != nil {
		t.Fatalf("mark in INBOX: %v", err)
	}
	if _, err := svc.ToolProvider().HandleMove(attendedCtx(), map[string]any{"uid": float64(uid), "destination": "Receipts"}); err != nil {
		t.Fatalf("move out of INBOX: %v", err)
	}
}

// TestFolderRolesMatchConfig pins the email package's role vocabulary
// to the one config validates move_into against.
func TestFolderRolesMatchConfig(t *testing.T) {
	var ours []string
	for _, r := range allFolderRoles {
		ours = append(ours, string(r))
	}
	if cfg := platformconfig.EmailFolderRoles(); !slices.Equal(ours, cfg) {
		t.Errorf("email roles %v, config roles %v", ours, cfg)
	}
	if got := destinationRoleNames(); slices.Contains(got, "drafts") || len(got) != len(allFolderRoles)-1 {
		t.Errorf("destinationRoleNames() = %v, want every role but drafts", got)
	}
	for _, attr := range []imap.MailboxAttr{imap.MailboxAttrDrafts, imap.MailboxAttrSent, imap.MailboxAttrTrash, imap.MailboxAttrJunk, imap.MailboxAttrArchive, imap.MailboxAttrAll, imap.MailboxAttrFlagged, imap.MailboxAttrImportant} {
		if role := roleFromAttrs([]imap.MailboxAttr{attr}, "x"); !slices.Contains(allFolderRoles, role) {
			t.Errorf("roleFromAttrs(%s) = %q, which allFolderRoles lacks", attr, role)
		}
	}
}

// TestEmailAccountsEntryRendersFiling pins the filing fields in the
// Email Accounts block: an operator entry shows its resolved junk
// folder and move_into, a role no folder is known to hold yet shows as
// role:<role>, a configured junk_folder shows before any listing, a
// limited assistant entry shows its list, filing_note shows when set,
// and an assistant entry at its defaults adds nothing.
func TestEmailAccountsEntryRendersFiling(t *testing.T) {
	svc, mem, _ := identityServiceWith(t, ServiceDependencies{Contacts: identityStub()}, func(cfg *Config) {
		base := cfg.Accounts[0]
		add := func(name string, mailbox MailboxConfig, junk string) {
			cfg.Accounts = append(cfg.Accounts, AccountConfig{Name: name, IMAP: base.IMAP, JunkFolder: junk, Mailbox: mailbox})
		}
		add("personal", MailboxConfig{Owner: "operator", FilingNote: " The operator files receipts by hand. "}, "")
		add("cold", MailboxConfig{Owner: "operator"}, "")
		add("configured", MailboxConfig{Owner: "operator"}, "Receipts")
		add("limited", MailboxConfig{MoveInto: []string{"role:archive", "Receipts"}}, "")
		add("noted", MailboxConfig{FilingNote: "Newsletters stay in INBOX."}, "")
		add("everywhere", MailboxConfig{Owner: "operator", MoveInto: []string{"*"}}, "")
	})
	mem.addFolder("Receipts")
	mem.addFolder(junkTestFolder, imap.MailboxAttrJunk)
	for _, account := range []string{"primary", "personal", "limited", "noted", "everywhere"} {
		if _, err := svc.ToolProvider().HandleFolders(context.Background(), map[string]any{"account": account}); err != nil {
			t.Fatalf("HandleFolders(%s): %v", account, err)
		}
	}

	block, err := svc.ContextProvider().TagContext(context.Background(), agentctxRequest())
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	var payload struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(block, "### Email Accounts\n\n")), &payload); err != nil {
		t.Fatalf("block = %s, %v", block, err)
	}
	entries := make(map[string]map[string]any)
	for _, e := range payload.Accounts {
		entries[e["account"].(string)] = e
	}

	tests := []struct {
		account      string
		wantJunk     string
		wantMoveInto []string
		wantNote     string
	}{
		{"primary", "", nil, ""},
		{"personal", junkTestFolder, []string{junkTestFolder}, "The operator files receipts by hand."},
		{"cold", "", []string{"role:junk"}, ""},
		{"configured", "Receipts", []string{"Receipts"}, ""},
		{"limited", junkTestFolder, []string{"Archive", "Receipts"}, ""},
		{"noted", "", nil, "Newsletters stay in INBOX."},
		{"everywhere", junkTestFolder, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.account, func(t *testing.T) {
			entry := entries[tt.account]
			if got, _ := entry["junk_folder"].(string); got != tt.wantJunk {
				t.Errorf("junk_folder = %q, want %q", got, tt.wantJunk)
			}
			var moveInto []string
			if raw, ok := entry["move_into"].([]any); ok {
				for _, v := range raw {
					moveInto = append(moveInto, v.(string))
				}
			}
			if !slices.Equal(moveInto, tt.wantMoveInto) {
				t.Errorf("move_into = %q, want %q", moveInto, tt.wantMoveInto)
			}
			if got, _ := entry["filing_note"].(string); got != tt.wantNote {
				t.Errorf("filing_note = %q, want %q", got, tt.wantNote)
			}
			for _, key := range []string{"junk_folder", "move_into", "filing_note"} {
				if _, present := entry[key]; present && tt.account == "primary" {
					t.Errorf("an assistant entry at its defaults must not render %s", key)
				}
			}
		})
	}

	// An assistant entry at its defaults contributes no bytes at all.
	for _, cfg := range svc.AccountsInConfigOrder() {
		if cfg.Name != "primary" {
			continue
		}
		data, err := json.Marshal(svc.newFilingView(cfg))
		if err != nil || string(data) != "{}" {
			t.Errorf("default filing view = %s, %v; want {}", data, err)
		}
	}
}
