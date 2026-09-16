package email

import (
	"context"
	"slices"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// TestDraftOnlyRecipientsLoseTheirDisplayName pins that a relaxed
// drafts-only account names every draft_only recipient in the draft by
// bare address, whatever display name the model or the original message
// gave it, while a recipient the gate allowed keeps its name. The
// operator's send is the only gate there, and their client shows the
// name in place of the address, so a stranger must not be drafted under
// a household member's name.
func TestDraftOnlyRecipientsLoseTheirDisplayName(t *testing.T) {
	tests := []struct {
		name           string
		to, cc         []string // email_send recipients; empty for a reply
		replyTo        string   // the original's Reply-To, for a reply
		wantTo, wantCc []Address
	}{
		{name: "a stranger under a household member's name", to: []string{`"Hannah" <mallory@example.net>`},
			wantTo: []Address{{Address: "mallory@example.net"}}},
		{name: "a stranger under a contact's address as the name", to: []string{`"hannah@example.com" <mallory@example.net>`},
			wantTo: []Address{{Address: "mallory@example.net"}}},
		{name: "a known contact", to: []string{`"Hannah" <kim@example.com>`},
			wantTo: []Address{{Address: "kim@example.com"}}},
		{name: "an allowed contact keeps its name beside a stranger in cc", to: []string{`"Alice Liddell" <alice@example.com>`}, cc: []string{`"Hannah" <mallory@example.net>`},
			wantTo: []Address{{Name: "Alice Liddell", Address: "alice@example.com"}}, wantCc: []Address{{Address: "mallory@example.net"}}},
		{name: "a forged Reply-To on a reply", replyTo: `"Alice" <mallory@example.net>`,
			wantTo: []Address{{Address: "mallory@example.net"}}},
	}
	anys := func(list []string) []any {
		out := make([]any, len(list))
		for i, s := range list {
			out[i] = s
		}
		return out
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem, smtp := gateService(t, modeRelaxed, nil)
			ctx := tools.WithMessageOrigin(context.Background(), memory.OriginWake)
			var out string
			var err error
			if tt.replyTo != "" {
				uid := mem.append("INBOX", rawMessage("Bob <bob@example.net>", "thane@example.com", "Door", "what is the code?", "Reply-To: "+tt.replyTo))
				out, err = svc.ToolProvider().HandleReply(ctx, map[string]any{"uid": float64(uid), "body": "see you"})
			} else {
				args := map[string]any{"to": anys(tt.to), "subject": "hi", "body": "see you"}
				if len(tt.cc) > 0 {
					args["cc"] = anys(tt.cc)
				}
				out, err = svc.ToolProvider().HandleSend(ctx, args)
			}
			if err != nil {
				t.Fatalf("want drafted, got %v", err)
			}
			resp := decodeSend(t, out)
			if resp.Disposition != DispositionDrafted || resp.Decision.Gating != GatingDraftOnly {
				t.Fatalf("result = %s gating %s, want drafted gating draft_only", resp.Disposition, resp.Decision.Gating)
			}
			if !slices.Equal(resp.To, addressStrings(tt.wantTo)) || !slices.Equal(resp.Cc, nonNilStrings(addressStrings(tt.wantCc))) {
				t.Errorf("result to %q cc %q, want to %q cc %q", resp.To, resp.Cc, addressStrings(tt.wantTo), addressStrings(tt.wantCc))
			}
			acct, _ := svc.ResolveAccount(context.Background(), "primary")
			draft, err := acct.Client.ReadMessage(context.Background(), ReadOptions{Folder: resp.DraftsFolder, UID: resp.DraftUID, Peek: true})
			if err != nil {
				t.Fatalf("read the draft: %v", err)
			}
			if !slices.Equal(draft.To, tt.wantTo) || !slices.Equal(draft.Cc, tt.wantCc) {
				t.Errorf("draft To %+v Cc %+v, want To %+v Cc %+v", draft.To, draft.Cc, tt.wantTo, tt.wantCc)
			}
			if n := len(smtp.received()); n != 0 {
				t.Errorf("SMTP deliveries = %d; a relaxed account never sends", n)
			}
		})
	}
}
