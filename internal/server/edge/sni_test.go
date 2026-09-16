package edge

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a log sink safe to read while certmagic's background
// maintenance goroutine is writing to it. A bare bytes.Buffer is not:
// the cache starts maintaining assets the moment a door is built, and
// its first line races the test's own reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// refusals returns only the door's refusal records; certmagic's own
// maintenance lines land in the same sink.
func (b *syncBuffer) refusals() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if strings.Contains(line, "door does not hold") {
			out = append(out, line)
		}
	}
	return out
}

func testDoor(t *testing.T, logger *slog.Logger) *Server {
	t.Helper()
	s, err := New(Options{Config: testConfig(t, t.TempDir()), Surfaces: testSurfaces(), Logger: logger})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return s
}

// TestUnheldNameIsRefusedAsUnrecognized is the point of the whole file.
// A name the door does not hold and a name it holds without a
// certificate used to fail identically — internal_error, the alert that
// says the server is broken. They now say different things, and the
// difference is the one an operator needs: alert 112 means you asked the
// wrong door, alert 80 means this door owes you a certificate it does
// not have.
func TestUnheldNameIsRefusedAsUnrecognized(t *testing.T) {
	s := testDoor(t, slog.New(slog.DiscardHandler))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			tlsConn := tls.Server(conn, s.https.TLSConfig)
			_ = tlsConn.Handshake()
			_ = tlsConn.Close()
		}
	}()

	tests := []struct {
		name       string
		serverName string
		want       string
	}{
		{
			name:       "a name this door was never given",
			serverName: "pocket.example.org",
			want:       "unrecognized name",
		},
		{
			name:       "a name it holds, whose certificate has not been issued",
			serverName: "thane.example.net",
			want:       "internal error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
				ServerName:         tt.serverName,
				InsecureSkipVerify: true,
			})
			if err == nil {
				conn.Close()
				t.Fatalf("handshake for %q succeeded; the door holds no certificates", tt.serverName)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("handshake for %q failed with %q, want an alert saying %q", tt.serverName, err, tt.want)
			}
		})
	}
}

// TestCertificateForDelegatesOnlyHeldNames pins the dispatch itself: a
// held name reaches certmagic untouched, an unheld one never does and
// returns the nil certificate and nil error that produce the alert.
func TestCertificateForDelegatesOnlyHeldNames(t *testing.T) {
	s := testDoor(t, slog.New(slog.DiscardHandler))

	var delegated []string
	sentinel := errors.New("delegated")
	get := s.certificateFor(func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		delegated = append(delegated, hello.ServerName)
		return nil, sentinel
	})

	for _, name := range []string{"thane.example.net", "THANE.example.net", "ollama.example.net"} {
		if _, err := get(&tls.ClientHelloInfo{ServerName: name}); !errors.Is(err, sentinel) {
			t.Errorf("held name %q was not delegated: %v", name, err)
		}
	}
	if len(delegated) != 3 {
		t.Errorf("delegated %v, want all three held names", delegated)
	}

	// A trailing dot never reaches this callback in a real handshake —
	// the client's stdlib strips it before sending SNI, and a
	// ClientHello carrying one anyway is refused while it is parsed — so
	// the dotted spelling is simply not a name this door holds.
	for _, name := range []string{"pocket.example.org", "", "thane.example.net.", "thane.example.net.evil.example.org"} {
		cert, err := get(&tls.ClientHelloInfo{ServerName: name})
		if cert != nil || err != nil {
			t.Errorf("unheld name %q returned (%v, %v), want (nil, nil) so the stdlib sends unrecognized_name", name, cert, err)
		}
	}
}

// TestRefusalWarnsOnceThenCoalesces covers the noise budget. The first
// refusal after a quiet stretch is a diagnostic and logs in full; a
// burst behind it is one summary, not one line per handshake, because a
// client in a retry loop must not be able to write the log.
func TestRefusalWarnsOnceThenCoalesces(t *testing.T) {
	var buf syncBuffer
	s := testDoor(t, slog.New(slog.NewJSONHandler(&buf, nil)))

	hello := func(name string) *tls.ClientHelloInfo {
		return &tls.ClientHelloInfo{ServerName: name, Conn: fakeConn{}}
	}

	s.recordUnheldName(hello("pocket.example.org"))
	got := buf.refusals()
	if len(got) != 1 {
		t.Fatalf("first refusal wrote %d records, want 1: %v", len(got), got)
	}
	first := got[0]
	if !strings.Contains(first, "pocket.example.org") || !strings.Contains(first, "203.0.113.7:52000") {
		t.Fatalf("first refusal did not name the hostname and the peer: %s", first)
	}
	if !strings.Contains(first, "WARN") {
		t.Errorf("first refusal was not a warning: %s", first)
	}

	buf.Reset()
	for i := range 50 {
		s.recordUnheldName(hello(fmt.Sprintf("scan-%d.example.org", i)))
	}
	if got := buf.refusals(); len(got) != 0 {
		t.Fatalf("a burst inside the window logged %d records: %v", len(got), got)
	}

	s.flushUnheldNames()
	got = buf.refusals()
	if len(got) != 1 {
		t.Fatalf("the burst summary is not one record: %v", got)
	}
	summary := got[0]
	if !strings.Contains(summary, `"refused_since_last":50`) || !strings.Contains(summary, `"refused_total":51`) {
		t.Errorf("summary lost the count: %s", summary)
	}
	// The sample is bounded: the names are chosen by whoever is
	// connecting, so a scanner cannot make one log record arbitrarily
	// large.
	if names := strings.Count(summary, "scan-"); names > unheldNameSampleLimit {
		t.Errorf("summary carried %d names, want at most %d: %s", names, unheldNameSampleLimit, summary)
	}

	// Nothing further to report means nothing further is written, so a
	// quiet door stays quiet.
	buf.Reset()
	s.flushUnheldNames()
	if got := buf.refusals(); len(got) != 0 {
		t.Errorf("flush with nothing pending logged: %v", got)
	}
}

// TestShutdownStopsTheTailFlush keeps a door that has stopped serving
// from logging afterwards.
func TestShutdownStopsTheTailFlush(t *testing.T) {
	var buf syncBuffer
	s := testDoor(t, slog.New(slog.NewJSONHandler(&buf, nil)))

	s.recordUnheldName(&tls.ClientHelloInfo{ServerName: "pocket.example.org", Conn: fakeConn{}})
	s.recordUnheldName(&tls.ClientHelloInfo{ServerName: "pocket.example.org", Conn: fakeConn{}})

	s.sniMu.Lock()
	pending := s.sniFlush != nil
	s.sniMu.Unlock()
	if !pending {
		t.Fatal("a folded refusal scheduled no tail flush")
	}

	if err := s.Shutdown(t.Context()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	s.sniMu.Lock()
	defer s.sniMu.Unlock()
	if s.sniFlush != nil {
		t.Error("shutdown left a tail flush armed")
	}
}

// fakeConn is a net.Conn that only knows where it came from, which is
// all a ClientHelloInfo needs for the refusal log.
type fakeConn struct{ net.Conn }

func (fakeConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 52000}
}

// TestOversizeNameIsCappedInTheLog covers the one field of the refusal
// record a stranger writes. The stdlib checks that a ClientHello's
// server name is a well-formed extension, not that it is a plausible
// hostname, so the name arriving here can be far longer than any DNS
// name — and it must not be able to set the size of a log record.
func TestOversizeNameIsCappedInTheLog(t *testing.T) {
	var buf syncBuffer
	s := testDoor(t, slog.New(slog.NewJSONHandler(&buf, nil)))

	huge := strings.Repeat("a", 40_000) + ".example.org"
	s.recordUnheldName(&tls.ClientHelloInfo{ServerName: huge, Conn: fakeConn{}})

	got := buf.refusals()
	if len(got) != 1 {
		t.Fatalf("wrote %d records, want 1: %v", len(got), got)
	}
	if len(got[0]) > 1000 {
		t.Errorf("a %d-byte server name produced a %d-byte record", len(huge), len(got[0]))
	}
	if !strings.Contains(got[0], "truncated") {
		t.Errorf("the record does not say the name was shortened: %s", got[0])
	}

	// Truncation is for the log only: the held-set lookup sees the name
	// as sent, so no shortened name can ever match a held one.
	if normalizeServerName(huge) != strings.ToLower(huge) {
		t.Error("normalizeServerName shortened a name the certificate decision is made on")
	}
}

// TestClosingWindowReportsWhatFoldedIntoIt covers the seam between the
// two paths. A window can close while refusals are still folded into it
// — the tail timer fires but its callback has not reached the lock — and
// the refusal that closes it must carry their summary out rather than
// marking them logged and stranding their names in the next window.
func TestClosingWindowReportsWhatFoldedIntoIt(t *testing.T) {
	var buf syncBuffer
	s := testDoor(t, slog.New(slog.NewJSONHandler(&buf, nil)))

	hello := func(name string) *tls.ClientHelloInfo {
		return &tls.ClientHelloInfo{ServerName: name, Conn: fakeConn{}}
	}

	s.recordUnheldName(hello("first.example.org"))
	s.recordUnheldName(hello("folded.example.org"))
	buf.Reset()

	// Close the window without letting the tail flush run, which is what
	// a fired-but-not-yet-scheduled callback looks like from here.
	s.sniMu.Lock()
	s.sniLastLog = time.Now().Add(-2 * unheldNameLogInterval)
	s.sniMu.Unlock()
	s.recordUnheldName(hello("next.example.org"))

	got := buf.refusals()
	if len(got) != 2 {
		t.Fatalf("wrote %d records, want the folded summary and the new refusal: %v", len(got), got)
	}
	if !strings.Contains(got[0], "folded.example.org") {
		t.Errorf("the folded refusal was never reported: %s", got[0])
	}
	if !strings.Contains(got[1], "next.example.org") {
		t.Errorf("the refusal that closed the window was not logged: %s", got[1])
	}

	// Nothing is left over to reappear in a later window.
	buf.Reset()
	s.flushUnheldNames()
	if got := buf.refusals(); len(got) != 0 {
		t.Errorf("stale samples survived into the next window: %v", got)
	}
}

// TestShutdownSilencesALateFlush is the other half of cancellation.
// Timer.Stop cannot recall a callback that has already started, so a
// callback that was waiting on the lock while Shutdown ran has to find
// the door stopped and write nothing.
func TestShutdownSilencesALateFlush(t *testing.T) {
	var buf syncBuffer
	s := testDoor(t, slog.New(slog.NewJSONHandler(&buf, nil)))

	s.recordUnheldName(&tls.ClientHelloInfo{ServerName: "pocket.example.org", Conn: fakeConn{}})
	s.recordUnheldName(&tls.ClientHelloInfo{ServerName: "folded.example.org", Conn: fakeConn{}})
	if err := s.Shutdown(t.Context()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	buf.Reset()

	// The callback runs anyway: this is the goroutine that was already
	// past Stop when Shutdown took the lock.
	s.flushUnheldNames()
	if got := buf.refusals(); len(got) != 0 {
		t.Errorf("a shut-down door logged from a late flush: %v", got)
	}
}
