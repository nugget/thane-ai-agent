package email

import (
	"context"
	"testing"
)

// TestReadOnlyOperatorMailboxRefusesUnattendedSeen pins the order of
// email_read's two seen rules: on an operator mailbox whose access is
// read, an unattended mark_seen: true is refused as on any operator
// mailbox, not quietly downgraded to an unmarked read.
func TestReadOnlyOperatorMailboxRefusesUnattendedSeen(t *testing.T) {
	tests := []struct {
		name    string
		owner   string
		ctx     context.Context
		wantErr bool
	}{
		{"operator read-only unattended is refused", "operator", context.Background(), true},
		{"operator read-only attended reads unmarked", "operator", attendedCtx(), false},
		{"assistant read-only unattended reads unmarked", "assistant", context.Background(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mem, _ := policyService(t, ServiceDependencies{Contacts: identityStub()}, func(cfg *Config) {
				cfg.Accounts[0].Mailbox.Owner = tt.owner
				cfg.Accounts[0].Policy.Access = AccessRead
			})
			uid := mem.append("INBOX", rawMessage("Alice <alice@example.com>", "thane@example.com", "Hello", "hi"))
			out, err := svc.ToolProvider().HandleRead(tt.ctx, map[string]any{"uid": float64(uid), "mark_seen": true})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("read = %s, want a refusal", out)
				}
				mustContain(t, err.Error(), `account "primary"`, "operator's own mailbox", "mark_seen: false")
			} else if err != nil {
				t.Fatalf("HandleRead: %v", err)
			} else {
				mustContain(t, out, `"marked_seen":false`, "policy.access is read")
			}
			if seenFlag(t, svc, uid) {
				t.Error("a read-only account must leave the message unread")
			}
		})
	}
}
