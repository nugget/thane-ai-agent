package email

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
)

// TestWithdrawDraft pins email_draft_withdraw: with a trash role the
// draft moves there and its entry is withdrawn, even on an operator
// mailbox whose move_into allows only junk, because withdrawing is not
// filing; with no trash role it is refused and the draft stays open
// where it was.
func TestWithdrawDraft(t *testing.T) {
	tests := []struct {
		name      string
		noTrash   bool
		wantStage DraftStage
	}{
		{"trash role resolves", false, DraftStageWithdrawn},
		{"no trash role", true, DraftStageOpen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDraftFixture(t, func(a *AccountConfig) { a.Mailbox.Owner = "operator" })
			if tt.noTrash {
				f.mem.setFolderAttrs("Trash")
			}
			resp, _ := f.replyDraft(t, context.Background(), "Wrong answer", "Probably not.")
			out, err := f.svc.ToolProvider().HandleDraftWithdraw(context.Background(), map[string]any{"draft_id": resp.DraftID, "reason": "the question was withdrawn"})
			drafts, _ := f.drafts(t)
			e := f.entry(t, resp.DraftID)
			if e.Stage != tt.wantStage {
				t.Errorf("stage = %s, want %s", e.Stage, tt.wantStage)
			}
			if tt.noTrash {
				refusal := draftRefusalOf(t, err)
				if refusal.View.Reason != draftRefusedNoTrash {
					t.Errorf("refusal = %+v, want no_trash_folder", refusal.View)
				}
				mustContain(t, refusal.Message, "trash_folder", "still Thane's")
				if _, ok := drafts[resp.DraftUID]; !ok {
					t.Errorf("the draft left the drafts folder: %v", drafts)
				}
				return
			}
			if err != nil {
				t.Fatalf("withdraw: %v", err)
			}
			var view draftWithdrawnView
			if err := json.Unmarshal([]byte(out), &view); err != nil {
				t.Fatalf("withdraw result is not JSON: %v\n%s", err, out)
			}
			if view.Action != "withdrawn" || view.TrashFolder != "Trash" || !view.TrashUIDKnown || view.TrashUID == 0 {
				t.Errorf("result = %+v", view)
			}
			if _, ok := drafts[resp.DraftUID]; ok || !slices.Contains(subjectsIn(t, f.svc, "Trash"), "Re: Wrong answer") {
				t.Errorf("draft not moved to Trash: drafts %v, trash %v", drafts, subjectsIn(t, f.svc, "Trash"))
			}
			if e.ClosedReason != closedWithdrawn || e.WithdrawnTo == nil || e.WithdrawnTo.Folder != "Trash" || e.WithdrawnTo.Reason != "the question was withdrawn" {
				t.Errorf("entry = %+v", e)
			}
			_, err = f.revise(context.Background(), resp.DraftID, "Changed my mind.", "")
			if refusal := draftRefusalOf(t, err); refusal.View.Reason != draftRefusedWithdrawn {
				t.Errorf("revising a withdrawn draft = %+v, want withdrawn", refusal.View)
			}
		})
	}
}

// TestDraftToolsRefuseUnknownDraftIDs pins that the draft tools act
// only on the ledger: a draft_id it does not hold is refused, as is a
// missing one, and a draft Thane did not write can never be named.
func TestDraftToolsRefuseUnknownDraftIDs(t *testing.T) {
	f := newDraftFixture(t, nil)
	f.mem.append("Drafts", operatorDraft("Operator draft", ""))
	p := f.svc.ToolProvider()
	handlers := map[string]func(context.Context, map[string]any) (string, error){
		"email_draft_get":      p.HandleDraftGet,
		"email_draft_revise":   p.HandleDraftRevise,
		"email_draft_withdraw": p.HandleDraftWithdraw,
	}
	for name, handle := range handlers {
		t.Run(name, func(t *testing.T) {
			_, err := handle(context.Background(), map[string]any{"draft_id": "0192e0a0-0000-7000-8000-000000000000", "body": "x"})
			if err == nil {
				t.Fatal("an unknown draft_id was accepted")
			}
			mustContain(t, err.Error(), "not in the draft ledger", "email_drafts")
			if _, err := handle(context.Background(), map[string]any{"body": "x"}); err == nil || !strings.Contains(err.Error(), "draft_id is required") {
				t.Errorf("missing draft_id = %v", err)
			}
		})
	}
}

// TestDraftGetShowsTheDraftBesideItsOriginal pins email_draft_get: the
// header names the draft, the account's writes_as and voice, and the
// original; the draft's body and the original's follow, each after a
// separator line; the original is not marked seen; and an original that
// moved reads as not found, with where to look.
func TestDraftGetShowsTheDraftBesideItsOriginal(t *testing.T) {
	f := newDraftFixture(t, func(a *AccountConfig) { a.Mailbox.Voice = "brief; sign as Thane" })
	resp, original := f.replyDraft(t, context.Background(), "Guest list", "Two more guests are fine.")
	get := func() (draftGetHeader, []string) {
		t.Helper()
		out, err := f.svc.ToolProvider().HandleDraftGet(context.Background(), map[string]any{"draft_id": resp.DraftID})
		if err != nil {
			t.Fatalf("draft get: %v", err)
		}
		parts := strings.Split(out, bodySeparator)
		if len(parts) != 3 {
			t.Fatalf("result has %d sections, want header, draft, original:\n%s", len(parts), out)
		}
		var header draftGetHeader
		if err := json.Unmarshal([]byte(parts[0]), &header); err != nil {
			t.Fatalf("header is not JSON: %v\n%s", err, parts[0])
		}
		return header, parts
	}

	header, parts := get()
	if header.DraftID != resp.DraftID || header.Stage != DraftStageOpen || header.UID != resp.DraftUID || header.WritesAs != "Thane <thane@example.com>" || header.Voice != "brief; sign as Thane" || len(header.History) != 1 {
		t.Errorf("header = %+v", header)
	}
	if header.Original == nil || !header.Original.Found || header.Original.UID != original || header.Original.From == nil || header.Original.From.TrustZone != "trusted" {
		t.Errorf("original = %+v, want Alice's message found at uid %d", header.Original, original)
	}
	mustContain(t, parts[1], "Two more guests are fine.")
	mustContain(t, parts[2], "Can you confirm the plan for Saturday?")
	if seenFlag(t, f.svc, original) {
		t.Error("reading the draft beside its original marked the original seen")
	}

	if _, err := f.svc.ToolProvider().HandleMove(attendedCtx(), map[string]any{"uid": float64(original), "destination": "Archive"}); err != nil {
		t.Fatalf("move original: %v", err)
	}
	header, parts = get()
	if header.Original == nil || header.Original.Found {
		t.Errorf("original after the move = %+v, want found false", header.Original)
	}
	mustContain(t, parts[2], "email_search", header.Original.MessageID)
}

// TestDraftsListsTheLedger pins email_drafts' rows: open drafts by
// default, the rest with include_closed, each naming its original and
// who last wrote it.
func TestDraftsListsTheLedger(t *testing.T) {
	f := newDraftFixture(t, nil)
	reply, _ := f.replyDraft(t, loopCtx("email-owner-triage"), "Carpool", "I can drive.")
	out, err := f.svc.ToolProvider().HandleSend(context.Background(), sendArgs("alice@example.com"))
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	fresh := decodeSend(t, out)
	if _, err := f.svc.ToolProvider().HandleDraftWithdraw(context.Background(), map[string]any{"draft_id": fresh.DraftID}); err != nil {
		t.Fatalf("withdraw: %v", err)
	}

	open := f.listDrafts(t, map[string]any{"account": "primary"})
	if open.Count != 1 || open.Open != 1 || len(open.Drafts) != 1 {
		t.Fatalf("email_drafts = %+v, want the one open draft", open)
	}
	row := open.Drafts[0]
	if row.DraftID != reply.DraftID || row.Stage != DraftStageOpen || row.Subject != "Re: Carpool" || !slices.Equal(row.To, reply.To) || row.Revisions != 0 || row.LastRevised.By != "email-owner-triage" || row.LastRevised.At == "" {
		t.Errorf("row = %+v", row)
	}
	if row.Original == nil || row.Original.From != "alice@example.com" || row.Original.Subject != "Carpool" || row.Original.MessageID != messageIDFor("Carpool") {
		t.Errorf("row original = %+v", row.Original)
	}

	all := f.listDrafts(t, map[string]any{"include_closed": true})
	if all.Count != 2 || all.Open != 1 {
		t.Fatalf("with include_closed = %+v, want both", all)
	}
	for _, r := range all.Drafts {
		if r.DraftID == fresh.DraftID && (r.Stage != DraftStageWithdrawn || r.Original != nil) {
			t.Errorf("withdrawn row = %+v", r)
		}
	}
}

// TestThaneDraftAnnotation pins thane_draft: in the drafts folder, the
// rows email_list, email_search, and email_read return carry it only
// when their UID and Message-ID both match an open entry, so the
// operator's drafts never do, not even one that took over a recorded
// UID in a rebuilt folder; other folders are never annotated.
func TestThaneDraftAnnotation(t *testing.T) {
	f := newDraftFixture(t, nil)
	operatorUID := f.mem.append("Drafts", operatorDraft("Operator note", ""))
	resp, _ := f.replyDraft(t, context.Background(), "Firewood", "Two cords, please.")
	p := f.svc.ToolProvider()

	annotations := func(out string) map[uint32]string {
		t.Helper()
		var list listResponse
		if err := json.Unmarshal([]byte(out), &list); err != nil {
			t.Fatalf("list result is not JSON: %v", err)
		}
		got := map[uint32]string{}
		for _, m := range list.Messages {
			if m.ThaneDraft != nil {
				got[m.UID] = m.ThaneDraft.DraftID
			}
		}
		return got
	}
	want := map[uint32]string{resp.DraftUID: resp.DraftID}

	out, err := p.HandleList(context.Background(), map[string]any{"folder": "Drafts"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := annotations(out); !mapsEqual(got, want) {
		t.Errorf("email_list annotations = %v, want %v", got, want)
	}
	out, err = p.HandleSearch(context.Background(), map[string]any{"folder": "Drafts", "subject": "e"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := annotations(out); !mapsEqual(got, want) {
		t.Errorf("email_search annotations = %v, want %v", got, want)
	}
	out, err = p.HandleList(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("list INBOX: %v", err)
	}
	if got := annotations(out); len(got) != 0 {
		t.Errorf("INBOX annotations = %v, want none", got)
	}

	readRef := func(uid uint32) *thaneDraftRef {
		t.Helper()
		out, err := p.HandleRead(context.Background(), map[string]any{"uid": float64(uid), "folder": "Drafts"})
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		var header readResponse
		if err := json.Unmarshal([]byte(strings.SplitN(out, bodySeparator, 2)[0]), &header); err != nil {
			t.Fatalf("read header: %v", err)
		}
		return header.ThaneDraft
	}
	if ref := readRef(resp.DraftUID); ref == nil || ref.DraftID != resp.DraftID {
		t.Errorf("email_read of Thane's draft = %+v", ref)
	}
	if ref := readRef(operatorUID); ref != nil {
		t.Errorf("email_read of the operator's draft = %+v, want no thane_draft", ref)
	}

	f.mem.recreateFolder("Drafts")
	f.mem.append("Drafts", operatorDraft("After the rebuild", ""))
	if uid := f.mem.append("Drafts", operatorDraft("Second after the rebuild", "")); uid != resp.DraftUID {
		t.Fatalf("operator draft got uid %d, want the recorded uid %d", uid, resp.DraftUID)
	}
	out, err = p.HandleList(context.Background(), map[string]any{"folder": "Drafts"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := annotations(out); len(got) != 0 {
		t.Errorf("after the rebuild annotations = %v, want none: the operator's draft holds the recorded UID", got)
	}
}

func mapsEqual(a, b map[uint32]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestDraftIndexMatchesUIDAndMessageIDTogether pins the annotation's
// key directly: a UID alone never matches.
func TestDraftIndexMatchesUIDAndMessageIDTogether(t *testing.T) {
	idx := draftIndex{{UID: 7, MessageID: "abc@example.com"}: "draft-1"}
	tests := []struct {
		name      string
		uid       uint32
		messageID string
		want      string
	}{
		{"same uid and message id", 7, "abc@example.com", "draft-1"},
		{"bracketed message id", 7, "<abc@example.com>", "draft-1"},
		{"same uid, another message id", 7, "other@example.com", ""},
		{"same message id, another uid", 8, "abc@example.com", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ""
			if ref := idx.ref(tt.uid, tt.messageID); ref != nil {
				got = ref.DraftID
			}
			if got != tt.want {
				t.Errorf("ref(%d, %q) = %q, want %q", tt.uid, tt.messageID, got, tt.want)
			}
		})
	}
}

// TestReplyRefusesASecondDraftForTheSameMessage pins the duplicate
// refusal: while Thane's draft answering a message is open, another
// drafted reply to it is refused with route draft_open, naming that
// draft and email_draft_revise, and writes nothing; once the draft is
// withdrawn, a reply drafts again.
func TestReplyRefusesASecondDraftForTheSameMessage(t *testing.T) {
	f := newDraftFixture(t, nil)
	first, original := f.replyDraft(t, context.Background(), "Boiler", "Booked for Tuesday.")
	drafts, uidNext := f.drafts(t)
	reply := func() (string, error) {
		return f.svc.ToolProvider().HandleReply(context.Background(), map[string]any{"uid": float64(original), "body": "Booked for Wednesday."})
	}

	_, err := reply()
	refusal := refusalOf(t, err)
	if refusal.Decision.Route != RouteDraftOpen {
		t.Errorf("route = %s, want %s", refusal.Decision.Route, RouteDraftOpen)
	}
	mustContain(t, refusal.Message, first.DraftID, "email_draft_revise")
	if after, afterNext := f.drafts(t); len(after) != len(drafts) || afterNext != uidNext {
		t.Errorf("a refused reply wrote to the drafts folder: %v", after)
	}

	if _, err := f.svc.ToolProvider().HandleDraftWithdraw(context.Background(), map[string]any{"draft_id": first.DraftID}); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	out, err := reply()
	if err != nil {
		t.Fatalf("reply after withdrawing: %v", err)
	}
	if again := decodeSend(t, out); again.Disposition != DispositionDrafted || again.DraftID == first.DraftID {
		t.Errorf("reply after withdrawing = %+v, want a new draft", again)
	}
}

// TestReplyRefusesWhenTheOperatorStartedAReply pins the operator
// mailbox rule: a draft answering the same message that Thane did not
// write refuses a new drafted reply there, with route
// operator_reply_started; an assistant mailbox does not look.
func TestReplyRefusesWhenTheOperatorStartedAReply(t *testing.T) {
	tests := []struct {
		name          string
		owner         string
		operatorDraft bool
		wantRoute     string
	}{
		{"operator mailbox with the operator's reply started", "operator", true, RouteOperatorReplyStarted},
		{"operator mailbox with no reply started", "operator", false, ""},
		{"assistant mailbox does not look", "assistant", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newDraftFixture(t, func(a *AccountConfig) { a.Mailbox.Owner = tt.owner })
			original := f.deliverOriginal("Roof leak")
			if tt.operatorDraft {
				f.mem.append("Drafts", operatorDraft("Re: Roof leak", messageIDFor("Roof leak")), imap.FlagDraft)
			}
			drafts, uidNext := f.drafts(t)
			out, err := f.svc.ToolProvider().HandleReply(context.Background(), map[string]any{"uid": float64(original), "body": "A roofer comes Monday."})
			if tt.wantRoute == "" {
				if err != nil {
					t.Fatalf("reply: %v", err)
				}
				if resp := decodeSend(t, out); resp.Disposition != DispositionDrafted || resp.DraftID == "" {
					t.Errorf("reply = %+v, want drafted", resp)
				}
				return
			}
			refusal := refusalOf(t, err)
			if refusal.Decision.Route != tt.wantRoute {
				t.Errorf("route = %s, want %s", refusal.Decision.Route, tt.wantRoute)
			}
			mustContain(t, refusal.Message, "not one of Thane's open drafts", "Drafts")
			if after, afterNext := f.drafts(t); len(after) != len(drafts) || afterNext != uidNext {
				t.Errorf("a refused reply wrote to the drafts folder: %v", after)
			}
		})
	}
}

// TestDraftToolsAreCataloguedBehindTheirTag pins the registration: every
// draft tool is in the tool catalog under the email_drafts tag, and only
// that tag, which is a builtin leaf; and every description teaches the
// ownership rule.
func TestDraftToolsAreCataloguedBehindTheirTag(t *testing.T) {
	if spec, ok := toolcatalog.LookupBuiltinTagSpec("email_drafts"); !ok || spec.Kind.IsMenu() || spec.Description == "" {
		t.Fatalf("email_drafts tag spec = %+v ok=%v", spec, ok)
	}
	var names []string
	for _, tool := range (&Tools{}).draftToolDefinitions() {
		names = append(names, tool.Name)
		spec, ok := toolcatalog.LookupBuiltinToolSpec(tool.Name)
		if !ok || !slices.Equal(spec.Tags, []string{"email_drafts"}) {
			t.Errorf("%s catalog spec = %+v ok=%v, want tag email_drafts", tool.Name, spec, ok)
		}
		if tool.Handler == nil {
			t.Errorf("%s has no handler", tool.Name)
		}
		if tool.Name != "email_draft_revise" && tool.Name != "email_draft_withdraw" && !strings.Contains(tool.Description, draftOwnershipRule) {
			t.Errorf("%s does not state the ownership rule", tool.Name)
		}
	}
	slices.Sort(names)
	if want := []string{"email_draft_get", "email_draft_revise", "email_draft_withdraw", "email_drafts"}; !slices.Equal(names, want) {
		t.Errorf("draft tools = %v, want %v", names, want)
	}
	for _, tool := range (&Tools{}).draftToolDefinitions() {
		if tool.Name == "email_drafts" {
			mustContain(t, tool.Description, "email_draft_get", "email_draft_revise", "writes_as and voice", "email_draft_withdraw")
		}
	}
}
