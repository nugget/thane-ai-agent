package email

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// moveTo moves one message the way the operator's client does, and
// returns its UID in dest as the server's COPYUID names it. Unlike move
// (draft_harness_test.go), it fails the test on any error.
func (o *operatorClient) moveTo(folder string, uid uint32, dest string) uint32 {
	o.t.Helper()
	if _, err := o.c.Select(folder, nil).Wait(); err != nil {
		o.t.Fatalf("operator select %s: %v", folder, err)
	}
	set := imap.UIDSet{}
	set.AddNum(imap.UID(uid))
	data, err := o.c.Move(set, dest).Wait()
	if err != nil || data == nil {
		o.t.Fatalf("operator move to %s: %v", dest, err)
	}
	destSet, ok := data.DestUIDs.(imap.UIDSet)
	if !ok {
		o.t.Fatalf("operator move to %s: no COPYUID", dest)
	}
	nums, ok := destSet.Nums()
	if !ok || len(nums) != 1 {
		o.t.Fatalf("operator move to %s: COPYUID names %v", dest, destSet)
	}
	return uint32(nums[0])
}

// listRowsIn lists one folder of the primary account, keyed by UID.
func listRowsIn(t *testing.T, svc *Service, folder string) map[uint32]messageSummaryView {
	t.Helper()
	out, err := svc.ToolProvider().HandleList(context.Background(), map[string]any{"folder": folder})
	if err != nil {
		t.Fatalf("HandleList %s: %v", folder, err)
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

// markFlag runs email_mark flag flagged on one message and decodes the
// result.
func markFlag(t *testing.T, svc *Service, folder string, uid uint32, add bool) markResponse {
	t.Helper()
	out, err := svc.ToolProvider().HandleMark(context.Background(), markArgs(uid, "flag", "flagged", "add", add, "folder", folder))
	if err != nil {
		t.Fatalf("HandleMark: %v", err)
	}
	var resp markResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, out)
	}
	return resp
}

// TestLabelMarksKeyNeverCollides pins that the record key encodes the
// account and Message-ID unambiguously: account "a/b" with Message-ID "c"
// and account "a" with Message-ID "b/c" have separate records, and a
// record naming another pair is never read as this pair's.
func TestLabelMarksKeyNeverCollides(t *testing.T) {
	state := testOpstate(t)
	if labelMarksKey("a/b", "c") == labelMarksKey("a", "b/c") {
		t.Fatal("account a/b with message c and account a with message b/c share a key")
	}
	theirs, err := loadLabelMarks(state, "a/b", "c")
	if err != nil {
		t.Fatal(err)
	}
	theirs.bind(newMessageCopy("INBOX", 1, 1))
	theirs.addKeyword("thane-contact")
	theirs.setColor("blue", []imap.Flag{colorBit2})
	if err := saveLabelMarks(state, &theirs); err != nil {
		t.Fatal(err)
	}
	mine, err := loadLabelMarks(state, "a", "b/c")
	if err != nil || mine.claims() || !mine.empty() {
		t.Fatalf("account a, message b/c reads %+v, %v; want an empty record", mine, err)
	}

	// A record stored under this pair's key that names another pair, as a
	// collision would, is refused rather than claimed.
	raw, err := json.Marshal(theirs)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Set(labelMarksNamespace, labelMarksKey("a", "b/c"), string(raw)); err != nil {
		t.Fatal(err)
	}
	if got, err := loadLabelMarks(state, "a", "b/c"); err == nil || got.claims() {
		t.Errorf("a record naming account a/b, message c, was read as account a, message b/c: %+v, %v", got, err)
	}
	if got, err := loadLabelMarks(state, "a/b", "c"); err != nil || !got.SetFlagged {
		t.Errorf("account a/b lost its own record: %+v, %v", got, err)
	}
}

// TestByteIdenticalCopiesClaimOnlyTheMarkedOne pins two copies of one
// message in INBOX, byte for byte the same, so they share Message-ID and
// size: Go labels the first, and the operator's flag on the second, in
// the very colour Go wrote, is never stripped from its wake, shown as
// flag_label, or cleared.
func TestByteIdenticalCopiesClaimOnlyTheMarkedOne(t *testing.T) {
	mem, state := newMemIMAP(t), testOpstate(t)
	svc, fail, delivered, _ := labelServiceOn(t, mem, state, map[string]LabelConfig{"contact": contactLabel}, nil)
	poll(t, svc)
	raw := rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi")
	first, second := mem.append("INBOX", raw), mem.append("INBOX", raw)
	fail.Store(true)
	_, _ = svc.CheckNewMessages(context.Background())
	checkFlags(t, flagsOf(t, svc, "INBOX", first), thaneBlue, nil)
	checkFlags(t, flagsOf(t, svc, "INBOX", second), nil, thaneBlue)
	mem.operator().setFlags("INBOX", second, imap.StoreFlagsAdd, imapFlags(thaneBlue...)...)

	fail.Store(false)
	poll(t, svc)
	envs := delivered()
	checkWake(t, wakeFlagsFor(t, envs, first), nil, thaneBlue)
	checkWake(t, wakeFlagsFor(t, envs, second), thaneBlue, nil)

	rows := listRows(t, svc)
	if rows[first].Size != rows[second].Size || rows[first].MessageID != rows[second].MessageID {
		t.Fatalf("the copies differ in size or Message-ID: %+v / %+v", rows[first], rows[second])
	}
	if rows[first].FlagLabel != "contact" || rows[second].FlagLabel != "" {
		t.Errorf("list flag_label = %q / %q, want contact on the marked copy only", rows[first].FlagLabel, rows[second].FlagLabel)
	}
	out, err := svc.ToolProvider().HandleSearch(context.Background(), map[string]any{"label": "contact"})
	if err != nil {
		t.Fatalf("HandleSearch: %v", err)
	}
	var found listResponse
	if err := json.Unmarshal([]byte(out), &found); err != nil {
		t.Fatalf("search is not JSON: %v", err)
	}
	for _, m := range found.Messages {
		if want := map[uint32]string{first: "contact"}[m.UID]; m.FlagLabel != want {
			t.Errorf("search uid %d flag_label = %q, want %q", m.UID, m.FlagLabel, want)
		}
	}

	if resp := markFlag(t, svc, "INBOX", second, true); len(resp.ThaneColorCleared) != 0 {
		t.Errorf("flagging the operator's copy cleared %v", resp.ThaneColorCleared)
	}
	checkFlags(t, flagsOf(t, svc, "INBOX", second), thaneBlue, nil)
	if resp := markFlag(t, svc, "INBOX", second, false); len(resp.ThaneColorCleared) != 0 {
		t.Errorf("unflagging the operator's copy cleared %v", resp.ThaneColorCleared)
	}
	checkFlags(t, flagsOf(t, svc, "INBOX", second), []string{"thane-contact", colorBit2}, []string{flagFlag})
	if m := marksFor(t, svc, "Hello"); !m.SetFlagged || m.Copy.UID != first {
		t.Errorf("record = %+v, want it to keep claiming the flag on uid %d", m, first)
	}
}

// TestByteIdenticalCopiesLabelRemoval pins email_mark removing a model
// label from the copy Thane did not mark, when the operator gave it the
// same marks: refused, and nothing changes on it.
func TestByteIdenticalCopiesLabelRemoval(t *testing.T) {
	svc, mem, _ := labelService(t, map[string]LabelConfig{"waiting": waitingLabel}, nil)
	raw := rawMessage("Stranger <stranger@example.com>", "thane@example.com", "Hello", "hi")
	first, second := mem.append("INBOX", raw), mem.append("INBOX", raw)
	tools := svc.ToolProvider()
	if _, err := tools.HandleMark(context.Background(), markArgs(first, "label", "waiting")); err != nil {
		t.Fatalf("label: %v", err)
	}
	yellow := []string{"thane-waiting", flagFlag, colorBit1}
	mem.operator().setFlags("INBOX", second, imap.StoreFlagsAdd, imapFlags(yellow...)...)

	for _, add := range []bool{false, true} {
		out, err := tools.HandleMark(context.Background(), markArgs(second, "label", "waiting", "add", add))
		if err != nil {
			t.Fatalf("add %v: HandleMark: %v", add, err)
		}
		var resp labelMarkResponse
		if err := json.Unmarshal([]byte(out), &resp); err != nil {
			t.Fatalf("result is not JSON: %v", err)
		}
		if resp.Action != "refused" || len(resp.Refused) != 1 || !strings.Contains(resp.Refused[0].Reason, "describes another copy of the message") {
			t.Errorf("add %v on the operator's copy = %s, want it refused as another copy", add, out)
		}
		checkFlags(t, flagsOf(t, svc, "INBOX", second), yellow, nil)
	}
	if _, err := tools.HandleMark(context.Background(), markArgs(first, "label", "waiting", "add", false)); err != nil {
		t.Fatalf("remove from the marked copy: %v", err)
	}
	checkFlags(t, flagsOf(t, svc, "INBOX", first), nil, yellow)
	checkFlags(t, flagsOf(t, svc, "INBOX", second), yellow, nil)
}

// TestThaneMoveCarriesTheClaim pins email_move carrying Thane's claim to
// the copy the server's COPYUID names: in the destination the flag still
// shows flag_label, moved back into INBOX its wake leaves Thane's marks
// out, and flagging it there clears Thane's colour.
func TestThaneMoveCarriesTheClaim(t *testing.T) {
	svc, mem, delivered := labelService(t, map[string]LabelConfig{"contact": contactLabel}, func(a *AccountConfig) {
		a.Mailbox.MoveInto = []string{"*"}
	})
	poll(t, svc)
	uid := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi"))
	poll(t, svc)
	tools := svc.ToolProvider()

	archived := moveOne(t, tools, "INBOX", uid, "archive")
	if m := marksFor(t, svc, "Hello"); m.Copy.Folder != "Archive" || m.Copy.UID != archived {
		t.Fatalf("record copy = %+v, want Archive uid %d", m.Copy, archived)
	}
	if got := listRowsIn(t, svc, "Archive")[archived].FlagLabel; got != "contact" {
		t.Errorf("Archive flag_label = %q, want contact", got)
	}

	back := moveOne(t, tools, "Archive", archived, "inbox")
	poll(t, svc)
	checkWake(t, wakeFlagsFor(t, delivered(), back), nil, thaneBlue)
	if got := listRows(t, svc)[back].FlagLabel; got != "contact" {
		t.Errorf("INBOX flag_label after moving back = %q, want contact", got)
	}
	if resp := markFlag(t, svc, "INBOX", back, true); !slices.Contains(resp.ThaneColorCleared, back) {
		t.Errorf("flagging the moved-back copy cleared %v, want uid %d", resp.ThaneColorCleared, back)
	}
	checkFlags(t, flagsOf(t, svc, "INBOX", back), []string{flagFlag, "thane-contact"}, []string{colorBit2})
}

// moveOne runs email_move on one message to a folder role and returns its
// destination UID, failing unless the server confirmed it.
func moveOne(t *testing.T, tools *Tools, folder string, uid uint32, role string) uint32 {
	t.Helper()
	out, err := tools.HandleMove(context.Background(), map[string]any{"uid": float64(uid), "folder": folder, "destination_role": role})
	if err != nil {
		t.Fatalf("HandleMove to %s: %v", role, err)
	}
	var resp moveResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("move result is not JSON: %v", err)
	}
	if !resp.DestinationUIDsKnown || len(resp.DestinationUIDs) != 1 {
		t.Fatalf("move result = %s, want one confirmed destination UID", out)
	}
	return resp.DestinationUIDs[0]
}

// TestOperatorMoveLapsesTheClaim pins a message the operator moves in
// their own client: Thane's claim does not follow it, so in the
// destination its flag has no flag_label and flagging it clears nothing,
// and moved back into INBOX its wake shows every mark as the operator's
// and Go writes nothing on it.
func TestOperatorMoveLapsesTheClaim(t *testing.T) {
	svc, mem, delivered := labelService(t, map[string]LabelConfig{"contact": contactLabel}, nil)
	poll(t, svc)
	uid := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi"))
	poll(t, svc)
	op := mem.operator()

	archived := op.moveTo("INBOX", uid, "Archive")
	if got := listRowsIn(t, svc, "Archive")[archived].FlagLabel; got != "" {
		t.Errorf("Archive flag_label = %q after the operator's move, want none", got)
	}
	if resp := markFlag(t, svc, "Archive", archived, true); len(resp.ThaneColorCleared) != 0 {
		t.Errorf("flagging the operator's moved copy cleared %v", resp.ThaneColorCleared)
	}
	checkFlags(t, flagsOf(t, svc, "Archive", archived), thaneBlue, nil)

	back := op.moveTo("Archive", archived, "INBOX")
	poll(t, svc)
	checkWake(t, wakeFlagsFor(t, delivered(), back), thaneBlue, nil)
	if got := listRows(t, svc)[back].FlagLabel; got != "" {
		t.Errorf("INBOX flag_label = %q after the operator moved it back, want none", got)
	}
	if m := marksFor(t, svc, "Hello"); m.Copy.UID != uid || m.Copy.Folder != DefaultFolder {
		t.Errorf("record copy = %+v, want it left on the copy Thane marked, uid %d", m.Copy, uid)
	}
}

// TestMoveWithoutCopyUIDLetsTheClaimLapse pins carryLabelClaims: a move
// the server confirmed with COPYUID carries the claim to the copy it
// names; one it did not confirm, one onto a folder with no UIDVALIDITY,
// and a record describing another copy carry nothing.
func TestMoveWithoutCopyUIDLetsTheClaimLapse(t *testing.T) {
	marked := newMessageCopy("INBOX", 7, 5)
	tests := []struct {
		name   string
		copy   messageCopy
		result MoveResult
		want   messageCopy
	}{
		{"COPYUID names the new copy", marked,
			MoveResult{SourceFolder: "INBOX", SourceUIDValidity: 7, Destination: "Archive", UIDs: []uint32{5}, DestUIDs: []uint32{40}, DestUIDValidity: 9, DestUIDsKnown: true},
			newMessageCopy("Archive", 9, 40)},
		{"no COPYUID", marked,
			MoveResult{SourceFolder: "INBOX", SourceUIDValidity: 7, Destination: "Archive", UIDs: []uint32{5}},
			marked},
		{"destination UIDs the server did not confirm", marked,
			MoveResult{SourceFolder: "INBOX", SourceUIDValidity: 7, Destination: "Archive", UIDs: []uint32{5}, DestUIDs: []uint32{40}, DestUIDValidity: 9},
			marked},
		{"a destination with no UIDVALIDITY", marked,
			MoveResult{SourceFolder: "INBOX", SourceUIDValidity: 7, Destination: "Archive", UIDs: []uint32{5}, DestUIDs: []uint32{40}, DestUIDsKnown: true},
			marked},
		{"the record describes another copy", newMessageCopy("INBOX", 7, 6),
			MoveResult{SourceFolder: "INBOX", SourceUIDValidity: 7, Destination: "Archive", UIDs: []uint32{5}, DestUIDs: []uint32{40}, DestUIDValidity: 9, DestUIDsKnown: true},
			newMessageCopy("INBOX", 7, 6)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _ := labelService(t, map[string]LabelConfig{"contact": contactLabel}, nil)
			m, err := loadLabelMarks(svc.state, "primary", messageIDFor("Hello"))
			if err != nil {
				t.Fatal(err)
			}
			m.bind(tt.copy)
			m.addKeyword("thane-contact")
			if err := saveLabelMarks(svc.state, &m); err != nil {
				t.Fatal(err)
			}
			senders := map[uint32]moveSender{5: {envelope: Envelope{UID: 5, MessageID: messageIDFor("Hello")}}}
			svc.carryLabelClaims(context.Background(), "primary", tt.result, senders)
			if got := marksFor(t, svc, "Hello"); got.Copy != tt.want || !got.hasKeyword("thane-contact") {
				t.Errorf("record = %+v, want copy %+v with its keyword", got, tt.want)
			}
		})
	}
}

// TestMoveOfTwoCopiesLetsTheClaimLapse pins carryLabelClaims against
// go-imap's sorted COPYUID sets: the result below pairs 3 with 10 and 5
// with 11 whatever the server said, so when a move takes two copies that
// may share the marked copy's Message-ID the claim lands on neither. A
// copy moved beside a message under another Message-ID still carries it.
func TestMoveOfTwoCopiesLetsTheClaimLapse(t *testing.T) {
	marked := newMessageCopy("INBOX", 7, 3)
	pair := MoveResult{SourceFolder: "INBOX", SourceUIDValidity: 7, Destination: "Archive", UIDs: []uint32{3, 5}, DestUIDs: []uint32{10, 11}, DestUIDValidity: 9, DestUIDsKnown: true}
	lone := MoveResult{SourceFolder: "INBOX", SourceUIDValidity: 7, Destination: "Archive", UIDs: []uint32{3}, DestUIDs: []uint32{10}, DestUIDValidity: 9, DestUIDsKnown: true}
	id := normalizeMessageID(messageIDFor("Hello"))
	tests := []struct {
		name    string
		result  MoveResult
		senders map[uint32]string
		want    messageCopy
	}{
		{"two copies under one Message-ID", pair, map[uint32]string{3: id, 5: id}, marked},
		{"one Message-ID spelled with and without brackets", pair, map[uint32]string{3: "<" + id + ">", 5: id}, marked},
		{"a moved message whose envelope was not read", pair, map[uint32]string{3: id}, marked},
		{"beside a message under another Message-ID", pair, map[uint32]string{3: id, 5: "other@example.com"}, newMessageCopy("Archive", 9, 10)},
		{"the marked copy alone", lone, map[uint32]string{3: id}, newMessageCopy("Archive", 9, 10)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _ := labelService(t, map[string]LabelConfig{"contact": contactLabel}, nil)
			m, err := loadLabelMarks(svc.state, "primary", id)
			if err != nil {
				t.Fatal(err)
			}
			m.bind(marked)
			m.addKeyword("thane-contact")
			m.setColor("blue", []imap.Flag{colorBit2})
			if err := saveLabelMarks(svc.state, &m); err != nil {
				t.Fatal(err)
			}
			senders := make(map[uint32]moveSender, len(tt.senders))
			for uid, messageID := range tt.senders {
				senders[uid] = moveSender{envelope: Envelope{UID: uid, MessageID: messageID}}
			}
			svc.carryLabelClaims(context.Background(), "primary", tt.result, senders)
			if got := marksFor(t, svc, "Hello"); got.Copy != tt.want || !got.hasKeyword("thane-contact") {
				t.Errorf("record = %+v, want copy %+v with its keyword", got, tt.want)
			}
		})
	}
}
