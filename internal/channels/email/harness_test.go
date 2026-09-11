package email

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// The harness in this file stands up real protocol servers on the
// loopback interface so the client layer is tested against the wire,
// not against a mock of itself: go-imap's in-memory IMAP server for
// IMAP, and a small scripted SMTP server for delivery.

const (
	testIMAPUser = "alice"
	testIMAPPass = "secret"
)

// memIMAP is one in-memory IMAP server with one user and the folders
// a real account has.
type memIMAP struct {
	t      *testing.T
	host   string
	port   int
	user   *imapmemserver.User
	server *imapserver.Server

	mu      sync.Mutex
	folders []string
	attrs   map[string][]imap.MailboxAttr
	tls     bool // implicit TLS listener

}

type memIMAPOptions struct {
	// rev1Only advertises IMAP4rev1 alone, without LIST-STATUS,
	// LIST-EXTENDED, SPECIAL-USE, MOVE, or UIDPLUS, to exercise the
	// fallback and refusal paths.
	rev1Only bool

	// implicitTLS wraps the listener in TLS so the client connects
	// with tls: true and verifies the test chain.
	implicitTLS bool

	// noSTARTTLS withholds the STARTTLS capability on the plaintext
	// listener, so a client with tls: false must refuse to log in.
	noSTARTTLS bool

	// untrustedCert serves a certificate the client's trust root does
	// not contain.
	untrustedCert bool
}

// newMemIMAP starts an in-memory IMAP server with INBOX, Drafts, Sent,
// Trash, and Archive. Unless rev1Only is set it advertises IMAP4rev2,
// UIDPLUS, MOVE, LIST-STATUS, LIST-EXTENDED, and SPECIAL-USE, and the
// session shim decorates the standard folders with their special-use
// attributes (imapmemserver cannot set those itself).
func newMemIMAP(t *testing.T, configure ...func(*memIMAPOptions)) *memIMAP {
	t.Helper()
	var opts memIMAPOptions
	for _, f := range configure {
		f(&opts)
	}

	mem := imapmemserver.New()
	user := imapmemserver.NewUser(testIMAPUser, testIMAPPass)
	m := &memIMAP{
		t:    t,
		user: user,
		attrs: map[string][]imap.MailboxAttr{
			"Drafts":  {imap.MailboxAttrDrafts},
			"Sent":    {imap.MailboxAttrSent},
			"Trash":   {imap.MailboxAttrTrash},
			"Archive": {imap.MailboxAttrArchive},
		},
	}
	for _, name := range []string{"INBOX", "Drafts", "Sent", "Trash", "Archive"} {
		if err := user.Create(name, nil); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		m.folders = append(m.folders, name)
	}
	mem.AddUser(user)

	tlsCfg := testServerTLS(t)
	if opts.untrustedCert {
		tlsCfg = untrustedServerTLS(t)
	}
	var startTLS *tls.Config
	if !opts.noSTARTTLS && !opts.implicitTLS {
		startTLS = tlsCfg
	}

	caps := imap.CapSet{imap.CapIMAP4rev1: {}}
	if !opts.rev1Only {
		for _, c := range []imap.Cap{imap.CapIMAP4rev2, imap.CapUIDPlus, imap.CapMove, imap.CapListStatus, imap.CapListExtended, imap.CapSpecialUse} {
			caps[c] = struct{}{}
		}
	}

	m.server = imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			sess := mem.NewSession()
			if opts.rev1Only {
				return sess, nil, nil
			}
			return &specialUseSession{Session: sess, mem: m}, nil, nil
		},
		InsecureAuth: true,
		TLSConfig:    startTLS,
		Caps:         caps,
		Logger:       quietLogger{},
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	m.tls = opts.implicitTLS
	if opts.implicitTLS {
		ln = tls.NewListener(ln, tlsCfg)
	}
	go func() { _ = m.server.Serve(ln) }()
	t.Cleanup(func() { _ = m.server.Close() })

	tcp := ln.Addr().(*net.TCPAddr)
	m.host, m.port = tcp.IP.String(), tcp.Port
	return m
}

// quietLogger keeps imapserver's diagnostics out of test output.
type quietLogger struct{}

func (quietLogger) Printf(string, ...interface{}) {}

// imapConfig returns the account configuration pointing at the server.
func (m *memIMAP) imapConfig() IMAPConfig {
	implicit := m.tls
	return IMAPConfig{Host: m.host, Port: m.port, Username: testIMAPUser, Password: testIMAPPass, TLS: &implicit}
}

// newClient returns a Client for the server, closed at test end.
func (m *memIMAP) newClient(name string) *Client {
	c := NewClient(name, m.imapConfig(), slog.New(slog.NewTextHandler(discardWriter{}, nil)))
	m.t.Cleanup(func() { _ = c.Close() })
	return c
}

// recreateFolder deletes and recreates a folder, which changes its
// UIDVALIDITY.
func (m *memIMAP) recreateFolder(name string) {
	m.t.Helper()
	if err := m.user.Delete(name); err != nil {
		m.t.Fatalf("delete %s: %v", name, err)
	}
	if err := m.user.Create(name, nil); err != nil {
		m.t.Fatalf("recreate %s: %v", name, err)
	}
}

// append stores a raw message in a folder and returns its UID.
func (m *memIMAP) append(folder, raw string, flags ...imap.Flag) uint32 {
	m.t.Helper()
	data, err := m.user.Append(folder, bytes.NewReader([]byte(raw)), &imap.AppendOptions{Flags: flags, Time: time.Now()})
	if err != nil {
		m.t.Fatalf("append to %s: %v", folder, err)
	}
	return uint32(data.UID)
}

func (m *memIMAP) folderNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := slices.Clone(m.folders)
	slices.Sort(names)
	return names
}

func (m *memIMAP) folderAttrs(name string) []imap.MailboxAttr {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.attrs[name]
}

// specialUseSession wraps an imapmemserver session so LIST answers
// carry special-use attributes and honors RETURN (STATUS). The
// in-memory server stores its special-use field unexported and its
// Create ignores CreateOptions.SpecialUse, so the only way to exercise
// role detection is to answer LIST ourselves.
type specialUseSession struct {
	imapserver.Session
	mem *memIMAP
}

var _ imapserver.SessionIMAP4rev2 = (*specialUseSession)(nil)

func (s *specialUseSession) List(w *imapserver.ListWriter, ref string, patterns []string, options *imap.ListOptions) error {
	if options == nil {
		options = &imap.ListOptions{}
	}
	for _, name := range s.mem.folderNames() {
		matched := false
		for _, p := range patterns {
			if imapserver.MatchList(name, '/', ref, p) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		attrs := s.mem.folderAttrs(name)
		if options.SelectSpecialUse && len(attrs) == 0 {
			continue
		}
		data := &imap.ListData{Mailbox: name, Delim: '/'}
		if options.ReturnSpecialUse || options.SelectSpecialUse {
			data.Attrs = append(data.Attrs, attrs...)
		}
		if options.ReturnStatus != nil {
			if st, err := s.Status(name, options.ReturnStatus); err == nil {
				data.Status = st
			}
		}
		if err := w.WriteList(data); err != nil {
			return err
		}
	}
	return nil
}

func (s *specialUseSession) Move(w *imapserver.MoveWriter, numSet imap.NumSet, dest string) error {
	return s.Session.(imapserver.SessionMove).Move(w, numSet, dest)
}

func (s *specialUseSession) Namespace() (*imap.NamespaceData, error) {
	return s.Session.(imapserver.SessionNamespace).Namespace()
}

// discardWriter swallows log output.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// rawMessage builds a simple RFC 5322 text message with a Message-ID
// derived from the subject, so tests can find it again.
func rawMessage(from, to, subject, body string, extraHeaders ...string) string {
	var sb strings.Builder
	sb.WriteString("From: " + from + "\r\n")
	sb.WriteString("To: " + to + "\r\n")
	sb.WriteString("Subject: " + subject + "\r\n")
	sb.WriteString("Message-ID: <" + messageIDFor(subject) + ">\r\n")
	sb.WriteString("Date: Mon, 01 Sep 2026 10:00:00 +0000\r\n")
	for _, h := range extraHeaders {
		sb.WriteString(h + "\r\n")
	}
	sb.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	sb.WriteString("\r\n")
	sb.WriteString(body + "\r\n")
	return sb.String()
}

func messageIDFor(subject string) string {
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, subject)
	return clean + "@example.com"
}

// --- SMTP fake ---

// smtpDelivery is one message the fake accepted.
type smtpDelivery struct {
	Helo  string
	Auth  string
	From  string
	To    []string
	Data  string
	IsTLS bool
}

// smtpFake is a scripted SMTP server: enough of RFC 5321 to accept a
// submission with STARTTLS or implicit TLS and PLAIN auth, and to
// refuse on cue.
type smtpFake struct {
	t        *testing.T
	ln       net.Listener
	tlsCfg   *tls.Config
	implicit bool // TLS from the first byte (port 465 style)
	starttls bool // offer STARTTLS

	rejectRcpt map[string]int // address -> reply code
	rejectAuth bool

	mu         sync.Mutex
	deliveries []smtpDelivery
}

func newSMTPFake(t *testing.T, configure func(*smtpFake)) *smtpFake {
	t.Helper()
	f := &smtpFake{t: t, tlsCfg: testServerTLS(t), starttls: true, rejectRcpt: map[string]int{}}
	if configure != nil {
		configure(f)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if f.implicit {
		ln = tls.NewListener(ln, f.tlsCfg)
	}
	f.ln = ln
	go f.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *smtpFake) config(user, pass string) SMTPConfig {
	tcp := f.ln.Addr().(*net.TCPAddr)
	startTLS := !f.implicit
	return SMTPConfig{Host: tcp.IP.String(), Port: tcp.Port, Username: user, Password: pass, StartTLS: &startTLS}
}

func (f *smtpFake) received() []smtpDelivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deliveries)
}

func (f *smtpFake) serve() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *smtpFake) handle(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	reply := func(s string) {
		_, _ = w.WriteString(s + "\r\n")
		_ = w.Flush()
	}
	reply("220 fake.example.com ESMTP")

	var d smtpDelivery
	_, d.IsTLS = conn.(*tls.Conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "EHLO"), strings.HasPrefix(verb, "HELO"):
			d.Helo = strings.TrimSpace(line[4:])
			exts := []string{"250-fake.example.com"}
			if f.starttls && !d.IsTLS {
				exts = append(exts, "250-STARTTLS")
			}
			exts = append(exts, "250-AUTH PLAIN", "250 8BITMIME")
			for _, e := range exts {
				_, _ = w.WriteString(e + "\r\n")
			}
			_ = w.Flush()
		case verb == "STARTTLS":
			reply("220 go ahead")
			tlsConn := tls.Server(conn, f.tlsCfg)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn = tlsConn
			r = bufio.NewReader(conn)
			w = bufio.NewWriter(conn)
			d.IsTLS = true
		case strings.HasPrefix(verb, "AUTH PLAIN"):
			if f.rejectAuth {
				reply("535 5.7.8 authentication failed")
				continue
			}
			d.Auth = strings.TrimSpace(line[len("AUTH PLAIN"):])
			reply("235 2.7.0 ok")
		case strings.HasPrefix(verb, "MAIL FROM:"):
			d.From = smtpPath(line[len("MAIL FROM:"):])
			reply("250 ok")
		case strings.HasPrefix(verb, "RCPT TO:"):
			rcpt := smtpPath(line[len("RCPT TO:"):])
			if code, bad := f.rejectRcpt[strings.ToLower(rcpt)]; bad {
				reply(strconv.Itoa(code) + " 5.1.1 no such user")
				continue
			}
			d.To = append(d.To, rcpt)
			reply("250 ok")
		case verb == "DATA":
			reply("354 end with .")
			var data strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				data.WriteString(l)
			}
			d.Data = data.String()
			f.mu.Lock()
			f.deliveries = append(f.deliveries, d)
			f.mu.Unlock()
			reply("250 2.0.0 queued")
		case verb == "QUIT":
			reply("221 bye")
			return
		case verb == "RSET", verb == "NOOP":
			reply("250 ok")
		default:
			reply("502 not implemented")
		}
	}
}

// smtpPath extracts the address from a MAIL FROM / RCPT TO argument,
// dropping any ESMTP parameters that follow it.
func smtpPath(arg string) string {
	arg = strings.TrimSpace(arg)
	if i := strings.IndexByte(arg, ' '); i >= 0 {
		arg = arg[:i]
	}
	return strings.Trim(arg, "<>")
}

var (
	testCertOnce sync.Once
	testCert     tls.Certificate
	testCertErr  error
)

// testServerTLS returns a self-signed certificate for 127.0.0.1 and
// installs it as the client's trust root, so the TLS paths verify a
// real chain instead of skipping verification.
func testServerTLS(t *testing.T) *tls.Config {
	t.Helper()
	testCertOnce.Do(func() {
		cert, err := selfSignedCert()
		if err != nil {
			testCertErr = err
			return
		}
		testCert = cert
		pool := x509.NewCertPool()
		pool.AddCert(cert.Leaf)
		tlsRootCAs = pool
	})
	if testCertErr != nil {
		t.Fatalf("generate test certificate: %v", testCertErr)
	}
	return &tls.Config{Certificates: []tls.Certificate{testCert}, MinVersion: tls.VersionTLS12}
}

// untrustedServerTLS returns a fresh self-signed certificate the
// client's trust root does not contain, for negative controls: a
// client that accepts it is not verifying chains.
func untrustedServerTLS(t *testing.T) *tls.Config {
	t.Helper()
	testServerTLS(t) // install the trusted root first
	cert, err := selfSignedCert()
	if err != nil {
		t.Fatalf("generate untrusted certificate: %v", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
}

// selfSignedCert generates a CA-flagged self-signed certificate for
// 127.0.0.1 and localhost.
func selfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "email test server"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// hungListener accepts connections and never speaks, for cancellation
// tests. Accepted connections are closed at test end so nothing
// outlives the test.
func hungListener(t *testing.T) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
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
		for _, conn := range conns {
			_ = conn.Close()
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
		}
	}()
	tcp := ln.Addr().(*net.TCPAddr)
	return tcp.IP.String(), tcp.Port
}

// stallingIMAP is a scripted IMAP server over TLS that greets, answers
// CAPABILITY, LOGIN, and NOOP, and on any other command writes one
// untagged line and then goes silent, so the watchdog is the only
// thing that can end the operation. It exists because imapmemserver
// cannot be told to stall.
func stallingIMAP(t *testing.T) (host string, port int) {
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
		for _, conn := range conns {
			_ = conn.Close()
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
			go func() {
				_, _ = fmt.Fprint(conn, "* OK stalling server ready\r\n")
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					fields := strings.Fields(line)
					if len(fields) < 2 {
						continue
					}
					tag, cmd := fields[0], strings.ToUpper(fields[1])
					switch cmd {
					case "CAPABILITY":
						_, _ = fmt.Fprintf(conn, "* CAPABILITY IMAP4rev1\r\n%s OK done\r\n", tag)
					case "LOGIN", "NOOP":
						_, _ = fmt.Fprintf(conn, "%s OK done\r\n", tag)
					case "LOGOUT":
						_, _ = fmt.Fprintf(conn, "* BYE\r\n%s OK done\r\n", tag)
						return
					default:
						// One response, then silence: the read deadline a
						// caller might have set is reset by this line.
						_, _ = fmt.Fprint(conn, "* 1 EXISTS\r\n")
						select {} //nolint:staticcheck // held open until the test closes the conn
					}
				}
			}()
		}
	}()
	tcp := ln.Addr().(*net.TCPAddr)
	return tcp.IP.String(), tcp.Port
}

// mustContain fails unless s contains every substring.
func mustContain(t *testing.T, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("expected %q to contain %q", s, sub)
		}
	}
}

func fmtUIDs(uids []uint32) string { return fmt.Sprint(uids) }
