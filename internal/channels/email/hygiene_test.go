package email

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

// TestClientWatchdogBoundsADeadlinelessOperation pins the operation
// timeout: a server that answers one line and then stalls cannot hold
// the account mutex past defaultOpTimeout even when the caller passed
// no deadline, the failure reads as a timeout, and the next call on
// the same client reconnects instead of blocking.
func TestClientWatchdogBoundsADeadlinelessOperation(t *testing.T) {
	saved := defaultOpTimeout
	defaultOpTimeout = 300 * time.Millisecond
	t.Cleanup(func() { defaultOpTimeout = saved })

	host, port := stallingIMAP(t)
	implicit := true
	c := NewClient("primary", IMAPConfig{Host: host, Port: port, Username: "a", Password: "b", TLS: &implicit}, nil)
	t.Cleanup(func() { _ = c.Close() })

	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping against the scripted server: %v", err)
	}
	start := time.Now()
	_, err := c.MailboxStatus(context.Background(), "INBOX")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a stalled STATUS must fail")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("STATUS held the account for %v; the watchdog must end it near %v", elapsed, defaultOpTimeout)
	}
	if kind := FailureKindOf(err); kind != FailureTimeout {
		t.Errorf("kind = %q, want %q (%v)", kind, FailureTimeout, err)
	}
	mustContain(t, err.Error(), "stopped answering", "300ms", `"primary"`)

	// The mutex was released and the connection dropped: the next call
	// reconnects to the still-scripted server and the greeting, LOGIN,
	// and NOOP succeed.
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping after the watchdog fired: %v", err)
	}
}

// TestClientTLSPaths pins the transport-security rules: implicit TLS
// verifies the test chain and works; an untrusted certificate is
// refused as a TLS failure before any credential is sent; and a
// plaintext port without STARTTLS is refused the same way, mirroring
// the SMTP rule.
func TestClientTLSPaths(t *testing.T) {
	implicit := newMemIMAP(t, func(o *memIMAPOptions) { o.implicitTLS = true })
	implicit.append("INBOX", rawMessage("a@example.com", "b@example.com", "secure", "1"))
	c := implicit.newClient("primary")
	if got, err := c.ListMessages(context.Background(), ListOptions{}); err != nil || got.TotalMatched != 1 {
		t.Fatalf("ListMessages over implicit TLS = %+v, %v", got, err)
	}

	untrusted := newMemIMAP(t, func(o *memIMAPOptions) { o.implicitTLS = true; o.untrustedCert = true })
	err := untrusted.newClient("primary").Ping(context.Background())
	if FailureKindOf(err) != FailureTLS {
		t.Errorf("untrusted implicit TLS: kind = %q, want tls_failed (%v)", FailureKindOf(err), err)
	}

	startTLS := newMemIMAP(t)
	if err := startTLS.newClient("primary").Ping(context.Background()); err != nil {
		t.Fatalf("Ping over STARTTLS: %v", err)
	}

	untrustedStart := newMemIMAP(t, func(o *memIMAPOptions) { o.untrustedCert = true })
	err = untrustedStart.newClient("primary").Ping(context.Background())
	if FailureKindOf(err) != FailureTLS {
		t.Errorf("untrusted STARTTLS: kind = %q, want tls_failed (%v)", FailureKindOf(err), err)
	}

	plaintext := newMemIMAP(t, func(o *memIMAPOptions) { o.noSTARTTLS = true })
	err = plaintext.newClient("primary").Ping(context.Background())
	if FailureKindOf(err) != FailureTLS {
		t.Fatalf("plaintext without STARTTLS: kind = %q, want tls_failed (%v)", FailureKindOf(err), err)
	}
	mustContain(t, err.Error(), "STARTTLS", "no credential was sent")
}

// TestClientMoveRefusedWithoutMoveOrUIDPlus pins the refusal that
// keeps a rev1-only server from expunging bystanders.
func TestClientMoveRefusedWithoutMoveOrUIDPlus(t *testing.T) {
	m := newMemIMAP(t, func(o *memIMAPOptions) { o.rev1Only = true })
	c := m.newClient("primary")
	uid := m.append("INBOX", rawMessage("a@example.com", "b@example.com", "stay", "1"))
	_, err := c.MoveMessages(context.Background(), MoveOptions{Folder: "INBOX", UIDs: []uint32{uid}, Destination: "Archive"})
	if FailureKindOf(err) != FailureUnsupported {
		t.Fatalf("kind = %q, want unsupported (%v)", FailureKindOf(err), err)
	}
	mustContain(t, err.Error(), "nothing was changed", `"primary"`)
	if got, _ := c.ListMessages(context.Background(), ListOptions{}); got.TotalMatched != 1 {
		t.Errorf("the message must still be in INBOX, got %d", got.TotalMatched)
	}
}

// TestClientMarkAlreadyInStateCountsAsAffected pins the read-back
// semantic the Godoc promises.
func TestClientMarkAlreadyInStateCountsAsAffected(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	uid := m.append("INBOX", rawMessage("a@example.com", "b@example.com", "one", "1"))
	for i := 0; i < 2; i++ {
		res, err := c.MarkMessages(context.Background(), MarkAction{UIDs: []uint32{uid}, Flag: "flagged", Add: true})
		if err != nil {
			t.Fatalf("MarkMessages #%d: %v", i, err)
		}
		if fmtUIDs(res.Affected) != fmtUIDs([]uint32{uid}) {
			t.Errorf("MarkMessages #%d Affected = %v; a flag already set is still the requested state", i, res.Affected)
		}
	}
	res, err := c.MarkMessages(context.Background(), MarkAction{UIDs: []uint32{uid}, Flag: "answered", Add: false})
	if err != nil || fmtUIDs(res.Affected) != fmtUIDs([]uint32{uid}) {
		t.Errorf("removing a flag never set: Affected = %v, %v", res.Affected, err)
	}
}

// TestClassifyIMAPError pins one row per classification.
func TestClassifyIMAPError(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	no := func(code imap.ResponseCode) error { return &imap.Error{Type: imap.StatusResponseTypeNo, Code: code} }
	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want FailureKind
	}{
		{"nil", context.Background(), nil, FailureUnclassified},
		{"auth failed", context.Background(), no(imap.ResponseCodeAuthenticationFailed), FailureAuthentication},
		{"authz failed", context.Background(), no(imap.ResponseCodeAuthorizationFailed), FailureAuthentication},
		{"expired", context.Background(), no(imap.ResponseCodeExpired), FailureAuthentication},
		{"nonexistent", context.Background(), no(imap.ResponseCodeNonExistent), FailureFolderNotFound},
		{"trycreate", context.Background(), no(imap.ResponseCodeTryCreate), FailureFolderNotFound},
		{"noperm", context.Background(), no(imap.ResponseCodeNoPerm), FailurePermission},
		{"overquota", context.Background(), no(imap.ResponseCodeOverQuota), FailureQuota},
		{"unavailable", context.Background(), no(imap.ResponseCodeUnavailable), FailureUnavailable},
		{"inuse", context.Background(), no(imap.ResponseCodeInUse), FailureUnavailable},
		{"serverbug", context.Background(), no(imap.ResponseCodeServerBug), FailureUnavailable},
		{"plain NO", context.Background(), no(""), FailureUnclassified},
		{"eof", context.Background(), io.EOF, FailureUnavailable},
		{"unexpected eof", context.Background(), io.ErrUnexpectedEOF, FailureUnavailable},
		{"context canceled error", context.Background(), context.Canceled, FailureCancelled},
		{"cancelled ctx wins over plain error", cancelled, errors.New("connection closed"), FailureCancelled},
		{"other", context.Background(), errors.New("weird"), FailureUnclassified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyIMAPError(tt.ctx, tt.err); got != tt.want {
				t.Errorf("classifyIMAPError = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFolderErrorOnlyDowngradesMissingFolders pins the not-found
// downgrade: a bare NO on a folder that exists keeps the server's
// text, one on a folder that does not lists the real folders.
func TestFolderErrorOnlyDowngradesMissingFolders(t *testing.T) {
	m := newMemIMAP(t)
	c := m.newClient("primary")
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	bare := &imap.Error{Type: imap.StatusResponseTypeNo, Text: "message too big"}

	err := c.folderError(context.Background(), "append to folder", "inbox", bare)
	if FailureKindOf(err) != FailureUnclassified {
		t.Errorf("NO on an existing folder must not read as not-found: %v", err)
	}
	err = c.folderError(context.Background(), "select folder", "Nope", bare)
	if FailureKindOf(err) != FailureFolderNotFound {
		t.Fatalf("NO on a missing folder = %q, want folder_not_found", FailureKindOf(err))
	}
	mustContain(t, err.Error(), "Archive", "Drafts", `"Nope"`)

	long := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		long = append(long, strings.Repeat("F", 1)+string(rune('A'+i%26))+string(rune('a'+i/26)))
	}
	rendered := (&ClientError{Account: "primary", Op: "op", Folder: "x", Kind: FailureFolderNotFound, Available: long}).Error()
	mustContain(t, rendered, "and 15 more", "email_folders")
	if strings.Count(rendered, ", ") > maxListedFolders+2 {
		t.Errorf("folder list not capped: %s", rendered)
	}
}
