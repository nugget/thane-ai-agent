package email

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// TestAuditCopyIsExemptFromTheTrustGate pins the bcc_owner contract:
// the operator's audit address needs no contact record, so a send to a
// trusted recipient succeeds and the audit copy rides the envelope only.
func TestAuditCopyIsExemptFromTheTrustGate(t *testing.T) {
	imap := newMemIMAP(t)
	smtp := newSMTPFake(t, nil)
	zero := 0
	cfg := Config{
		BccOwner:     "Audit <audit@example.com>",
		PollInterval: &zero,
		Accounts: []AccountConfig{{
			Name:        "primary",
			IMAP:        imap.imapConfig(),
			SMTP:        smtp.config("thane", "pw"),
			DefaultFrom: "Thane <thane@example.com>",
			SentFolder:  "Sent",
		}},
	}
	resolver := &stubContacts{zones: map[string]string{"alice@example.com": "trusted"}}
	svc, err := NewService(cfg, ServiceDependencies{Contacts: resolver, Logger: quietSlog()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)

	out, err := svc.ToolProvider().HandleSend(context.Background(), map[string]any{
		"to": []any{"alice@example.com"}, "subject": "hi", "body": "hello",
	})
	if err != nil {
		t.Fatalf("a send to a trusted recipient must not be refused over the audit copy: %v", err)
	}
	mustContain(t, out, `"bcc_count":1`, `"sent_folder":"Sent"`, `"sent_folder_copy":"stored"`)
	got := smtp.received()
	if len(got) != 1 {
		t.Fatalf("deliveries = %d, want 1", len(got))
	}
	if !strings.Contains(strings.Join(got[0].To, ","), "audit@example.com") {
		t.Errorf("RCPT TO = %v, want the audit copy in the envelope", got[0].To)
	}
	if strings.Contains(got[0].Data, "audit@example.com") {
		t.Error("the audit copy must not appear in the delivered headers")
	}
}

// TestContextBlockFallsBackToTheIMAPUsername pins the address shown for
// an account without default_from: its IMAP login when that is an
// address, and nothing when it is a bare user name.
func TestContextBlockFallsBackToTheIMAPUsername(t *testing.T) {
	zero := 0
	cfg := Config{PollInterval: &zero, Accounts: []AccountConfig{
		{Name: "packages", IMAP: IMAPConfig{Host: "imap.example.com", Username: "packages@example.com"}},
		{Name: "legacy", IMAP: IMAPConfig{Host: "imap.example.com", Username: "legacyuser"}},
	}}
	svc, err := NewService(cfg, ServiceDependencies{Logger: quietSlog()})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)
	got, err := svc.ContextProvider().TagContext(context.Background(), agentctx.ContextRequest{})
	if err != nil {
		t.Fatalf("TagContext: %v", err)
	}
	var payload emailContextJSON
	if err := json.Unmarshal([]byte(strings.TrimPrefix(got, "### Email Accounts\n\n")), &payload); err != nil {
		t.Fatalf("payload is not JSON: %v\n%s", err, got)
	}
	if payload.Accounts[0].Address != "packages@example.com" {
		t.Errorf("packages address = %q, want the IMAP login", payload.Accounts[0].Address)
	}
	if payload.Accounts[1].Address != "" {
		t.Errorf("a bare user name must not be shown as an address, got %q", payload.Accounts[1].Address)
	}
}

// TestFolderMissRefreshesTheContextCache pins the promise listFolders
// makes: a tool that hits a missing folder re-lists, so the next turn's
// Email Accounts block carries the real names.
func TestFolderMissRefreshesTheContextCache(t *testing.T) {
	svc, primary, _ := twoAccountService(t)
	uid := primary.append("INBOX", rawMessage("a@example.com", "b@example.com", "one", "1"))
	if _, ok := svc.cachedFolders("primary"); ok {
		t.Fatal("cache must start empty")
	}
	_, err := svc.ToolProvider().HandleMove(context.Background(), map[string]any{"uid": float64(uid), "destination": "Nope"})
	if FailureKindOf(err) != FailureFolderNotFound {
		t.Fatalf("move to a missing folder = %v, want folder_not_found", err)
	}
	snap, ok := svc.cachedFolders("primary")
	if !ok || len(snap.Folders) != 5 {
		t.Errorf("cache after a folder miss = %+v, %v; the miss must re-list", snap, ok)
	}
}

// TestHandleFoldersCapsTheResult pins the bound on email_folders: a
// label-heavy account lists at most maxFolderResults, keeps the role
// folders, says it truncated, and the cache still holds everything.
func TestHandleFoldersCapsTheResult(t *testing.T) {
	svc, primary, _ := twoAccountService(t)
	for i := 0; i < maxFolderResults+5; i++ {
		name := fmt.Sprintf("Label%03d", i)
		if err := primary.user.Create(name, nil); err != nil {
			t.Fatalf("create folder: %v", err)
		}
		primary.mu.Lock()
		primary.folders = append(primary.folders, name)
		primary.mu.Unlock()
	}
	out, err := svc.ToolProvider().HandleFolders(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("HandleFolders: %v", err)
	}
	var resp foldersResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	total := maxFolderResults + 5 + 5
	if resp.Count != maxFolderResults || len(resp.Folders) != maxFolderResults || resp.Total != total || !resp.Truncated {
		t.Errorf("capped response = count %d, folders %d, total %d, truncated %v", resp.Count, len(resp.Folders), resp.Total, resp.Truncated)
	}
	for _, role := range []FolderRole{RoleDrafts, RoleSent, RoleTrash, RoleArchive} {
		if _, ok := FindFolderByRole(resp.Folders, role); !ok {
			t.Errorf("role %s dropped by the cap", role)
		}
	}
	if snap, ok := svc.cachedFolders("primary"); !ok || len(snap.Folders) != total {
		t.Errorf("the cache must keep the full listing, got %d", len(snap.Folders))
	}
}

// TestBoundAccountWithWhitespaceResolves pins that a binding written
// with stray whitespace resolves at runtime, as it did at hydration.
func TestBoundAccountWithWhitespaceResolves(t *testing.T) {
	svc, _, _ := twoAccountService(t)
	acct, err := svc.ResolveAccount(boundEmailCtx(" packages "), "")
	if err != nil || acct.Name != "packages" {
		t.Fatalf("omitted account under a padded binding = %+v, %v", acct, err)
	}
	if _, err := svc.ResolveAccount(boundEmailCtx(" packages "), "packages"); err != nil {
		t.Errorf("naming the bound account under a padded binding must be allowed: %v", err)
	}
}
