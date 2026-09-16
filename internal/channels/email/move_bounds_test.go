package email

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/emersion/go-imap/v2"
)

// TestBatchIsCappedPerCall pins the per-call UID limit on email_move and
// email_mark: a batch over it is refused with the overage and how to
// split it, beside any other problem with the call, and nothing
// changes; a batch at the limit is accepted. It also pins the schema
// and the descriptions to the limits Go enforces.
func TestBatchIsCappedPerCall(t *testing.T) {
	svc, mem := filingService(t, identityStub(), true, func(a *AccountConfig) { a.DraftsFolder = "Drafts" })
	uid := mem.append("INBOX", rawMessage("Bob <bob@example.org>", "alice@example.org", "stays", "x"))
	batch := func(n int) []any {
		uids := make([]any, n)
		for i := range uids {
			uids[i] = float64(uid + uint32(i))
		}
		return uids
	}

	tests := []struct {
		name string
		call func(*Tools, context.Context, map[string]any) (string, error)
		args map[string]any
		want []string
	}{
		{"move over the limit", (*Tools).HandleMove, map[string]any{"uids": batch(maxBatchUIDs + 1), "destination": "Receipts"}, []string{"uids lists 101 messages, 1 over the 100 one email_move call takes: split them across calls of at most 100 each; nothing was moved"}},
		{"move over the limit beside another problem", (*Tools).HandleMove, map[string]any{"uids": batch(250)}, []string{"uids lists 250 messages, 150 over the 100", "destination or destination_role is required"}},
		{"mark over the limit", (*Tools).HandleMark, map[string]any{"uids": batch(250), "flag": "flagged"}, []string{"uids lists 250 messages, 150 over the 100 one email_mark call takes", "nothing was changed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.call(svc.ToolProvider(), attendedCtx(), tt.args)
			if err == nil {
				t.Fatal("the call must be refused")
			}
			mustContain(t, err.Error(), tt.want...)
			acct, _ := svc.ResolveAccount(context.Background(), "primary")
			listed, err := acct.Client.ListMessages(context.Background(), ListOptions{Folder: "INBOX"})
			if err != nil || len(listed.Envelopes) != 1 || slices.Contains(listed.Envelopes[0].Flags, string(imap.FlagFlagged)) {
				t.Errorf("INBOX after a refused batch = %+v, %v; the message must stay, unflagged", listed.Envelopes, err)
			}
		})
	}

	out, err := svc.ToolProvider().HandleMark(attendedCtx(), map[string]any{"uids": batch(maxBatchUIDs), "flag": "flagged"})
	if err != nil {
		t.Fatalf("a batch at the limit: %v", err)
	}
	var mark markResponse
	if err := json.Unmarshal([]byte(out), &mark); err != nil || len(mark.UIDsAffected) != 1 || len(mark.UIDsNotFound) != maxBatchUIDs-1 {
		t.Errorf("mark at the limit = %s, %v", out, err)
	}

	for _, tool := range svc.ToolProvider().Tools() {
		if tool.Name != "email_move" && tool.Name != "email_mark" {
			continue
		}
		uids := tool.Parameters["properties"].(map[string]any)["uids"].(map[string]any)
		if uids["maxItems"] != maxBatchUIDs {
			t.Errorf("%s uids maxItems = %v, want %d", tool.Name, uids["maxItems"], maxBatchUIDs)
		}
		mustContain(t, uids["description"].(string), fmt.Sprintf("at most %d per call", maxBatchUIDs))
		mustContain(t, tool.Description, fmt.Sprintf("A call takes at most %d UIDs", maxBatchUIDs))
		if tool.Name == "email_move" {
			mustContain(t, tool.Description, fmt.Sprintf("within %d KB", maxMoveOutput/1024), fmt.Sprintf("over %d bytes are cut and end with %s", maxMoveFieldOutput, fieldCutMarker), "moved_omitted", "refused_omitted")
		}
	}
}

// TestMoveResultClipsOversizedHeaders pins the per-field bound on a
// real move: a sender and a Message-ID past it are cut on a rune
// boundary and marked, and the UIDs stay whole.
func TestMoveResultClipsOversizedHeaders(t *testing.T) {
	svc, mem := filingService(t, identityStub(), true, func(a *AccountConfig) { a.DraftsFolder = "Drafts" })
	raw := "From: " + strings.Repeat("N", 600) + " <bob@example.org>\r\n" +
		"To: alice@example.org\r\n" +
		"Subject: long headers\r\n" +
		"Message-ID: <" + strings.Repeat("m", 600) + "@example.org>\r\n" +
		"Date: Mon, 01 Sep 2026 10:00:00 +0000\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nx\r\n"
	uid := mem.append("INBOX", raw)
	out, err := svc.ToolProvider().HandleMove(attendedCtx(), map[string]any{"uid": float64(uid), "destination": "Receipts"})
	if err != nil {
		t.Fatalf("HandleMove: %v", err)
	}
	resp := decodeMove(t, out)
	if len(resp.Moved) != 1 {
		t.Fatalf("moved = %+v", resp.Moved)
	}
	m := resp.Moved[0]
	for field, v := range map[string]string{"from": m.From, "message_id": m.MessageID} {
		if len(v) > maxMoveFieldOutput || !strings.HasSuffix(v, fieldCutMarker) {
			t.Errorf("%s = %d bytes %q, want at most %d ending %q", field, len(v), v, maxMoveFieldOutput, fieldCutMarker)
		}
	}
	if m.UID != uid || m.DestinationUID == 0 || !slices.Equal(resp.UIDs, []uint32{uid}) || !slices.Equal(resp.DestinationUIDs, []uint32{m.DestinationUID}) {
		t.Errorf("recovery UIDs = %+v", resp)
	}
}

// TestClipFieldCutsOnARune pins the clip: a value at the bound is left
// alone, and a longer one is cut to fit with the marker, never inside a
// multibyte character.
func TestClipFieldCutsOnARune(t *testing.T) {
	atBound := strings.Repeat("a", maxMoveFieldOutput)
	if got := clipField(atBound); got != atBound {
		t.Errorf("clipField at the bound changed the value to %q", got)
	}
	got := clipField(strings.Repeat("é", maxMoveFieldOutput))
	if len(got) > maxMoveFieldOutput || !utf8.ValidString(got) || !strings.HasSuffix(got, fieldCutMarker) {
		t.Errorf("clipField = %d bytes, valid %v: %q", len(got), utf8.ValidString(got), got)
	}
}

// TestMoveResultIsHeldTo16KB pins the move result's bound at the
// largest batch with every field at its largest: the result fits,
// moved sheds its detail before refused does, the entries that shed it
// are the last ones and are counted, and every recovery UID is present
// in order.
func TestMoveResultIsHeldTo16KB(t *testing.T) {
	tests := []struct {
		name           string
		moved, refused int
		wantOmitted    bool
	}{
		{"largest batch, all moved", maxBatchUIDs, 0, true},
		{"largest batch, all refused", 0, maxBatchUIDs, true},
		{"largest batch, half and half", maxBatchUIDs / 2, maxBatchUIDs / 2, true},
		{"a small batch sheds nothing", 3, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := maximalMove(tt.moved, tt.refused)
			out, err := marshalMoveResponse(in)
			if err != nil {
				t.Fatalf("marshalMoveResponse: %v", err)
			}
			if len(out) > maxMoveOutput {
				t.Errorf("move result = %d bytes, cap %d", len(out), maxMoveOutput)
			}
			resp := decodeMove(t, out)
			if !slices.Equal(resp.UIDs, in.UIDs) || !slices.Equal(resp.DestinationUIDs, in.DestinationUIDs) {
				t.Fatal("uids and destination_uids must be complete")
			}
			if len(resp.Moved) != tt.moved || len(resp.Refused) != tt.refused {
				t.Fatalf("moved %d, refused %d; every entry must stay", len(resp.Moved), len(resp.Refused))
			}
			for i, m := range resp.Moved {
				if m.UID != in.Moved[i].UID || m.DestinationUID != in.Moved[i].DestinationUID {
					t.Errorf("moved[%d] UIDs = %d→%d, want %d→%d", i, m.UID, m.DestinationUID, in.Moved[i].UID, in.Moved[i].DestinationUID)
				}
				checkDetail(t, fmt.Sprintf("moved[%d]", i), i < tt.moved-resp.MovedOmitted, m.From, m.MessageID, m.TrustZone)
			}
			for i, r := range resp.Refused {
				if r.UID != in.Refused[i].UID {
					t.Errorf("refused[%d] uid = %d, want %d", i, r.UID, in.Refused[i].UID)
				}
				checkDetail(t, fmt.Sprintf("refused[%d]", i), i < tt.refused-resp.RefusedOmitted, r.From, r.Reason, r.TrustZone, r.Recovery)
			}
			if omitted := resp.MovedOmitted+resp.RefusedOmitted > 0; omitted != tt.wantOmitted {
				t.Errorf("moved_omitted %d, refused_omitted %d; want shedding %v", resp.MovedOmitted, resp.RefusedOmitted, tt.wantOmitted)
			}
			if resp.RefusedOmitted > 0 && resp.MovedOmitted != tt.moved {
				t.Errorf("refused_omitted = %d with moved_omitted %d of %d; refused sheds only after every moved entry has", resp.RefusedOmitted, resp.MovedOmitted, tt.moved)
			}
			if !tt.wantOmitted && strings.Contains(out, "_omitted") {
				t.Errorf("a result that fits must not render the omitted counts: %s", out)
			}
		})
	}
}

// maximalMove builds a move result with the given entries, every text
// field far past its bound in multibyte characters, and the widest UIDs.
func maximalMove(moved, refused int) moveResponse {
	long := strings.Repeat("é", 2000)
	resp := moveResponse{Action: "moved", Account: "primary", SourceFolder: "INBOX", DestinationFolder: junkTestFolder, DestinationUIDsKnown: true, UIDs: []uint32{}, DestinationUIDs: []uint32{}, UIDsNotFound: []uint32{}, Moved: []movedMessage{}, Refused: []refusedMessage{}}
	if moved == 0 {
		resp.Action = "refused"
	}
	for i := range moved {
		src, dst := uint32(math.MaxUint32-i), uint32(math.MaxUint32-1000-i)
		resp.UIDs = append(resp.UIDs, src)
		resp.DestinationUIDs = append(resp.DestinationUIDs, dst)
		resp.Moved = append(resp.Moved, movedMessage{UID: src, DestinationUID: dst, MessageID: long + "@example.org", From: long + " <bob@example.org>", TrustZone: "unknown"})
	}
	for i := range refused {
		resp.Refused = append(resp.Refused, refusedMessage{UID: uint32(math.MaxUint32 - 5000 - i), From: long, TrustZone: "trusted", Reason: long, Recovery: junkRefusalRecovery})
	}
	return resp
}

// TestMoveResultClipsItsOwnFields pins the clip on the result's own
// strings: an account, a folder, or a note of any size is cut on a
// rune boundary and marked, the result stays within the cap, and every
// UID and entry stays, at the largest batch as at the smallest, even
// where JSON takes six bytes to escape each byte of every field.
func TestMoveResultClipsItsOwnFields(t *testing.T) {
	huge := strings.Repeat("é", 10*1024)
	escaped := strings.Repeat("<\x01", 1024)
	tests := []struct {
		name string
		in   moveResponse
	}{
		{"a 20 KB account name", withOwnFields(maximalMove(1, 0), huge, "INBOX", junkTestFolder, "")},
		{"a 20 KB source folder", withOwnFields(maximalMove(1, 0), "primary", huge, junkTestFolder, "")},
		{"a 20 KB destination folder and the note naming it", withOwnFields(maximalMove(1, 0), "primary", "INBOX", huge, "the server did not confirm which UIDs moved or their new UIDs; list "+huge+" to check")},
		{"the largest batch with every field at its largest", withOwnFields(maximalMove(maxBatchUIDs/2, maxBatchUIDs/2), huge, huge, huge, huge)},
		{"the largest batch with every byte escaped", withOwnFields(maximalMove(maxBatchUIDs, 0), escaped, escaped, escaped, escaped)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := marshalMoveResponse(tt.in)
			if err != nil {
				t.Fatalf("marshalMoveResponse: %v", err)
			}
			if len(out) > maxMoveOutput {
				t.Errorf("move result = %d bytes, cap %d", len(out), maxMoveOutput)
			}
			resp := decodeMove(t, out)
			if !slices.Equal(resp.UIDs, tt.in.UIDs) || !slices.Equal(resp.DestinationUIDs, tt.in.DestinationUIDs) || len(resp.Moved) != len(tt.in.Moved) || len(resp.Refused) != len(tt.in.Refused) {
				t.Fatalf("uids %d, destination_uids %d, moved %d, refused %d; every UID and entry must stay", len(resp.UIDs), len(resp.DestinationUIDs), len(resp.Moved), len(resp.Refused))
			}
			for i, m := range resp.Moved {
				if m.UID != tt.in.Moved[i].UID || m.DestinationUID != tt.in.Moved[i].DestinationUID {
					t.Errorf("moved[%d] UIDs = %d→%d, want %d→%d", i, m.UID, m.DestinationUID, tt.in.Moved[i].UID, tt.in.Moved[i].DestinationUID)
				}
			}
			for i, r := range resp.Refused {
				if r.UID != tt.in.Refused[i].UID {
					t.Errorf("refused[%d] uid = %d, want %d", i, r.UID, tt.in.Refused[i].UID)
				}
			}
			checkClipped(t, "account", resp.Account, tt.in.Account)
			checkClipped(t, "source_folder", resp.SourceFolder, tt.in.SourceFolder)
			checkClipped(t, "destination_folder", resp.DestinationFolder, tt.in.DestinationFolder)
			checkClipped(t, "note", resp.Note, tt.in.Note)
		})
	}
}

// TestMoveResultCutsListsAsALastResort pins the result of a server whose
// COPYUID names far more messages than a call sends, so the result
// passes the cap with every entry reduced to its UIDs: it still fits,
// moved is cut first, then uids and destination_uids together so they
// stay paired, then uids_not_found, then refused, and the note says the
// lists were cut.
func TestMoveResultCutsListsAsALastResort(t *testing.T) {
	escaped := strings.Repeat("<\x01", 1024)
	in := withOwnFields(maximalMove(1200, 3), escaped, escaped, escaped, "")
	in.UIDsNotFound = []uint32{1, 2, 3}
	resp := cutMove(t, in)
	if len(resp.Moved) != 0 || resp.MovedOmitted != 0 {
		t.Errorf("moved %d, moved_omitted %d; moved is cut first, and its count follows it", len(resp.Moved), resp.MovedOmitted)
	}
	n := len(resp.UIDs)
	if n == 0 || n >= len(in.UIDs) || !slices.Equal(resp.UIDs, in.UIDs[:n]) || !slices.Equal(resp.DestinationUIDs, in.DestinationUIDs[:n]) {
		t.Errorf("uids %d and destination_uids %d of %d; want the same leading run of each", n, len(resp.DestinationUIDs), len(in.UIDs))
	}
	if !slices.Equal(resp.UIDsNotFound, in.UIDsNotFound) || len(resp.Refused) != len(in.Refused) || resp.RefusedOmitted != len(in.Refused) {
		t.Errorf("uids_not_found %v, refused %d, refused_omitted %d; both lists are cut only after uids", resp.UIDsNotFound, len(resp.Refused), resp.RefusedOmitted)
	}

	in = maximalMove(0, 1500)
	resp = cutMove(t, in)
	n = len(resp.Refused)
	if n == 0 || n >= len(in.Refused) || resp.RefusedOmitted != n {
		t.Fatalf("refused %d of %d, refused_omitted %d; want a leading run, every entry counted", n, len(in.Refused), resp.RefusedOmitted)
	}
	for i, r := range resp.Refused {
		if r.UID != in.Refused[i].UID {
			t.Errorf("refused[%d] uid = %d, want %d", i, r.UID, in.Refused[i].UID)
		}
	}
}

// cutMove renders a result that must be cut short and checks what every
// cut result shares: it fits, and its note says the lists were cut.
func cutMove(t *testing.T, in moveResponse) moveResponse {
	t.Helper()
	out, err := marshalMoveResponse(in)
	if err != nil {
		t.Fatalf("marshalMoveResponse: %v", err)
	}
	if len(out) > maxMoveOutput {
		t.Errorf("move result = %d bytes, cap %d", len(out), maxMoveOutput)
	}
	resp := decodeMove(t, out)
	if !strings.HasSuffix(resp.Note, moveCutNote) {
		t.Errorf("note = %q, want it to end %q", resp.Note, moveCutNote)
	}
	return resp
}

// withOwnFields sets the account, folders, and note of a move result.
func withOwnFields(resp moveResponse, account, source, destination, note string) moveResponse {
	resp.Account, resp.SourceFolder, resp.DestinationFolder, resp.Note = account, source, destination, note
	return resp
}

// checkClipped asserts a field within the bound is unchanged and a
// longer one is a prefix of it, within the bound, valid UTF-8, and
// ending with the marker.
func checkClipped(t *testing.T, field, got, in string) {
	t.Helper()
	if len(in) <= maxMoveFieldOutput {
		if got != in {
			t.Errorf("%s = %q, want it unchanged", field, got)
		}
		return
	}
	kept, cut := strings.CutSuffix(got, fieldCutMarker)
	if !cut || len(got) > maxMoveFieldOutput || !utf8.ValidString(got) || !strings.HasPrefix(in, kept) {
		t.Errorf("%s = %d bytes %q; want a prefix of the input within %d bytes ending %q", field, len(got), got, maxMoveFieldOutput, fieldCutMarker)
	}
}

// checkDetail asserts an entry either keeps its detail, each field
// present, within the bound and valid UTF-8, or carries none.
func checkDetail(t *testing.T, entry string, kept bool, fields ...string) {
	t.Helper()
	for _, f := range fields {
		switch {
		case !kept && f != "":
			t.Errorf("%s past the budget still carries %q", entry, f)
		case kept && (f == "" || len(f) > maxMoveFieldOutput || !utf8.ValidString(f)):
			t.Errorf("%s detail = %d bytes, valid %v", entry, len(f), utf8.ValidString(f))
		}
	}
}
