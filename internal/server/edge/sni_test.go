package edge

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"testing"
)

// refusals returns only the door's refusal records from a log buffer
// certmagic's own maintenance lines also land in.
func refusals(buf *bytes.Buffer) []string {
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
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
			name:       "a name it holds, spelled with the trailing dot of a fully qualified name",
			serverName: "thane.example.net.",
			want:       "internal error",
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

	for _, name := range []string{"pocket.example.org", "", "thane.example.net.evil.example.org"} {
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
	var buf bytes.Buffer
	s := testDoor(t, slog.New(slog.NewJSONHandler(&buf, nil)))

	hello := func(name string) *tls.ClientHelloInfo {
		return &tls.ClientHelloInfo{ServerName: name, Conn: fakeConn{}}
	}

	s.recordUnheldName(hello("pocket.example.org"))
	got := refusals(&buf)
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
	if got := refusals(&buf); len(got) != 0 {
		t.Fatalf("a burst inside the window logged %d records: %v", len(got), got)
	}

	s.flushUnheldNames()
	got = refusals(&buf)
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
	if got := refusals(&buf); len(got) != 0 {
		t.Errorf("flush with nothing pending logged: %v", got)
	}
}

// TestShutdownStopsTheTailFlush keeps a door that has stopped serving
// from logging afterwards.
func TestShutdownStopsTheTailFlush(t *testing.T) {
	var buf bytes.Buffer
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
