package email

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// TestRefusalsFoldFolderCase pins the refuse-only checks as blind to
// case, because some servers fold the case of every mailbox name: a
// case variant of the junk folder still meets the junk guard, and a
// case variant of the drafts folder is still refused as a destination
// and as a source. Every refusal lands before the server is asked, so
// the case-sensitive test server never sees the variant.
func TestRefusalsFoldFolderCase(t *testing.T) {
	lowerJunk := strings.ToLower(junkTestFolder)
	tests := []struct {
		name   string
		source string // where the message is appended
		call   func(*Tools, context.Context, map[string]any) (string, error)
		args   map[string]any
		want   []string // a refusal error, or nil for a guarded result
	}{
		{"junk guard on a case variant of the junk folder", "INBOX", (*Tools).HandleMove, map[string]any{"destination": lowerJunk}, nil},
		{"drafts destination in another case", "INBOX", (*Tools).HandleMove, map[string]any{"destination": "DRAFTS"}, []string{`email_move cannot file mail into "DRAFTS"`, "drafts waiting for the operator to send or discard"}},
		{"drafts source in another case", "Drafts", (*Tools).HandleMove, map[string]any{"folder": "drafts", "destination": "Receipts"}, []string{`email_move cannot act on mail in "Drafts"`, "nothing was moved"}},
		{"drafts marked in another case", "Drafts", (*Tools).HandleMark, map[string]any{"folder": "drafts", "flag": "flagged"}, []string{`email_mark cannot act on mail in "Drafts"`, "nothing was changed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem := filingService(t, guardContacts(), false, nil)
			uid := mem.append(tt.source, rawMessage(guardSenders["from alice"], "bob@example.org", "from alice", "x"), imap.FlagSeen)
			args := map[string]any{"uid": float64(uid)}
			for k, v := range tt.args {
				args[k] = v
			}
			out, err := tt.call(svc.ToolProvider(), context.Background(), args)
			if tt.want != nil {
				if err == nil {
					t.Fatalf("result = %s, want a refusal", out)
				}
				mustContain(t, err.Error(), tt.want...)
			} else {
				if err != nil {
					t.Fatalf("HandleMove: %v; the guard must refuse before the server is asked", err)
				}
				resp := decodeMove(t, out)
				if resp.Action != "refused" || len(resp.Refused) != 1 || resp.Refused[0].TrustZone != "trusted" {
					t.Fatalf("move = %+v, want Alice refused at trusted", resp)
				}
			}
			if got := subjectsIn(t, svc, tt.source); !slices.Equal(got, []string{"from alice"}) {
				t.Errorf("%s after the refusal = %v; the message must stay", tt.source, got)
			}
		})
	}
}

// TestJunkGuardReportsTheZoneItJudged pins refused[].trust_zone as the
// zone the guard judged: an automated address the operator filed at
// trusted is capped at known on its events, and refused at trusted.
func TestJunkGuardReportsTheZoneItJudged(t *testing.T) {
	contacts := guardContacts()
	contacts.zones["notifications@example.org"] = "trusted"
	svc, mem := filingService(t, contacts, false, nil)
	uid := mem.append("INBOX", rawMessage("Alice's notices <notifications@example.org>", "bob@example.org", "notice", "x"))
	out, err := svc.ToolProvider().HandleMove(context.Background(), map[string]any{"uid": float64(uid), "destination_role": "junk"})
	if err != nil {
		t.Fatalf("HandleMove: %v", err)
	}
	resp := decodeMove(t, out)
	if len(resp.Refused) != 1 {
		t.Fatalf("refused = %+v, want the notice refused", resp.Refused)
	}
	if r := resp.Refused[0]; r.TrustZone != "trusted" || !strings.Contains(r.Reason, "a contact at trusted") {
		t.Errorf("refused entry = %+v, want trust_zone trusted matching its reason", r)
	}
}
