package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// The draft tests need two things the harness lacked: a second IMAP
// session acting as the operator's own mail client, and a way to act in
// the middle of Thane's own session, right after an APPEND and before
// Thane sees the reply, which is the window a revision must survive.

// Append answers an APPEND, then runs the server's one-shot append hook.
func (s *specialUseSession) Append(mailbox string, r imap.LiteralReader, options *imap.AppendOptions) (*imap.AppendData, error) {
	data, err := s.Session.Append(mailbox, r, options)
	s.mem.mu.Lock()
	hook := s.mem.appendHook
	s.mem.appendHook = nil
	s.mem.mu.Unlock()
	if err == nil && hook != nil {
		hook(mailbox, data.UID)
	}
	return data, err
}

// onNextAppend runs hook once, after the next APPEND any session makes.
func (m *memIMAP) onNextAppend(hook func(folder string, uid imap.UID)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendHook = hook
}

// Fetch answers a FETCH, then runs the server's one-shot fetch hook
// before the command completes, so a test can act between a check Thane
// reads and the change it makes next.
func (s *specialUseSession) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	err := s.Session.Fetch(w, numSet, options)
	s.mem.mu.Lock()
	hook := s.mem.fetchHook
	s.mem.fetchHook = nil
	s.mem.mu.Unlock()
	if err == nil && hook != nil {
		hook()
	}
	return err
}

// onNextFetch runs hook once, after the next FETCH any session makes.
func (m *memIMAP) onNextFetch(hook func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fetchHook = hook
}

// onNextMove runs hook once in place of the next MOVE any session makes,
// which then answers a bare OK with no COPYUID and no EXPUNGE: the
// answer of a server that moved the message and did not say where. The
// hook makes the move itself, through another session.
func (m *memIMAP) onNextMove(hook func() error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.moveHook = hook
}

// divertMove answers a MOVE the in-memory server would get wrong, and
// reports whether it did. A pending onNextMove hook runs in its place.
// A MOVE to an existing folder whose set names no message answers a
// bare OK, as a real server does: imapmemserver would write a COPYUID
// with empty UID sets there, which is not valid syntax and which go-imap
// cannot parse. A MOVE to a folder that does not exist is left to
// imapmemserver, which refuses it.
func (s *specialUseSession) divertMove(numSet imap.NumSet, dest string) (bool, error) {
	s.mem.mu.Lock()
	hook := s.mem.moveHook
	s.mem.moveHook = nil
	s.mem.mu.Unlock()
	if hook != nil {
		return true, hook()
	}
	uids, ok := numSet.(imap.UIDSet)
	if !ok {
		return false, nil
	}
	if _, err := s.Status(dest, &imap.StatusOptions{}); err != nil {
		return false, nil
	}
	// imapmemserver's Search rewrites the set's ranges in place and reads
	// its options without a nil check, so it gets a copy and options.
	criteria := &imap.SearchCriteria{UID: []imap.UIDSet{append(imap.UIDSet(nil), uids...)}}
	found, err := s.Search(imapserver.NumKindUID, criteria, &imap.SearchOptions{})
	if err != nil {
		return true, err
	}
	return len(found.AllUIDs()) == 0, nil
}

// onNextStore runs hook once, before the next STORE any session makes;
// an error it returns fails that STORE, as a dropped connection or a
// server NO would. The session's Store is in labels_harness_test.go.
func (m *memIMAP) onNextStore(hook func() error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextStoreHook = hook
}

// Expunge runs the server's one-shot expunge hook, then expunges.
func (s *specialUseSession) Expunge(w *imapserver.ExpungeWriter, uids *imap.UIDSet) error {
	s.mem.mu.Lock()
	hook := s.mem.expungeHook
	s.mem.expungeHook = nil
	s.mem.mu.Unlock()
	if hook != nil {
		hook()
	}
	return s.Session.Expunge(w, uids)
}

// onNextExpunge runs hook once, before the next EXPUNGE any session
// makes.
func (m *memIMAP) onNextExpunge(hook func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expungeHook = hook
}

// copyTo copies one message to dest, as a client files a sent draft in
// Sent. It reports errors rather than failing, so a server-side hook can
// call it.
func (o *operatorClient) copyTo(folder string, uid uint32, dest string) error {
	if _, err := o.c.Select(folder, nil).Wait(); err != nil {
		return fmt.Errorf("operator select %s: %w", folder, err)
	}
	set := imap.UIDSet{}
	set.AddNum(imap.UID(uid))
	if _, err := o.c.Copy(set, dest).Wait(); err != nil {
		return fmt.Errorf("operator copy to %s: %w", dest, err)
	}
	return nil
}

// appendDraft stores raw in folder as a draft, as a client saves one. It
// reports errors rather than failing, so a server-side hook can call it.
func (o *operatorClient) appendDraft(folder, raw string) error {
	cmd := o.c.Append(folder, int64(len(raw)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagDraft, imap.FlagSeen}})
	if _, err := cmd.Write([]byte(raw)); err != nil {
		return fmt.Errorf("operator append: %w", err)
	}
	if err := cmd.Close(); err != nil {
		return fmt.Errorf("operator append: %w", err)
	}
	if _, err := cmd.Wait(); err != nil {
		return fmt.Errorf("operator append: %w", err)
	}
	return nil
}

// actOnOld does to uid in Drafts what act names, from the operator's
// client, and returns the UID of the operator's edit when act is
// oldEdited.
func (o *operatorClient) actOnOld(uid uint32, act oldAction) uint32 {
	o.t.Helper()
	var err error
	switch act {
	case oldDiscarded:
		err = o.discard("Drafts", uid)
	case oldSent:
		err = o.send("Drafts", uid)
	case oldFlagged:
		err = o.flagDeleted("Drafts", uid)
	case oldEdited:
		return o.edit("Drafts", uid, operatorDraft("Re: Crash window", messageIDFor("Crash window")))
	}
	if err != nil {
		o.t.Fatal(err)
	}
	return 0
}

// move moves one message the way a client files it, with UID MOVE. It
// reports errors rather than failing, so a server-side hook can call it.
func (o *operatorClient) move(folder string, uid uint32, dest string) error {
	if _, err := o.c.Select(folder, nil).Wait(); err != nil {
		return fmt.Errorf("operator select %s: %w", folder, err)
	}
	set := imap.UIDSet{}
	set.AddNum(imap.UID(uid))
	if _, err := o.c.Move(set, dest).Wait(); err != nil {
		return fmt.Errorf("operator move: %w", err)
	}
	return nil
}

// send sends one draft the way a client does: a copy is filed in Sent
// and the draft is expunged. It reports errors rather than failing, so a
// server-side hook can call it.
func (o *operatorClient) send(folder string, uid uint32) error {
	if _, err := o.c.Select(folder, nil).Wait(); err != nil {
		return fmt.Errorf("operator select %s: %w", folder, err)
	}
	set := imap.UIDSet{}
	set.AddNum(imap.UID(uid))
	if _, err := o.c.Copy(set, "Sent").Wait(); err != nil {
		return fmt.Errorf("operator copy to Sent: %w", err)
	}
	return o.discard(folder, uid)
}

// onFetch runs hook after the nth FETCH from now, any session's.
func (m *memIMAP) onFetch(n int, hook func()) {
	if n <= 1 {
		m.onNextFetch(hook)
		return
	}
	m.onNextFetch(func() { m.onFetch(n-1, hook) })
}

// operatorClient is the operator's own mail client: a second session
// that edits and discards drafts behind Thane's back, the way a person's
// client does.
type operatorClient struct {
	t *testing.T
	c *imapclient.Client
}

func (m *memIMAP) operator() *operatorClient {
	m.t.Helper()
	c, err := imapclient.DialInsecure(net.JoinHostPort(m.host, strconv.Itoa(m.port)), nil)
	if err != nil {
		m.t.Fatalf("operator dial: %v", err)
	}
	if err := c.Login(testIMAPUser, testIMAPPass).Wait(); err != nil {
		m.t.Fatalf("operator login: %v", err)
	}
	m.t.Cleanup(func() { _ = c.Close() })
	return &operatorClient{t: m.t, c: c}
}

// discard expunges one message the way a client deletes a draft. It
// reports errors rather than failing, so a server-side hook can call it.
func (o *operatorClient) discard(folder string, uid uint32) error {
	if _, err := o.c.Select(folder, nil).Wait(); err != nil {
		return fmt.Errorf("operator select %s: %w", folder, err)
	}
	set := imap.UIDSet{}
	set.AddNum(imap.UID(uid))
	if err := o.c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		return fmt.Errorf("operator store: %w", err)
	}
	if err := o.c.UIDExpunge(set).Close(); err != nil {
		return fmt.Errorf("operator expunge: %w", err)
	}
	return nil
}

// flagDeleted marks one message \Deleted and leaves it unexpunged, the
// way a client that defers its expunge discards a draft. It reports
// errors rather than failing, so a server-side hook can call it.
func (o *operatorClient) flagDeleted(folder string, uid uint32) error {
	if _, err := o.c.Select(folder, nil).Wait(); err != nil {
		return fmt.Errorf("operator select %s: %w", folder, err)
	}
	set := imap.UIDSet{}
	set.AddNum(imap.UID(uid))
	if err := o.c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
		return fmt.Errorf("operator store: %w", err)
	}
	return nil
}

// edit saves an edited draft the way a client does: the new version is
// stored under a new UID and the old one removed. It returns the new UID.
func (o *operatorClient) edit(folder string, uid uint32, raw string) uint32 {
	o.t.Helper()
	cmd := o.c.Append(folder, int64(len(raw)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagDraft, imap.FlagSeen}})
	if _, err := cmd.Write([]byte(raw)); err != nil {
		o.t.Fatalf("operator append: %v", err)
	}
	if err := cmd.Close(); err != nil {
		o.t.Fatalf("operator append: %v", err)
	}
	data, err := cmd.Wait()
	if err != nil {
		o.t.Fatalf("operator append: %v", err)
	}
	if err := o.discard(folder, uid); err != nil {
		o.t.Fatal(err)
	}
	return uint32(data.UID)
}

// operatorDraft is a draft the operator wrote in their own client,
// answering inReplyTo when it is set.
func operatorDraft(subject, inReplyTo string) string {
	extra := []string{"X-Mailer: operator-client"}
	if inReplyTo != "" {
		extra = append(extra, "In-Reply-To: <"+inReplyTo+">")
	}
	return rawMessage("Thane <thane@example.com>", "alice@example.com", subject, "The operator's own words.", extra...)
}

// draftFixture is one drafts-delivery account over the in-memory server.
type draftFixture struct {
	svc *Service
	mem *memIMAP
}

func newDraftFixture(t *testing.T, tweak func(*AccountConfig)) draftFixture {
	t.Helper()
	svc, mem, _ := policyService(t, ServiceDependencies{Contacts: identityStub()}, func(cfg *Config) {
		cfg.Accounts[0].Policy.Delivery = DeliveryDrafts
		if tweak != nil {
			tweak(&cfg.Accounts[0])
		}
	})
	return draftFixture{svc: svc, mem: mem}
}

// loopCtx is an unattended turn in the named loop.
func loopCtx(name string) context.Context {
	return tools.WithHints(tools.WithLoopID(context.Background(), "loop-"+name), map[string]string{"loop_name": name})
}

// deliverOriginal files a message from Alice in INBOX and returns its UID.
func (f draftFixture) deliverOriginal(subject string) uint32 {
	return f.mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", subject, "Can you confirm the plan for Saturday?"))
}

// replyDraft drafts a reply to a fresh original and returns the result
// and the original's UID.
func (f draftFixture) replyDraft(t *testing.T, ctx context.Context, subject, body string) (sendResponse, uint32) {
	t.Helper()
	uid := f.deliverOriginal(subject)
	out, err := f.svc.ToolProvider().HandleReply(ctx, map[string]any{"uid": float64(uid), "body": body})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	resp := decodeSend(t, out)
	if resp.Disposition != DispositionDrafted || resp.DraftID == "" {
		t.Fatalf("reply = %+v, want a drafted reply with a draft_id", resp)
	}
	return resp, uid
}

// entry returns a ledger entry, failing when it is missing.
func (f draftFixture) entry(t *testing.T, id string) draftEntry {
	t.Helper()
	e, ok, err := f.svc.draftByID(id)
	if err != nil || !ok {
		t.Fatalf("ledger entry %s: ok=%v err=%v", id, ok, err)
	}
	return e
}

// drafts returns uid → Message-ID for everything in the drafts folder,
// and the folder's UIDNEXT, so a test can show a call wrote nothing.
func (f draftFixture) drafts(t *testing.T) (map[uint32]string, uint32) {
	t.Helper()
	acct, _ := f.svc.ResolveAccount(context.Background(), "primary")
	listed, err := acct.Client.ListMessages(context.Background(), ListOptions{Folder: "Drafts", Limit: MaxListLimit})
	if err != nil {
		t.Fatalf("list Drafts: %v", err)
	}
	out := make(map[uint32]string, len(listed.Envelopes))
	for _, env := range listed.Envelopes {
		out[env.UID] = env.MessageID
	}
	status, err := acct.Client.MailboxStatus(context.Background(), "Drafts")
	if err != nil {
		t.Fatalf("status Drafts: %v", err)
	}
	return out, status.UIDNext
}

// readDraft reads one drafts-folder message without marking it seen.
func (f draftFixture) readDraft(t *testing.T, uid uint32) *Message {
	t.Helper()
	acct, _ := f.svc.ResolveAccount(context.Background(), "primary")
	msg, err := acct.Client.ReadMessage(context.Background(), ReadOptions{Folder: "Drafts", UID: uid, Peek: true})
	if err != nil {
		t.Fatalf("read Drafts uid %d: %v", uid, err)
	}
	return msg
}

// revise calls email_draft_revise.
func (f draftFixture) revise(ctx context.Context, id, body, note string) (string, error) {
	return f.svc.ToolProvider().HandleDraftRevise(ctx, map[string]any{"draft_id": id, "body": body, "note": note})
}

// listDrafts calls email_drafts and decodes the result.
func (f draftFixture) listDrafts(t *testing.T, args map[string]any) draftsResponse {
	t.Helper()
	out, err := f.svc.ToolProvider().HandleDrafts(context.Background(), args)
	if err != nil {
		t.Fatalf("email_drafts: %v", err)
	}
	var resp draftsResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("email_drafts result is not JSON: %v\n%s", err, out)
	}
	return resp
}

// draftRefusalOf asserts err is a draft tool's refusal.
func draftRefusalOf(t *testing.T, err error) *draftRefusal {
	t.Helper()
	var refusal *draftRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error is not a draft refusal: %v", err)
	}
	return refusal
}
