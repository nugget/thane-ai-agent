package email

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// siteFolderName matches a folder name some server happens to use, as
// opposed to a role. Model-facing text names roles and sends the model
// to the folder listing for the name.
var siteFolderName = regexp.MustCompile(`\b(Archive|Trash|Junk|Spam)\b|\[Gmail\]|All Mail`)

// TestAccessRefusalNamesNoOtherAccount pins the refusal an account that
// cannot compose returns: it says the message belongs to this mailbox
// and must not be written anywhere else, and never points at another
// account, which on a site with a delivering assistant account would
// send the operator's mail out under the wrong name.
func TestAccessRefusalNamesNoOtherAccount(t *testing.T) {
	tests := []struct {
		name  string
		tweak func(*Config)
		call  func(*Tools, context.Context, map[string]any) (string, error)
		args  map[string]any
	}{
		{"send from organize with smtp", func(c *Config) { c.Accounts[0].Policy.Access = AccessOrganize }, (*Tools).HandleSend, sendArgs("operator@example.com")},
		{"send from read", func(c *Config) { c.Accounts[0].Policy.Access = AccessRead }, (*Tools).HandleSend, sendArgs("operator@example.com")},
		{"reply from organize", func(c *Config) { c.Accounts[0].Policy.Access = AccessOrganize }, (*Tools).HandleReply, map[string]any{"uid": float64(1), "body": "x"}},
		{"send from operator mailbox without smtp", func(c *Config) {
			c.Accounts[0].SMTP = SMTPConfig{}
			c.Accounts[0].Policy.Access = AccessOrganize
			c.Accounts[0].Mailbox.Owner = "operator"
		}, (*Tools).HandleSend, sendArgs("operator@example.com")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _ := policyService(t, ServiceDependencies{Contacts: identityStub()}, tt.tweak)
			_, err := tt.call(svc.ToolProvider(), attendedCtx(), tt.args)
			refusal := refusalOf(t, err)
			if refusal.Decision.Route != RouteAccess {
				t.Fatalf("route = %q, want access", refusal.Decision.Route)
			}
			msg := err.Error()
			mustContain(t, msg, `"primary" cannot send or draft mail`, "must not be written from any other account", "report that this account cannot compose")
			for _, steer := range []string{"Use an account", `access "send"`, "another account", "account that can send", "an account whose"} {
				if strings.Contains(msg, steer) {
					t.Errorf("refusal steers the model to another account with %q: %s", steer, msg)
				}
			}
		})
	}
}

// TestAuditBccRidesOnlySentMail pins where the bcc_owner copy goes:
// into the envelope of mail Thane delivers, and never into a draft,
// whatever put the message in the drafts folder.
func TestAuditBccRidesOnlySentMail(t *testing.T) {
	tests := []struct {
		name            string
		ctx             context.Context
		to              string
		draft           bool
		delivery        string
		wantDisposition Disposition
		wantBccCount    int
	}{
		{"attended admin is sent", attendedCtx(), "operator@example.com", false, "", DispositionSent, 1},
		{"unattended floor drafts", context.Background(), "operator@example.com", false, "", DispositionDrafted, 0},
		{"trusted recipient drafts", attendedCtx(), "alice@example.com", false, "", DispositionDrafted, 0},
		{"requested draft", attendedCtx(), "operator@example.com", true, "", DispositionDrafted, 0},
		{"drafts delivery", attendedCtx(), "operator@example.com", false, DeliveryDrafts, DispositionDrafted, 0},
		{"direct delivery is sent", context.Background(), "alice@example.com", false, DeliveryDirect, DispositionSent, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, smtp := policyService(t, ServiceDependencies{Contacts: identityStub()}, func(cfg *Config) {
				cfg.BccOwner = "Audit <audit@example.com>"
				cfg.Accounts[0].Policy.Delivery = tt.delivery
			})
			args := sendArgs(tt.to)
			if tt.draft {
				args["draft"] = true
			}
			out, err := svc.ToolProvider().HandleSend(tt.ctx, args)
			if err != nil {
				t.Fatalf("HandleSend: %v", err)
			}
			resp := decodeSend(t, out)
			if resp.Disposition != tt.wantDisposition || resp.BccCount != tt.wantBccCount {
				t.Fatalf("disposition %q bcc_count %d, want %q and %d", resp.Disposition, resp.BccCount, tt.wantDisposition, tt.wantBccCount)
			}
			mustContain(t, out, fmt.Sprintf(`"bcc_count":%d`, tt.wantBccCount))

			if tt.wantDisposition == DispositionSent {
				got := smtp.received()
				if len(got) != 1 || !slices.ContainsFunc(got[0].To, func(rcpt string) bool { return strings.Contains(rcpt, "audit@example.com") }) {
					t.Errorf("sent mail must carry the audit copy in its envelope, got %+v", got)
				}
				return
			}
			if len(smtp.received()) != 0 {
				t.Fatal("a draft must not reach SMTP")
			}
			acct, _ := svc.ResolveAccount(context.Background(), "primary")
			msg, err := acct.Client.ReadMessage(context.Background(), ReadOptions{Folder: resp.DraftsFolder, UID: resp.DraftUID, Peek: true})
			if err != nil {
				t.Fatalf("read draft: %v", err)
			}
			if raw := string(msg.raw); strings.Contains(raw, "Bcc:") || strings.Contains(raw, "audit@example.com") {
				t.Errorf("a draft must carry no audit Bcc:\n%s", raw)
			}
		})
	}
}

// TestEmailMoveRequiresDestination pins that folder is always the
// source: a move without destination is refused, names both arguments,
// and moves nothing.
func TestEmailMoveRequiresDestination(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{"only folder", map[string]any{"folder": "Trash"}},
		{"folder and empty destination", map[string]any{"folder": "Trash", "destination": ""}},
		{"blank destination", map[string]any{"destination": "   "}},
		{"neither", map[string]any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, primary, _ := twoAccountService(t)
			uid := primary.append("INBOX", rawMessage("a@example.com", "thane@example.com", "keep", "1"))
			args := map[string]any{"uid": float64(uid)}
			for k, v := range tt.args {
				args[k] = v
			}
			_, err := svc.ToolProvider().HandleMove(context.Background(), args)
			if err == nil {
				t.Fatal("a move without destination must be refused")
			}
			mustContain(t, err.Error(), "destination is required; folder is the source", "nothing was moved")
			acct, _ := svc.ResolveAccount(context.Background(), "primary")
			listed, err := acct.Client.ListMessages(context.Background(), ListOptions{Folder: "INBOX"})
			if err != nil || len(listed.Envelopes) != 1 || listed.Envelopes[0].UID != uid {
				t.Errorf("INBOX after a refused move = %+v, %v; the message must stay", listed.Envelopes, err)
			}
		})
	}
}

// TestMoveRecordsUIDsInRecentOperations pins the move's operation
// record: the Email Accounts block names the source UIDs and their UIDs
// in the destination, so a later turn can undo the move from the block.
func TestMoveRecordsUIDsInRecentOperations(t *testing.T) {
	svc, primary, _ := twoAccountService(t)
	uid1 := primary.append("INBOX", rawMessage("a@example.com", "thane@example.com", "one", "1"))
	uid2 := primary.append("INBOX", rawMessage("a@example.com", "thane@example.com", "two", "2"))
	out, err := svc.ToolProvider().HandleMove(context.Background(), map[string]any{"uids": []any{float64(uid1), float64(uid2)}, "destination": "Archive"})
	if err != nil {
		t.Fatalf("HandleMove: %v", err)
	}
	var move moveResponse
	if err := json.Unmarshal([]byte(out), &move); err != nil || len(move.DestinationUIDs) != 2 {
		t.Fatalf("move result = %s, %v", out, err)
	}

	block, err := svc.ContextProvider().TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	var payload emailContextJSON
	if err := json.Unmarshal([]byte(strings.TrimPrefix(block, "### Email Accounts\n\n")), &payload); err != nil || len(payload.RecentOps) == 0 {
		t.Fatalf("block = %s, %v", block, err)
	}
	op := payload.RecentOps[0]
	want := fmt.Sprintf("2 to Archive; uids %d,%d; destination_uids %d,%d", uid1, uid2, move.DestinationUIDs[0], move.DestinationUIDs[1])
	if op.Tool != "email_move" || op.Folder != "INBOX" || op.Ref != want {
		t.Errorf("recent operation = %+v, want ref %q", op, want)
	}
}

// TestMoveOperationRef pins the reference's shape, including a server
// that confirmed nothing and a bulk move over the UID cap.
func TestMoveOperationRef(t *testing.T) {
	many := make([]uint32, maxOpRefUIDs+2)
	for i := range many {
		many[i] = uint32(i + 1)
	}
	tests := []struct {
		name   string
		result MoveResult
		want   string
	}{
		{"confirmed", MoveResult{Destination: "Folder A", UIDs: []uint32{7, 9}, DestUIDs: []uint32{101, 102}, DestUIDsKnown: true}, "2 to Folder A; uids 7,9; destination_uids 101,102"},
		{"unconfirmed", MoveResult{Destination: "Folder A", UIDs: []uint32{7}}, "1 to Folder A; uids 7; destination_uids unknown, list Folder A to find them"},
		{"capped", MoveResult{Destination: "B", UIDs: many, DestUIDs: many, DestUIDsKnown: true}, "12 to B; uids 1,2,3,4,5,6,7,8,9,10 and 2 more; destination_uids 1,2,3,4,5,6,7,8,9,10 and 2 more"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := moveOperationRef(tt.result); got != tt.want {
				t.Errorf("moveOperationRef = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFlagInIsCaseInsensitive pins the mark read-back: IMAP flags are
// case-insensitive, so a server spelling \Seen differently still set it.
func TestFlagInIsCaseInsensitive(t *testing.T) {
	tests := []struct {
		name  string
		flags []imap.Flag
		want  imap.Flag
		found bool
	}{
		{"exact", []imap.Flag{imap.FlagSeen}, imap.FlagSeen, true},
		{"upper case", []imap.Flag{`\SEEN`}, imap.FlagSeen, true},
		{"lower case", []imap.Flag{`\flagged`}, imap.FlagFlagged, true},
		{"absent", []imap.Flag{imap.FlagFlagged}, imap.FlagSeen, false},
		{"no flags", nil, imap.FlagAnswered, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := flagIn(tt.flags, tt.want); got != tt.found {
				t.Errorf("flagIn(%v, %s) = %v, want %v", tt.flags, tt.want, got, tt.found)
			}
		})
	}
}

// seenFlag reports whether uid in INBOX carries \Seen.
func seenFlag(t *testing.T, svc *Service, uid uint32) bool {
	t.Helper()
	acct, _ := svc.ResolveAccount(context.Background(), "primary")
	listed, err := acct.Client.ListMessages(context.Background(), ListOptions{Folder: "INBOX"})
	if err != nil {
		t.Fatalf("list INBOX: %v", err)
	}
	for _, env := range listed.Envelopes {
		if env.UID == uid {
			return slices.Contains(env.Flags, string(imap.FlagSeen))
		}
	}
	t.Fatalf("uid %d not in INBOX", uid)
	return false
}

func ownerService(t *testing.T, owner string) (*Service, *memIMAP) {
	t.Helper()
	svc, mem, _ := policyService(t, ServiceDependencies{Contacts: identityStub()}, func(cfg *Config) {
		cfg.Accounts[0].Mailbox.Owner = owner
	})
	return svc, mem
}

// TestReadMarkSeenFollowsOwner pins email_read's mark_seen default per
// mailbox owner, and the refusal of an explicit mark_seen: true on an
// operator mailbox in a turn the operator is not present for.
func TestReadMarkSeenFollowsOwner(t *testing.T) {
	tests := []struct {
		name       string
		owner      string
		ctx        context.Context
		markSeen   any
		wantErr    bool
		wantMarked bool
	}{
		{"assistant default marks seen", "", context.Background(), nil, false, true},
		{"assistant explicit true unattended", "assistant", context.Background(), true, false, true},
		{"operator default leaves unread", "operator", context.Background(), nil, false, false},
		{"operator default attended leaves unread", "operator", attendedCtx(), nil, false, false},
		{"operator explicit false", "operator", context.Background(), false, false, false},
		{"operator explicit true unattended is refused", "operator", context.Background(), true, true, false},
		{"operator explicit true attended marks seen", "operator", attendedCtx(), true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem := ownerService(t, tt.owner)
			uid := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi"))
			args := map[string]any{"uid": float64(uid)}
			if tt.markSeen != nil {
				args["mark_seen"] = tt.markSeen
			}
			out, err := svc.ToolProvider().HandleRead(tt.ctx, args)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("read = %s, want a refusal", out)
				}
				mustContain(t, err.Error(), `account "primary"`, "operator's own mailbox", "mark_seen: false")
			} else if err != nil {
				t.Fatalf("HandleRead: %v", err)
			} else {
				mustContain(t, out, fmt.Sprintf(`"marked_seen":%v`, tt.wantMarked))
			}
			if got := seenFlag(t, svc, uid); got != tt.wantMarked {
				t.Errorf("\\Seen after read = %v, want %v", got, tt.wantMarked)
			}
		})
	}
}

// TestUnattendedSeenAddRefusedOnOperatorMailbox pins email_mark: adding
// seen on an operator mailbox needs the operator present; every other
// flag, removing seen, and every other mailbox are unaffected.
func TestUnattendedSeenAddRefusedOnOperatorMailbox(t *testing.T) {
	tests := []struct {
		name    string
		owner   string
		ctx     context.Context
		flag    string
		add     bool
		wantErr bool
	}{
		{"operator unattended seen add is refused", "operator", context.Background(), "seen", true, true},
		{"operator attended seen add", "operator", attendedCtx(), "seen", true, false},
		{"operator unattended flagged add", "operator", context.Background(), "flagged", true, false},
		{"operator unattended seen remove", "operator", context.Background(), "seen", false, false},
		{"assistant unattended seen add", "assistant", context.Background(), "seen", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem := ownerService(t, tt.owner)
			var initial []imap.Flag
			if !tt.add {
				initial = []imap.Flag{imap.FlagSeen}
			}
			uid := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi"), initial...)
			out, err := svc.ToolProvider().HandleMark(tt.ctx, map[string]any{"uid": float64(uid), "flag": tt.flag, "add": tt.add})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("mark = %s, want a refusal", out)
				}
				mustContain(t, err.Error(), `email_mark cannot mark mail seen on account "primary"`, "nothing was changed", `flag "flagged"`)
				if seenFlag(t, svc, uid) {
					t.Error("a refused mark must leave the message unread")
				}
				return
			}
			if err != nil {
				t.Fatalf("HandleMark: %v", err)
			}
			mustContain(t, out, fmt.Sprintf(`"uids_affected":[%d]`, uid))
		})
	}
}

// TestEmailAccountsEntryRendersMailbox pins the mailbox fields in the
// Email Accounts block: an operator mailbox says whose it is, the name
// it writes as, its voice, that reads leave mail unread, and its
// configured drafts folder before any listing; a voice alone adds only
// voice and writes_as; and a default entry renders exactly the keys it
// rendered before the mailbox block existed.
func TestEmailAccountsEntryRendersMailbox(t *testing.T) {
	zero := 0
	imapCfg := func(user string) IMAPConfig { return IMAPConfig{Host: "imap.example.com", Username: user} }
	cfg := Config{PollInterval: &zero, Accounts: []AccountConfig{
		{
			Name: "primary", Description: "Thane's own mailbox.", IMAP: imapCfg("thane@example.com"),
			SMTP:        SMTPConfig{Host: "smtp.example.com", Port: 587, Username: "thane", Password: "pw"},
			DefaultFrom: "Thane <thane@example.com>", SentFolder: "Sent",
		},
		{
			Name: "personal", Description: "Alice's inbox.", IMAP: imapCfg("alice@example.org"),
			DefaultFrom: "Alice Example <alice@example.org>", DraftsFolder: "Operator Drafts",
			Policy:  PolicyConfig{Access: AccessOrganize},
			Mailbox: MailboxConfig{Owner: "operator", Voice: " first person as Alice; brief "},
		},
		{
			Name: "helper", IMAP: imapCfg("helper@example.com"),
			DefaultFrom: "Helper <helper@example.com>",
			Mailbox:     MailboxConfig{Voice: "plain and short"},
		},
		{
			Name: "filer", Description: "Bob's newsletters.", IMAP: imapCfg("filer@example.com"),
			DraftsFolder: "Drafts",
			Policy:       PolicyConfig{Access: AccessOrganize},
		},
	}}
	svc, err := NewService(cfg, ServiceDependencies{Logger: quietSlog()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	block, err := svc.ContextProvider().TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	var payload struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(block, "### Email Accounts\n\n")), &payload); err != nil || len(payload.Accounts) != 4 {
		t.Fatalf("block = %s, %v", block, err)
	}

	tests := []struct {
		name     string
		entry    map[string]any
		wantKeys []string
		want     map[string]any
	}{
		{
			name:     "default entry is unchanged",
			entry:    payload.Accounts[0],
			wantKeys: []string{"access", "account", "address", "can_send", "delivery", "description", "drafts_for", "folders", "refuses", "sends_directly_to", "sent_folder"},
		},
		{
			name:     "operator mailbox",
			entry:    payload.Accounts[1],
			wantKeys: []string{"access", "account", "address", "can_send", "delivery", "description", "drafts_folder", "drafts_for", "folders", "owner", "reads_mark_seen", "refuses", "sends_directly_to", "voice", "writes_as"},
			want: map[string]any{
				"owner":           "operator",
				"writes_as":       "Alice Example <alice@example.org>",
				"voice":           "first person as Alice; brief",
				"reads_mark_seen": false,
				"drafts_folder":   "Operator Drafts",
				"address":         "alice@example.org",
			},
		},
		{
			name:     "voice alone",
			entry:    payload.Accounts[2],
			wantKeys: []string{"access", "account", "address", "can_send", "delivery", "drafts_for", "folders", "refuses", "sends_directly_to", "voice", "writes_as"},
			want:     map[string]any{"voice": "plain and short", "writes_as": "Helper <helper@example.com>"},
		},
		{
			name:     "assistant account that cannot draft shows no configured drafts folder",
			entry:    payload.Accounts[3],
			wantKeys: []string{"access", "account", "address", "can_send", "delivery", "description", "drafts_for", "folders", "refuses", "sends_directly_to"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := make([]string, 0, len(tt.entry))
			for k := range tt.entry {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			if !slices.Equal(keys, tt.wantKeys) {
				t.Errorf("entry keys = %v, want %v", keys, tt.wantKeys)
			}
			for k, v := range tt.want {
				if tt.entry[k] != v {
					t.Errorf("%s = %#v, want %#v", k, tt.entry[k], v)
				}
			}
		})
	}
}

// TestModelFacingEmailTextNamesNoSiteFolder guards the email tool
// surface against teaching a folder name as a destination: code never
// knows what a site calls its folders, so descriptions name roles and
// send the model to email_folders or the Email Accounts block.
func TestModelFacingEmailTextNamesNoSiteFolder(t *testing.T) {
	svc, _, _ := twoAccountService(t)
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case string:
			if m := siteFolderName.FindString(x); m != "" {
				t.Errorf("%s names the site folder %q: %s", path, m, x)
			}
		case map[string]any:
			for k, child := range x {
				walk(path+"."+k, child)
			}
		case []string:
			for i, child := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), child)
			}
		}
	}
	for _, tool := range svc.ToolProvider().Tools() {
		walk(tool.Name+".description", tool.Description)
		walk(tool.Name+".parameters", tool.Parameters)
	}
	for _, cfg := range []AccountConfig{
		{Name: "a", Policy: PolicyConfig{Access: AccessOrganize}},
		{Name: "a", Policy: PolicyConfig{Access: AccessRead}},
	} {
		walk("accessRefusalSentence", accessRefusalSentence(cfg))
	}
	_, err := svc.ToolProvider().HandleMove(context.Background(), map[string]any{"uid": float64(1)})
	if err == nil {
		t.Fatal("a move without destination must be refused")
	}
	walk("email_move refusal", err.Error())
}
