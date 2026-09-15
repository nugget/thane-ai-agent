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
