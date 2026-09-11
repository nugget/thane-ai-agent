package email

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// twoAccountService builds a Service over two in-memory IMAP servers:
// "primary" can send, "packages" cannot. Both are real loopback
// servers, so handler tests exercise the wire.
func twoAccountService(t *testing.T) (*Service, *memIMAP, *memIMAP) {
	t.Helper()
	primary := newMemIMAP(t)
	packages := newMemIMAP(t)
	startTLS := true
	cfg := Config{
		BccOwner: "Operator <operator@example.com>",
		Accounts: []AccountConfig{
			{
				Name:        "primary",
				Description: "Thane's own mailbox.",
				IMAP:        primary.imapConfig(),
				SMTP:        SMTPConfig{Host: "smtp.example.com", Port: 587, Username: "thane", Password: "pw", StartTLS: &startTLS},
				DefaultFrom: "Thane <thane@example.com>",
				SentFolder:  "Sent",
			},
			{
				Name:        "packages",
				Description: "Parcel notifications. Never sends.",
				IMAP:        packages.imapConfig(),
			},
		},
	}
	svc, err := NewService(cfg, ServiceDependencies{State: testOpstate(t), Logger: quietSlog()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc, primary, packages
}

func agentctxRequest() agentctx.ContextRequest { return agentctx.ContextRequest{} }

func boundEmailCtx(account string) context.Context {
	return looppkg.WithBindings(context.Background(), map[string]string{looppkg.BindingEmailAccount: account})
}

func TestNewServiceValidatesConfig(t *testing.T) {
	_, err := NewService(Config{}, ServiceDependencies{})
	if err == nil {
		t.Fatal("an empty config must be refused")
	}
	_, err = NewService(Config{Accounts: []AccountConfig{{Name: "a", IMAP: IMAPConfig{Host: "h", Username: "u"}}, {Name: "a", IMAP: IMAPConfig{Host: "h", Username: "u"}}}}, ServiceDependencies{})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate account names must be refused, got %v", err)
	}
	// Polling on without a state store cannot work and must say so.
	interval := 60
	_, err = NewService(Config{PollInterval: &interval, Accounts: []AccountConfig{{Name: "a", IMAP: IMAPConfig{Host: "h", Username: "u"}}}}, ServiceDependencies{})
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("polling without state must be refused, got %v", err)
	}
	// Polling off needs no state and no bus.
	zero := 0
	svc, err := NewService(Config{PollInterval: &zero, Accounts: []AccountConfig{{Name: "a", IMAP: IMAPConfig{Host: "h", Username: "u"}}}}, ServiceDependencies{})
	if err != nil {
		t.Fatalf("NewService without polling: %v", err)
	}
	if svc.PollingEnabled() {
		t.Error("poll_interval 0 must disable polling")
	}
	if _, err := svc.CheckNewMessages(context.Background()); err == nil {
		t.Error("CheckNewMessages with polling disabled must error")
	}
}

func TestServiceRegistersEveryToolThroughTheProvider(t *testing.T) {
	svc, _, _ := twoAccountService(t)
	provider := svc.ToolProvider()
	if provider.Name() != "email" {
		t.Errorf("provider name = %q", provider.Name())
	}
	reg := tools.NewEmptyRegistry()
	reg.RegisterProvider(provider)

	want := []string{"email_list", "email_read", "email_folders", "email_search", "email_mark", "email_send", "email_reply", "email_move"}
	for _, name := range want {
		tool := reg.Get(name)
		if tool == nil {
			t.Errorf("tool %s not registered", name)
			continue
		}
		if tool.Handler == nil {
			t.Errorf("tool %s has no handler", name)
		}
	}
	if got := len(provider.Tools()); got != len(want) {
		t.Errorf("provider declares %d tools, want %d", got, len(want))
	}
}

// TestEmailAccountDescriptionTeachesBinding pins the tool surface and
// the loop-spec surface to the same story: an omitted account resolves
// to the binding, not unconditionally to the primary account.
func TestEmailAccountDescriptionTeachesBinding(t *testing.T) {
	if !strings.Contains(emailAccountDescription, "bound account") {
		t.Errorf("emailAccountDescription does not mention the bound account: %q", emailAccountDescription)
	}
	if strings.Contains(emailAccountDescription, "default: primary") {
		t.Errorf("emailAccountDescription promises the primary account unconditionally: %q", emailAccountDescription)
	}
}

// TestEveryEmailToolAccountParamUsesTheSharedDescription guards the
// other half: a tool that spells its own account description inline
// would drift silently.
func TestEveryEmailToolAccountParamUsesTheSharedDescription(t *testing.T) {
	var checked int
	for _, tool := range (&Tools{}).toolDefinitions() {
		props, ok := tool.Parameters["properties"].(map[string]any)
		if !ok {
			t.Errorf("%s: parameters have no properties", tool.Name)
			continue
		}
		raw, ok := props["account"]
		if !ok {
			t.Errorf("%s: every email tool must take an account parameter", tool.Name)
			continue
		}
		account, ok := raw.(map[string]any)
		if !ok {
			t.Errorf("%s: account parameter is not an object schema", tool.Name)
			continue
		}
		checked++
		if desc, _ := account["description"].(string); desc != emailAccountDescription {
			t.Errorf("%s: account description is not the shared constant: %q", tool.Name, desc)
		}
	}
	if checked == 0 {
		t.Fatal("no email tools were checked")
	}
}

func TestServiceResolveAccountHonorsBinding(t *testing.T) {
	svc, _, _ := twoAccountService(t)
	tests := []struct {
		name        string
		ctx         context.Context
		account     string
		wantAccount string
		wantErr     []string
	}{
		{"unbound omitted resolves to primary", context.Background(), "", "primary", nil},
		{"unbound names any account", context.Background(), "packages", "packages", nil},
		{"unbound unknown lists choices", context.Background(), "nope", "", []string{`"nope"`, "primary", "packages"}},
		{"bound omitted resolves to the binding", boundEmailCtx("packages"), "", "packages", nil},
		{"bound same name is fine", boundEmailCtx("packages"), "packages", "packages", nil},
		{"bound other name is refused with the retry", boundEmailCtx("packages"), "primary", "", []string{`"primary"`, `bound to account "packages"`, "account=\"packages\""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.ResolveAccount(tt.ctx, tt.account)
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("expected error, got account %q", got.Name)
				}
				mustContain(t, err.Error(), tt.wantErr...)
				return
			}
			if err != nil {
				t.Fatalf("ResolveAccount: %v", err)
			}
			if got.Name != tt.wantAccount || got.Client == nil || got.Config.Name != tt.wantAccount {
				t.Errorf("resolved %+v, want %s", got, tt.wantAccount)
			}
		})
	}
}

func TestHandleListReturnsJSONWithAccountAndFolder(t *testing.T) {
	svc, primary, packages := twoAccountService(t)
	primary.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi"))
	uidP := packages.append("INBOX", rawMessage("usps@example.com", "packages@example.com", "Delivered", "x"))

	out, err := svc.ToolProvider().HandleList(context.Background(), map[string]any{"account": "packages"})
	if err != nil {
		t.Fatalf("HandleList: %v", err)
	}
	var resp listResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, out)
	}
	if resp.Account != "packages" || resp.Folder != "INBOX" || resp.Count != 1 || resp.TotalMatched != 1 || resp.Truncated {
		t.Errorf("list response = %+v", resp)
	}
	if len(resp.Messages) != 1 || resp.Messages[0].UID != uidP || resp.Messages[0].From.Address != "usps@example.com" || resp.Messages[0].MessageID != messageIDFor("Delivered") {
		t.Errorf("messages = %+v", resp.Messages)
	}
	if !strings.HasPrefix(resp.Messages[0].Date, "-") && !strings.HasPrefix(resp.Messages[0].Date, "+") {
		t.Errorf("date should be a delta, got %q", resp.Messages[0].Date)
	}

	// Empty is count 0 with an explicit array, not prose.
	out, err = svc.ToolProvider().HandleList(context.Background(), map[string]any{"account": "packages", "folder": "Archive"})
	if err != nil {
		t.Fatalf("HandleList(Archive): %v", err)
	}
	mustContain(t, out, `"count":0`, `"messages":[]`, `"folder":"Archive"`)

	// A bound caller cannot reach the other account.
	_, err = svc.ToolProvider().HandleList(boundEmailCtx("packages"), map[string]any{"account": "primary"})
	if err == nil || !strings.Contains(err.Error(), "bound to account") {
		t.Fatalf("bound list of another account: %v", err)
	}
}

func TestHandleReadRendersHeaderJSONThenBody(t *testing.T) {
	svc, primary, _ := twoAccountService(t)
	uid := primary.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Lunch", "tacos at noon?", "Reply-To: alice-work@example.com"))

	out, err := svc.ToolProvider().HandleRead(context.Background(), map[string]any{"uid": float64(uid), "mark_seen": false})
	if err != nil {
		t.Fatalf("HandleRead: %v", err)
	}
	headerJSON, body, found := strings.Cut(out, bodySeparator)
	if !found {
		t.Fatalf("result lacks the body separator:\n%s", out)
	}
	var header readResponse
	if err := json.Unmarshal([]byte(headerJSON), &header); err != nil {
		t.Fatalf("header is not JSON: %v\n%s", err, headerJSON)
	}
	if header.Account != "primary" || header.Folder != "INBOX" || header.UID != uid || header.MarkedSeen || header.BodySource != "text" {
		t.Errorf("header = %+v", header)
	}
	if header.From == nil || header.From.Name != "Alice" || len(header.ReplyTo) != 1 || header.ReplyTo[0].Address != "alice-work@example.com" {
		t.Errorf("addresses = from %+v reply_to %+v", header.From, header.ReplyTo)
	}
	if header.Attachments == nil {
		t.Error("attachments must render as an array, never null")
	}
	if strings.TrimSpace(body) != "tacos at noon?" {
		t.Errorf("body = %q", body)
	}

	_, err = svc.ToolProvider().HandleRead(context.Background(), map[string]any{"uid": float64(9999)})
	if err == nil {
		t.Fatal("missing uid must error")
	}
	mustContain(t, err.Error(), "uid=9999", `"primary"`, `"INBOX"`)

	_, err = svc.ToolProvider().HandleRead(context.Background(), map[string]any{"folder": "INBOX"})
	if err == nil || !strings.Contains(err.Error(), "uid is required") {
		t.Fatalf("missing uid arg: %v", err)
	}
}

func TestHandleFoldersFeedsTheContextCache(t *testing.T) {
	svc, _, _ := twoAccountService(t)
	if _, ok := svc.cachedFolders("primary"); ok {
		t.Fatal("cache must start empty")
	}
	out, err := svc.ToolProvider().HandleFolders(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("HandleFolders: %v", err)
	}
	var resp foldersResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if resp.Account != "primary" || resp.Count != 5 {
		t.Errorf("folders response = %+v", resp)
	}
	if drafts, ok := FindFolderByRole(resp.Folders, RoleDrafts); !ok || drafts.Name != "Drafts" {
		t.Errorf("roles missing from folders result: %+v", resp.Folders)
	}
	snap, ok := svc.cachedFolders("primary")
	if !ok || len(snap.Folders) != 5 {
		t.Errorf("cache after listing = %+v, %v", snap, ok)
	}
}

func TestHandleMarkAndMoveReturnStructuredOutcomes(t *testing.T) {
	svc, primary, _ := twoAccountService(t)
	uid1 := primary.append("INBOX", rawMessage("a@example.com", "thane@example.com", "one", "1"))
	uid2 := primary.append("INBOX", rawMessage("a@example.com", "thane@example.com", "two", "2"))
	stale := uid2 + 100

	out, err := svc.ToolProvider().HandleMark(context.Background(), map[string]any{"uids": []any{float64(uid1), float64(stale)}, "flag": "flagged"})
	if err != nil {
		t.Fatalf("HandleMark: %v", err)
	}
	var mark markResponse
	if err := json.Unmarshal([]byte(out), &mark); err != nil {
		t.Fatalf("mark result is not JSON: %v", err)
	}
	if mark.Action != "flag_added" || mark.Account != "primary" || mark.Folder != "INBOX" || fmtUIDs(mark.UIDsAffected) != fmtUIDs([]uint32{uid1}) || fmtUIDs(mark.UIDsNotFound) != fmtUIDs([]uint32{stale}) {
		t.Errorf("mark response = %+v", mark)
	}

	_, err = svc.ToolProvider().HandleMark(context.Background(), map[string]any{"uid": float64(uid1), "flag": "deleted"})
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unsupported flag: %v", err)
	}
	_, err = svc.ToolProvider().HandleMark(context.Background(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "uids is required") || !strings.Contains(err.Error(), "flag is required") {
		t.Fatalf("every field violation must be reported together: %v", err)
	}

	out, err = svc.ToolProvider().HandleMove(context.Background(), map[string]any{"uids": []any{float64(uid1), float64(uid2)}, "destination": "Archive"})
	if err != nil {
		t.Fatalf("HandleMove: %v", err)
	}
	var move moveResponse
	if err := json.Unmarshal([]byte(out), &move); err != nil {
		t.Fatalf("move result is not JSON: %v", err)
	}
	if move.Action != "moved" || move.SourceFolder != "INBOX" || move.DestinationFolder != "Archive" || !move.DestinationUIDsKnown || len(move.DestinationUIDs) != 2 {
		t.Errorf("move response = %+v", move)
	}

	_, err = svc.ToolProvider().HandleMove(context.Background(), map[string]any{"uid": float64(uid1), "destination": "Promotions"})
	if err == nil {
		t.Fatal("unknown destination must error")
	}
	mustContain(t, err.Error(), `"Promotions"`, "Archive", "Trash")

	// folder-as-destination alias still works.
	uid3 := primary.append("INBOX", rawMessage("a@example.com", "thane@example.com", "three", "3"))
	out, err = svc.ToolProvider().HandleMove(context.Background(), map[string]any{"uid": float64(uid3), "folder": "Trash"})
	if err != nil {
		t.Fatalf("HandleMove alias: %v", err)
	}
	mustContain(t, out, `"destination_folder":"Trash"`, `"source_folder":"INBOX"`)
}

func TestHandleSearchRejectsBadDatesTogether(t *testing.T) {
	svc, _, _ := twoAccountService(t)
	_, err := svc.ToolProvider().HandleSearch(context.Background(), map[string]any{"since": "yesterday", "before": "soon"})
	if err == nil {
		t.Fatal("bad dates must error")
	}
	mustContain(t, err.Error(), "since must be", "before must be")
}

func TestHandleSendRefusesAccountWithoutSMTP(t *testing.T) {
	svc, _, _ := twoAccountService(t)
	_, err := svc.ToolProvider().HandleSend(context.Background(), map[string]any{
		"account": "packages", "to": []any{"alice@example.com"}, "subject": "hi", "body": "x",
	})
	if err == nil {
		t.Fatal("sending from an account without smtp must be refused")
	}
	mustContain(t, err.Error(), `"packages"`, "no smtp configured")
}

func TestContextProviderRendersAccountsFoldersAndBinding(t *testing.T) {
	svc, _, _ := twoAccountService(t)
	provider := svc.ContextProvider()
	if provider.TagContextBucket() != agentctx.ContextBucketLiveState {
		t.Errorf("bucket = %v, want live state", provider.TagContextBucket())
	}

	got, err := provider.TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	if !strings.HasPrefix(got, "### Email Accounts\n\n") {
		t.Fatalf("missing heading: %s", got)
	}
	var payload emailContextJSON
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got, "### Email Accounts\n\n")), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v\n%s", err, got)
	}
	if len(payload.Accounts) != 2 || payload.Accounts[0].Account != "primary" || payload.Accounts[1].Account != "packages" {
		t.Fatalf("accounts = %+v", payload.Accounts)
	}
	primary := payload.Accounts[0]
	if !primary.CanSend || primary.Address != "thane@example.com" || primary.Description == "" || primary.Bound {
		t.Errorf("primary view = %+v", primary)
	}
	if payload.Accounts[1].CanSend {
		t.Error("packages must not claim it can send")
	}
	if primary.Folders != nil || strings.Contains(got, `"folders":[]`) {
		t.Errorf("folders must render as null before any listing, got %s", got)
	}
	if strings.Contains(got, "pw") || strings.Contains(got, testIMAPPass) {
		t.Fatal("context block leaked a password")
	}

	// A listing feeds the cache; the block then names folders with roles.
	if _, err := svc.ToolProvider().HandleFolders(context.Background(), map[string]any{"account": "packages"}); err != nil {
		t.Fatalf("HandleFolders: %v", err)
	}
	got, _ = provider.TagContext(context.Background(), agentctx.ContextRequest{})
	mustContain(t, got, `{"name":"Archive","role":"archive"}`, `"folders_as_of":"-`, `"recent_operations":[{"tool":"email_folders","account":"packages"`)
	if strings.Contains(got, `"messages"`) {
		t.Error("the context block must not render churning message counts")
	}

	// Bound: only the bound account, marked bound, ops filtered.
	got, _ = provider.TagContext(boundEmailCtx("primary"), agentctx.ContextRequest{})
	var bound emailContextJSON
	_ = json.Unmarshal([]byte(strings.TrimPrefix(got, "### Email Accounts\n\n")), &bound)
	if len(bound.Accounts) != 1 || bound.Accounts[0].Account != "primary" || !bound.Accounts[0].Bound {
		t.Errorf("bound accounts = %+v", bound.Accounts)
	}
	if len(bound.RecentOps) != 0 {
		t.Errorf("recent ops for another account leaked into the bound view: %+v", bound.RecentOps)
	}

	// A binding to a vanished account is reported inside the JSON.
	got, _ = provider.TagContext(boundEmailCtx("ghost"), agentctx.ContextRequest{})
	mustContain(t, got, `"accounts":[]`, `"binding_error":"This loop is bound to email account ghost`)
}

func TestFolderViewsCapAndOrder(t *testing.T) {
	var folders []Folder
	for i := 0; i < maxContextFolders+5; i++ {
		folders = append(folders, Folder{Name: "Label-" + string(rune('a'+i)), Selectable: true})
	}
	folders = append(folders, Folder{Name: "Zed-Trash", Role: RoleTrash, Selectable: true}, Folder{Name: "[Container]", Selectable: false})
	views, truncated := folderViews(folders)
	if !truncated || len(views) != maxContextFolders {
		t.Fatalf("views = %d truncated = %v", len(views), truncated)
	}
	if views[0].Name != "Zed-Trash" || views[0].Role != RoleTrash {
		t.Errorf("role-bearing folders must sort first, got %+v", views[0])
	}
	for _, v := range views {
		if v.Name == "[Container]" {
			t.Error("non-selectable folders must not render")
		}
	}
}

func TestOperationLogRingIsNewestFirstAndBounded(t *testing.T) {
	log := NewOperationLog()
	for i := 0; i < defaultOpLogSize+3; i++ {
		log.Record(Operation{Tool: "email_list", Account: "primary", Ref: string(rune('a' + i))})
	}
	if log.Len() != defaultOpLogSize {
		t.Errorf("Len = %d, want %d", log.Len(), defaultOpLogSize)
	}
	recent := log.Recent(2)
	if len(recent) != 2 || recent[0].Ref != string(rune('a'+defaultOpLogSize+2)) || recent[0].Timestamp.IsZero() {
		t.Errorf("Recent = %+v", recent)
	}
	if recent[0].Timestamp.After(time.Now()) {
		t.Error("timestamps must be stamped at record time")
	}
	var nilLog *OperationLog
	nilLog.Record(Operation{})
	if nilLog.Recent(1) != nil || nilLog.Len() != 0 {
		t.Error("nil log must be inert")
	}
}
