package email

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// reorderedCopyUIDServer is a scripted IMAP server whose UID MOVE
// answers COPYUID 9 5,3 10,11. RFC 4315 pairs the two sets by position,
// so source UID 5 became 10 and source UID 3 became 11. It returns the
// host and port to dial.
func reorderedCopyUIDServer(t *testing.T) (string, int) {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", testServerTLS(t))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			go serveReorderedCopyUID(conn)
		}
	}()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %v is not TCP", ln.Addr())
	}
	return addr.IP.String(), addr.Port
}

// serveReorderedCopyUID answers one connection's commands until it ends.
func serveReorderedCopyUID(conn net.Conn) {
	_, _ = fmt.Fprint(conn, "* OK [CAPABILITY IMAP4rev1 MOVE UIDPLUS] ready\r\n")
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		tag, cmd := f[0], strings.ToUpper(f[1])
		switch {
		case cmd == "CAPABILITY":
			_, _ = fmt.Fprintf(conn, "* CAPABILITY IMAP4rev1 MOVE UIDPLUS\r\n%s OK done\r\n", tag)
		case cmd == "SELECT" || cmd == "EXAMINE":
			_, _ = fmt.Fprintf(conn, "* 2 EXISTS\r\n* OK [UIDVALIDITY 7] v\r\n* OK [UIDNEXT 6] n\r\n* FLAGS (\\Flagged \\Seen)\r\n* OK [PERMANENTFLAGS (\\Flagged \\Seen \\*)] p\r\n%s OK [READ-WRITE] done\r\n", tag)
		case cmd == "UID" && len(f) > 2 && strings.ToUpper(f[2]) == "MOVE":
			_, _ = fmt.Fprintf(conn, "* OK [COPYUID 9 5,3 10,11] moved\r\n* 2 EXPUNGE\r\n* 1 EXPUNGE\r\n%s OK done\r\n", tag)
		case cmd == "LOGOUT":
			_, _ = fmt.Fprintf(conn, "* BYE\r\n%s OK done\r\n", tag)
			return
		default:
			_, _ = fmt.Fprintf(conn, "%s OK done\r\n", tag)
		}
	}
}

// TestReorderedCopyUIDNeverHandsTheClaimToAnotherCopy pins the move of
// two byte-identical copies in one call: Thane marked uid 3, the
// operator flagged uid 5 the same colour, and the server numbered their
// new copies out of order. go-imap hands back sorted sets that pair 3
// with 10, which is really the operator's copy, so the claim must stay
// on the copy Thane marked and lapse, never reach either new copy.
func TestReorderedCopyUIDNeverHandsTheClaimToAnotherCopy(t *testing.T) {
	host, port := reorderedCopyUIDServer(t)
	implicit := true
	c := NewClient("primary", IMAPConfig{Host: host, Port: port, Username: "u", Password: "p", TLS: &implicit}, quietSlog())
	t.Cleanup(func() { _ = c.Close() })
	result, err := c.MoveMessages(context.Background(), MoveOptions{Folder: "INBOX", UIDs: []uint32{3, 5}, Destination: "Archive"})
	if err != nil {
		t.Fatalf("MoveMessages: %v", err)
	}
	if !result.DestUIDsKnown || len(result.UIDs) != 2 || len(result.DestUIDs) != 2 {
		t.Fatalf("MoveResult = %+v, want two confirmed UIDs", result)
	}

	svc, _, _ := labelService(t, map[string]LabelConfig{"contact": contactLabel}, nil)
	id := messageIDFor("Hello")
	marked := newMessageCopy("INBOX", 7, 3)
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
	senders := map[uint32]moveSender{
		3: {envelope: Envelope{UID: 3, MessageID: id}},
		5: {envelope: Envelope{UID: 5, MessageID: id}},
	}
	svc.carryLabelClaims(context.Background(), "primary", result, senders)
	if got := marksFor(t, svc, "Hello").Copy; got != marked {
		t.Errorf("record copy = %+v after the move, want it left on %+v; COPYUID gave the operator's uid 5 Archive uid 10", got, marked)
	}
}
