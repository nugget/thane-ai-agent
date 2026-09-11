package email

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

func TestClientListMessagesNewestFirstWithinLimit(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	m.append("INBOX", rawMessage("Alice <alice@example.com>", "bob@example.com", "first", "one"))
	m.append("INBOX", rawMessage("alice@example.com", "bob@example.com", "second", "two"))
	uid3 := m.append("INBOX", rawMessage("Carol <carol@example.com>", "bob@example.com", "third", "three"))

	got, err := c.ListMessages(context.Background(), ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if got.Folder != "INBOX" {
		t.Errorf("Folder = %q, want INBOX", got.Folder)
	}
	if got.UIDValidity == 0 {
		t.Error("UIDValidity should be reported")
	}
	if got.TotalMatched != 3 {
		t.Errorf("TotalMatched = %d, want 3", got.TotalMatched)
	}
	if !got.Truncated() {
		t.Error("Truncated() should be true when limit dropped a match")
	}
	if len(got.Envelopes) != 2 {
		t.Fatalf("len(Envelopes) = %d, want 2", len(got.Envelopes))
	}
	if got.Envelopes[0].UID != uid3 {
		t.Errorf("first envelope UID = %d, want newest %d", got.Envelopes[0].UID, uid3)
	}
	env := got.Envelopes[0]
	if env.From.Name != "Carol" || env.From.Address != "carol@example.com" {
		t.Errorf("From = %+v, want Carol <carol@example.com>", env.From)
	}
	if env.MessageID != messageIDFor("third") {
		t.Errorf("MessageID = %q, want %q", env.MessageID, messageIDFor("third"))
	}
	if len(env.To) != 1 || env.To[0].Address != "bob@example.com" {
		t.Errorf("To = %+v, want bob@example.com", env.To)
	}
}

func TestClientListMessagesSinceUIDReturnsOnlyNewer(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	uid1 := m.append("INBOX", rawMessage("a@example.com", "b@example.com", "one", "1"))
	m.append("INBOX", rawMessage("a@example.com", "b@example.com", "two", "2"))
	m.append("INBOX", rawMessage("a@example.com", "b@example.com", "three", "3"))

	got, err := c.ListMessages(context.Background(), ListOptions{SinceUID: uid1, Limit: 1})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(got.Envelopes) != 2 || got.TotalMatched != 2 {
		t.Fatalf("got %d envelopes (total %d), want 2 (SinceUID lifts the limit)", len(got.Envelopes), got.TotalMatched)
	}
	for _, env := range got.Envelopes {
		if env.UID <= uid1 {
			t.Errorf("envelope UID %d should be > %d", env.UID, uid1)
		}
	}

	// Nothing newer than the newest: the X:* range phantom is filtered.
	newest := got.Envelopes[0].UID
	none, err := c.ListMessages(context.Background(), ListOptions{SinceUID: newest})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(none.Envelopes) != 0 || none.TotalMatched != 0 {
		t.Errorf("expected no envelopes past the newest UID, got %d", len(none.Envelopes))
	}
}

func TestClientListUnseenExcludesSeen(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	seen := m.append("INBOX", rawMessage("a@example.com", "b@example.com", "seen", "x"), imap.FlagSeen)
	unseen := m.append("INBOX", rawMessage("a@example.com", "b@example.com", "unseen", "y"))

	got, err := c.ListMessages(context.Background(), ListOptions{Unseen: true})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(got.Envelopes) != 1 || got.Envelopes[0].UID != unseen {
		t.Fatalf("unseen listing = %+v, want only uid %d (seen uid %d excluded)", got.Envelopes, unseen, seen)
	}
}

func TestClientListLimitIsClamped(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	for i := 0; i < MaxListLimit+3; i++ {
		m.append("INBOX", rawMessage("a@example.com", "b@example.com", "bulk", "body"))
	}
	got, err := c.ListMessages(context.Background(), ListOptions{Limit: 5000})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(got.Envelopes) != MaxListLimit {
		t.Errorf("len = %d, want the ceiling %d", len(got.Envelopes), MaxListLimit)
	}
	if got.TotalMatched != MaxListLimit+3 {
		t.Errorf("TotalMatched = %d, want %d", got.TotalMatched, MaxListLimit+3)
	}
}

func TestClientListMissingFolderListsRealFolders(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("packages")

	_, err := c.ListMessages(context.Background(), ListOptions{Folder: "Promotions"})
	if err == nil {
		t.Fatal("expected an error for a folder that does not exist")
	}
	var cErr *ClientError
	if !errors.As(err, &cErr) {
		t.Fatalf("error type = %T, want *ClientError", err)
	}
	if cErr.Kind != FailureFolderNotFound {
		t.Errorf("Kind = %q, want %q", cErr.Kind, FailureFolderNotFound)
	}
	if cErr.Account != "packages" || cErr.Folder != "Promotions" {
		t.Errorf("Account/Folder = %q/%q", cErr.Account, cErr.Folder)
	}
	mustContain(t, err.Error(), `"packages"`, `"Promotions"`, "Archive", "Drafts", "INBOX", "Sent", "Trash")
	if FailureKindOf(err) != FailureFolderNotFound {
		t.Error("FailureKindOf should see through the error")
	}
}

func TestClientSearchMessages(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	m.append("INBOX", rawMessage("Alice <alice@example.com>", "bob@example.com", "Invoice 42", "pay me"))
	want := m.append("INBOX", rawMessage("Carol <carol@example.com>", "bob@example.com", "Lunch plans", "tacos?"))
	m.append("INBOX", rawMessage("alice@example.com", "bob@example.com", "Invoice 43", "pay me again"))

	cases := []struct {
		name string
		opts SearchOptions
		want []uint32
	}{
		{"by subject", SearchOptions{Subject: "Lunch"}, []uint32{want}},
		{"by from", SearchOptions{From: "carol"}, []uint32{want}},
		{"by message id", SearchOptions{MessageID: messageIDFor("Lunch plans")}, []uint32{want}},
		{"by text", SearchOptions{Query: "tacos"}, []uint32{want}},
		{"no match", SearchOptions{Subject: "nothing here"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.SearchMessages(context.Background(), tc.opts)
			if err != nil {
				t.Fatalf("SearchMessages: %v", err)
			}
			var uids []uint32
			for _, env := range got.Envelopes {
				uids = append(uids, env.UID)
			}
			if fmtUIDs(uids) != fmtUIDs(tc.want) {
				t.Errorf("uids = %v, want %v", uids, tc.want)
			}
			if got.TotalMatched != len(tc.want) {
				t.Errorf("TotalMatched = %d, want %d", got.TotalMatched, len(tc.want))
			}
		})
	}
}

func TestClientReadMessagePeekLeavesUnseen(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	uid := m.append("INBOX", rawMessage("Alice <alice@example.com>", "Bob <bob@example.com>", "Hello", "Hi Bob,\r\n\r\nLunch?", "Reply-To: alice-work@example.com", "References: <root@example.com>"))

	peeked, err := c.ReadMessage(context.Background(), ReadOptions{UID: uid, Peek: true})
	if err != nil {
		t.Fatalf("ReadMessage(peek): %v", err)
	}
	if peeked.TextBody != "Hi Bob,\r\n\r\nLunch?" && peeked.TextBody != "Hi Bob,\n\nLunch?" {
		t.Errorf("TextBody = %q", peeked.TextBody)
	}
	if peeked.BodySource != "text" {
		t.Errorf("BodySource = %q, want text", peeked.BodySource)
	}
	if len(peeked.ReplyTo) != 1 || peeked.ReplyTo[0].Address != "alice-work@example.com" {
		t.Errorf("ReplyTo = %+v", peeked.ReplyTo)
	}
	if len(peeked.References) != 1 || peeked.References[0] != "root@example.com" {
		t.Errorf("References = %v", peeked.References)
	}
	for _, f := range peeked.Flags {
		if f == string(imap.FlagSeen) {
			t.Fatal("peek must not mark the message seen")
		}
	}

	listed, err := c.ListMessages(context.Background(), ListOptions{Unseen: true})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(listed.Envelopes) != 1 {
		t.Fatalf("message should still be unseen after a peek, got %d unseen", len(listed.Envelopes))
	}

	if _, err := c.ReadMessage(context.Background(), ReadOptions{UID: uid}); err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	listed, err = c.ListMessages(context.Background(), ListOptions{Unseen: true})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(listed.Envelopes) != 0 {
		t.Fatalf("a non-peek read should mark the message seen, got %d unseen", len(listed.Envelopes))
	}
}

func TestClientReadMissingUIDNamesAccountAndFolder(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")

	_, err := c.ReadMessage(context.Background(), ReadOptions{UID: 284})
	var cErr *ClientError
	if !errors.As(err, &cErr) {
		t.Fatalf("error = %v (%T), want *ClientError", err, err)
	}
	if cErr.Kind != FailureMessageNotFound || cErr.UID != 284 || cErr.Folder != "INBOX" || cErr.Account != "primary" {
		t.Errorf("ClientError = %+v", cErr)
	}
	mustContain(t, err.Error(), "uid=284", `"primary"`, `"INBOX"`, "scoped")
}

func TestClientReadMessageDescribesAttachments(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	raw := "From: alice@example.com\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: Report\r\n" +
		"Message-ID: <report@example.com>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"mix\"\r\n" +
		"\r\n" +
		"--mix\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"See attached.\r\n" +
		"--mix\r\n" +
		"Content-Type: application/pdf; name=\"q3.pdf\"\r\n" +
		"Content-Disposition: attachment; filename=\"q3.pdf\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"JVBERi0xLjQKJcOkw7zDtsOfCg==\r\n" +
		"--mix\r\n" +
		"Content-Type: image/png\r\n" +
		"Content-Disposition: inline\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"iVBORw0KGgo=\r\n" +
		"--mix--\r\n"
	uid := m.append("INBOX", raw)

	msg, err := c.ReadMessage(context.Background(), ReadOptions{UID: uid, Peek: true})
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if msg.TextBody != "See attached." {
		t.Errorf("TextBody = %q", msg.TextBody)
	}
	if len(msg.Attachments) != 2 {
		t.Fatalf("Attachments = %+v, want 2", msg.Attachments)
	}
	pdf := msg.Attachments[0]
	if pdf.Filename != "q3.pdf" || pdf.ContentType != "application/pdf" || pdf.Inline || pdf.Size == 0 {
		t.Errorf("pdf attachment = %+v", pdf)
	}
	png := msg.Attachments[1]
	if png.ContentType != "image/png" || !png.Inline || png.Size == 0 {
		t.Errorf("inline image = %+v", png)
	}
}

func TestClientReadHTMLOnlyRendersText(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	raw := "From: shop@example.com\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: Your order\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<html><head><style>p{color:red}</style></head><body><h1>Order shipped</h1><p>Track it <a href=\"https://example.com/t/1\">here</a>.</p><script>alert(1)</script></body></html>\r\n"
	uid := m.append("INBOX", raw)

	msg, err := c.ReadMessage(context.Background(), ReadOptions{UID: uid, Peek: true})
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if msg.BodySource != "html" {
		t.Errorf("BodySource = %q, want html", msg.BodySource)
	}
	mustContain(t, msg.TextBody, "Order shipped", "Track it here (https://example.com/t/1).")
	if strings.Contains(msg.TextBody, "<") || strings.Contains(msg.TextBody, "alert") || strings.Contains(msg.TextBody, "color") {
		t.Errorf("TextBody leaked markup, script, or style: %q", msg.TextBody)
	}
}

func TestClientMarkReportsAffectedAndStaleUIDs(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	uid1 := m.append("INBOX", rawMessage("a@example.com", "b@example.com", "one", "1"))
	uid2 := m.append("INBOX", rawMessage("a@example.com", "b@example.com", "two", "2"))
	stale := uid2 + 500

	res, err := c.MarkMessages(context.Background(), MarkAction{UIDs: []uint32{uid1, uid2, stale}, Flag: "flagged", Add: true})
	if err != nil {
		t.Fatalf("MarkMessages: %v", err)
	}
	if fmtUIDs(res.Affected) != fmtUIDs([]uint32{uid1, uid2}) {
		t.Errorf("Affected = %v, want [%d %d]; stale %d must not count", res.Affected, uid1, uid2, stale)
	}
	if fmtUIDs(res.Requested) != fmtUIDs([]uint32{uid1, uid2, stale}) {
		t.Errorf("Requested = %v", res.Requested)
	}

	listed, err := c.SearchMessages(context.Background(), SearchOptions{Flagged: true})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if listed.TotalMatched != 2 {
		t.Errorf("flagged count = %d, want 2", listed.TotalMatched)
	}

	res, err = c.MarkMessages(context.Background(), MarkAction{UIDs: []uint32{uid1}, Flag: "flagged", Add: false})
	if err != nil {
		t.Fatalf("MarkMessages(remove): %v", err)
	}
	if fmtUIDs(res.Affected) != fmtUIDs([]uint32{uid1}) {
		t.Errorf("Affected after removal = %v", res.Affected)
	}

	if _, err := c.MarkMessages(context.Background(), MarkAction{UIDs: []uint32{uid1}, Flag: "deleted", Add: true}); err == nil {
		t.Error("an unsupported flag must be refused")
	}
}

func TestClientMoveReportsDestinationUIDs(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	uid1 := m.append("INBOX", rawMessage("a@example.com", "b@example.com", "one", "1"))
	uid2 := m.append("INBOX", rawMessage("a@example.com", "b@example.com", "two", "2"))

	res, err := c.MoveMessages(context.Background(), MoveOptions{UIDs: []uint32{uid1, uid2}, Destination: "Archive"})
	if err != nil {
		t.Fatalf("MoveMessages: %v", err)
	}
	if res.SourceFolder != "INBOX" || res.Destination != "Archive" {
		t.Errorf("folders = %q -> %q", res.SourceFolder, res.Destination)
	}
	if !res.DestUIDsKnown || len(res.DestUIDs) != 2 {
		t.Fatalf("DestUIDs = %v (known=%v), want 2 known UIDs from COPYUID", res.DestUIDs, res.DestUIDsKnown)
	}

	archived, err := c.ListMessages(context.Background(), ListOptions{Folder: "Archive"})
	if err != nil {
		t.Fatalf("ListMessages(Archive): %v", err)
	}
	if archived.TotalMatched != 2 {
		t.Errorf("Archive has %d, want 2", archived.TotalMatched)
	}
	inbox, err := c.ListMessages(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("ListMessages(INBOX): %v", err)
	}
	if inbox.TotalMatched != 0 {
		t.Errorf("INBOX has %d, want 0 after move", inbox.TotalMatched)
	}
	for _, dest := range res.DestUIDs {
		if _, err := c.ReadMessage(context.Background(), ReadOptions{Folder: "Archive", UID: dest, Peek: true}); err != nil {
			t.Errorf("reading moved uid %d in Archive: %v", dest, err)
		}
	}
}

func TestClientMoveToUnknownFolderListsFolders(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("packages")
	uid := m.append("INBOX", rawMessage("usps@example.com", "b@example.com", "Delivered", "1"))

	_, err := c.MoveMessages(context.Background(), MoveOptions{UIDs: []uint32{uid}, Destination: "packages/processed"})
	var cErr *ClientError
	if !errors.As(err, &cErr) || cErr.Kind != FailureFolderNotFound {
		t.Fatalf("error = %v, want folder_not_found", err)
	}
	mustContain(t, err.Error(), `"packages/processed"`, `"packages"`, "Archive", "Trash", "never create folders")
	if len(cErr.Available) < 5 {
		t.Errorf("Available = %v, want the account's folders", cErr.Available)
	}
}

func TestClientListFoldersReportsRolesAndCounts(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	m.append("INBOX", rawMessage("a@example.com", "b@example.com", "one", "1"))
	m.append("INBOX", rawMessage("a@example.com", "b@example.com", "two", "2"), imap.FlagSeen)
	m.append("Drafts", rawMessage("me@example.com", "b@example.com", "draft", "wip"), imap.FlagDraft)

	folders, err := c.ListFolders(context.Background())
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	byName := map[string]Folder{}
	for _, f := range folders {
		byName[f.Name] = f
	}
	inbox := byName["INBOX"]
	if inbox.Role != RoleInbox || inbox.Messages != 2 || inbox.Unseen != 1 || !inbox.Selectable {
		t.Errorf("INBOX = %+v", inbox)
	}
	if byName["Drafts"].Role != RoleDrafts || byName["Drafts"].Messages != 1 {
		t.Errorf("Drafts = %+v", byName["Drafts"])
	}
	if byName["Sent"].Role != RoleSent || byName["Trash"].Role != RoleTrash || byName["Archive"].Role != RoleArchive {
		t.Errorf("roles: Sent=%q Trash=%q Archive=%q", byName["Sent"].Role, byName["Trash"].Role, byName["Archive"].Role)
	}
	if byName["INBOX"].Delimiter != "/" {
		t.Errorf("Delimiter = %q, want /", byName["INBOX"].Delimiter)
	}
	if drafts, ok := FindFolderByRole(folders, RoleDrafts); !ok || drafts.Name != "Drafts" {
		t.Errorf("FindFolderByRole(drafts) = %+v, %v", drafts, ok)
	}
	for i := 1; i < len(folders); i++ {
		if folders[i-1].Name > folders[i].Name {
			t.Errorf("folders not sorted: %q before %q", folders[i-1].Name, folders[i].Name)
		}
	}
}

func TestClientListFoldersRev1FallbackStillCounts(t *testing.T) {
	m := newMemIMAP(t, func(o *memIMAPOptions) { o.rev1Only = true })
	c := m.newClient("primary")
	m.append("INBOX", rawMessage("a@example.com", "b@example.com", "one", "1"))

	folders, err := c.ListFolders(context.Background())
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	var inbox Folder
	for _, f := range folders {
		if f.Name == "INBOX" {
			inbox = f
		}
	}
	if inbox.Role != RoleInbox || inbox.Messages != 1 || inbox.Unseen != 1 {
		t.Errorf("INBOX via per-folder STATUS = %+v", inbox)
	}
}

func TestClientAppendMessageReturnsUIDAndKeepsFlags(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	raw := rawMessage("me@example.com", "bob@example.com", "Draft reply", "thinking...")

	res, err := c.AppendMessage(context.Background(), "Drafts", []byte(raw), []imap.Flag{imap.FlagDraft, imap.FlagSeen})
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if res.Folder != "Drafts" || res.UID == 0 || res.UIDValidity == 0 {
		t.Fatalf("AppendResult = %+v, want APPENDUID data", res)
	}
	msg, err := c.ReadMessage(context.Background(), ReadOptions{Folder: "Drafts", UID: res.UID, Peek: true})
	if err != nil {
		t.Fatalf("ReadMessage(draft): %v", err)
	}
	if msg.MessageID != messageIDFor("Draft reply") {
		t.Errorf("MessageID = %q", msg.MessageID)
	}
	hasDraft := false
	for _, f := range msg.Flags {
		if f == string(imap.FlagDraft) {
			hasDraft = true
		}
	}
	if !hasDraft {
		t.Errorf("flags = %v, want \\Draft", msg.Flags)
	}

	_, err = c.AppendMessage(context.Background(), "Nope", []byte(raw), nil)
	if FailureKindOf(err) != FailureFolderNotFound {
		t.Errorf("append to missing folder: %v (kind %q)", err, FailureKindOf(err))
	}
}

func TestClientMailboxStatus(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	m.append("INBOX", rawMessage("a@example.com", "b@example.com", "one", "1"))
	uid2 := m.append("INBOX", rawMessage("a@example.com", "b@example.com", "two", "2"), imap.FlagSeen)

	st, err := c.MailboxStatus(context.Background(), "")
	if err != nil {
		t.Fatalf("MailboxStatus: %v", err)
	}
	if st.Folder != "INBOX" || st.Messages != 2 || st.Unseen != 1 || st.UIDNext != uid2+1 || st.UIDValidity == 0 {
		t.Errorf("MailboxStatus = %+v", st)
	}
	if _, err := c.MailboxStatus(context.Background(), "Missing"); FailureKindOf(err) != FailureFolderNotFound {
		t.Errorf("status of missing folder: %v", err)
	}
}

func TestClientBadCredentialsClassifyAsAuthentication(t *testing.T) {
	m := newMemIMAP(t)
	cfg := m.imapConfig()
	cfg.Password = "wrong"
	c := NewClient("primary", cfg, nil)
	t.Cleanup(func() { _ = c.Close() })

	err := c.Ping(context.Background())
	if FailureKindOf(err) != FailureAuthentication {
		t.Fatalf("Ping with bad password: %v (kind %q), want authentication_failed", err, FailureKindOf(err))
	}
	mustContain(t, err.Error(), `"primary"`, "operator")
}

func TestClientCancellationReleasesAHungConnection(t *testing.T) {
	host, port := hungListener(t)
	noTLS := false
	c := NewClient("primary", IMAPConfig{Host: host, Port: port, Username: "a", Password: "b", TLS: &noTLS}, nil)
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.Ping(ctx)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Ping against a server that never greets must fail")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Ping held the account for %v; the context deadline must release it", elapsed)
	}
	kind := FailureKindOf(err)
	if kind != FailureCancelled && kind != FailureUnavailable {
		t.Errorf("kind = %q, want cancelled or unavailable (%v)", kind, err)
	}

	// The account is usable again once the server misbehaves less: the
	// mutex was released and the next call gets a fresh connection.
	m := newMemIMAP(t)
	c2 := m.newClient("primary")
	if err := c2.Ping(context.Background()); err != nil {
		t.Fatalf("Ping against a healthy server: %v", err)
	}
}

func TestClientReconnectsAfterConnectionLoss(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	// Drop the connection out from under the client.
	c.mu.Lock()
	_ = c.conn.Close()
	c.mu.Unlock()

	m.append("INBOX", rawMessage("a@example.com", "b@example.com", "after", "1"))
	got, err := c.ListMessages(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("ListMessages after connection loss: %v", err)
	}
	if got.TotalMatched != 1 {
		t.Errorf("TotalMatched = %d, want 1", got.TotalMatched)
	}
}
