package email

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"mime"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"
)

// dialTimeout bounds the TCP connect and TLS handshake of a new IMAP
// or SMTP connection when the caller's context has no earlier
// deadline.
const dialTimeout = 30 * time.Second

// defaultOpTimeout bounds one IMAP operation when the caller's context
// has no deadline. A FETCH of a large body over a slow link can
// legitimately take a while; a minute is generous for anything the
// tools do.
const defaultOpTimeout = 60 * time.Second

// logoutTimeout bounds the courtesy LOGOUT on Close.
const logoutTimeout = 5 * time.Second

// tlsRootCAs, when non-nil, replaces the system roots for IMAP and
// SMTP connections. It exists so tests can present a self-signed
// server certificate without disabling verification; production
// leaves it nil.
var tlsRootCAs *x509.CertPool

// tlsConfigFor is the one TLS configuration both transports use:
// server-name verification, no version older than TLS 1.2.
func tlsConfigFor(host string, nextProtos ...string) *tls.Config {
	return &tls.Config{
		ServerName: host,
		MinVersion: tls.VersionTLS12,
		NextProtos: nextProtos,
		RootCAs:    tlsRootCAs,
	}
}

// Client is a single-account IMAP client over go-imap/v2 with lazy
// connection, reconnection, and mutex-serialized access. All methods
// are safe for concurrent use; see the package documentation for why
// operations are serialized and how the caller's context bounds them.
type Client struct {
	name   string
	cfg    IMAPConfig
	logger *slog.Logger

	mu     sync.Mutex
	conn   net.Conn
	client *imapclient.Client
}

// NewClient creates an IMAP client for the named account. The
// connection is established on first use.
func NewClient(name string, cfg IMAPConfig, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		name:   name,
		cfg:    cfg,
		logger: logger,
	}
}

// Name returns the account name this client serves.
func (c *Client) Name() string { return c.name }

// Connect establishes the IMAP connection and authenticates. Callers
// do not need it — every operation connects on demand — but it lets a
// caller surface a bad credential at startup rather than on the first
// tool call.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connectLocked(ctx)
}

// connectLocked dials, greets, and logs in. Caller must hold c.mu.
func (c *Client) connectLocked(ctx context.Context) error {
	c.closeLocked()

	addr := net.JoinHostPort(c.cfg.Host, strconv.Itoa(c.cfg.Port))
	c.logger.Debug("connecting to IMAP server", "host", c.cfg.Host, "port", c.cfg.Port, "tls", c.cfg.TLS)

	dialer := &net.Dialer{Timeout: dialTimeout}
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return c.wrap(ctx, "dial IMAP "+addr, "", 0, err)
	}

	conn := raw
	if c.cfg.TLS {
		tlsConn := tls.Client(raw, tlsConfigFor(c.cfg.Host, "imap"))
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return c.wrap(ctx, "TLS handshake with "+addr, "", 0, err)
		}
		conn = tlsConn
	}

	client := imapclient.New(conn, &imapclient.Options{
		WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader},
	})
	c.conn = conn
	c.client = client

	release := c.guard(ctx)
	err = client.WaitGreeting()
	if err == nil {
		err = client.Login(c.cfg.Username, c.cfg.Password).Wait()
	}
	release()
	if err != nil {
		c.closeLocked()
		return c.wrap(ctx, "login to IMAP as "+c.cfg.Username, "", 0, err)
	}

	c.logger.Info("IMAP connected", "host", c.cfg.Host, "user", c.cfg.Username)
	return nil
}

// ensureConnected probes the current connection and reconnects when
// it is gone or unresponsive. Caller must hold c.mu.
func (c *Client) ensureConnected(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return c.wrap(ctx, "IMAP operation", "", 0, err)
	}
	if c.client != nil {
		select {
		case <-c.client.Closed():
			c.logger.Debug("IMAP connection closed, reconnecting", "host", c.cfg.Host)
		default:
			release := c.guard(ctx)
			err := c.client.Noop().Wait()
			release()
			if err == nil {
				return nil
			}
			c.logger.Debug("IMAP connection stale, reconnecting", "host", c.cfg.Host, "error", err)
		}
	}
	return c.connectLocked(ctx)
}

// guard binds one operation to ctx: the connection gets a deadline
// derived from ctx (or defaultOpTimeout), and a watcher closes the
// connection if ctx is cancelled mid-command so the blocked Wait
// returns instead of holding the mutex until TCP gives up. The
// returned function ends the guard and clears the deadline; call it
// as soon as the command completes. Caller must hold c.mu.
func (c *Client) guard(ctx context.Context) func() {
	conn, client := c.conn, c.client
	if conn == nil || client == nil {
		return func() {}
	}
	deadline := time.Now().Add(defaultOpTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-stop:
		}
	}()
	return func() {
		close(stop)
		_ = conn.SetDeadline(time.Time{})
	}
}

// wrap turns a failure into a classified *ClientError for this account.
func (c *Client) wrap(ctx context.Context, op, folder string, uid uint32, err error) error {
	if err == nil {
		return nil
	}
	var existing *ClientError
	if errors.As(err, &existing) {
		return err
	}
	return &ClientError{
		Account: c.name,
		Op:      op,
		Folder:  folder,
		UID:     uid,
		Kind:    classifyIMAPError(ctx, err),
		Err:     err,
	}
}

// Ping checks that the IMAP connection is alive, reconnecting if
// needed. connwatch calls it with a bounded context; a hung server
// releases the account when that context expires.
func (c *Client) Ping(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ensureConnected(ctx)
}

// Close logs out and closes the IMAP connection. A courtesy LOGOUT is
// attempted with a short deadline so the server records a clean
// disconnect; the connection is closed regardless.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), logoutTimeout)
	defer cancel()
	release := c.guard(ctx)
	logoutErr := c.client.Logout().Wait()
	release()
	c.closeLocked()
	if logoutErr != nil && ctx.Err() == nil {
		c.logger.Debug("IMAP logout returned error", "host", c.cfg.Host, "error", logoutErr)
	}
	return nil
}

// closeLocked drops the connection without a LOGOUT. Caller must hold
// c.mu.
func (c *Client) closeLocked() {
	if c.client != nil {
		_ = c.client.Close()
	}
	c.client = nil
	c.conn = nil
}

// selectFolder selects (or, when readOnly, examines) a mailbox. Caller
// must hold c.mu and have called ensureConnected. A folder the account
// lacks becomes [FailureFolderNotFound] with the real folder list
// attached.
func (c *Client) selectFolder(ctx context.Context, folder string, readOnly bool) (*imap.SelectData, error) {
	folder = normalizeFolder(folder)
	var opts *imap.SelectOptions
	if readOnly {
		opts = &imap.SelectOptions{ReadOnly: true}
	}
	release := c.guard(ctx)
	data, err := c.client.Select(folder, opts).Wait()
	release()
	if err != nil {
		return nil, c.folderError(ctx, "select folder", folder, err)
	}
	c.logger.Debug("IMAP folder selected",
		"folder", folder,
		"read_only", readOnly,
		"messages", data.NumMessages,
		"uid_validity", data.UIDValidity,
	)
	return data, nil
}

// folderError classifies a failure on a named folder. A NO reply to
// SELECT or MOVE almost always means the folder does not exist, even
// when the server omits the NONEXISTENT/TRYCREATE code, so an
// unclassified NO on a folder operation is treated as not-found and the
// account's folder list is attached. Caller must hold c.mu.
func (c *Client) folderError(ctx context.Context, op, folder string, err error) error {
	kind := classifyIMAPError(ctx, err)
	if kind == FailureUnclassified {
		var imapErr *imap.Error
		if errors.As(err, &imapErr) && imapErr.Type == imap.StatusResponseTypeNo {
			kind = FailureFolderNotFound
		}
	}
	cErr := &ClientError{Account: c.name, Op: op, Folder: folder, Kind: kind, Err: err}
	if kind == FailureFolderNotFound {
		cErr.Available = c.folderNamesLocked(ctx)
	}
	return cErr
}

// folderNamesLocked lists the account's selectable folder names for
// error text. It is best-effort: a failure yields nil and the error
// text falls back to telling the caller to list folders. Caller must
// hold c.mu.
func (c *Client) folderNamesLocked(ctx context.Context) []string {
	if c.client == nil {
		return nil
	}
	release := c.guard(ctx)
	mailboxes, err := c.client.List("", "*", nil).Collect()
	release()
	if err != nil {
		c.logger.Debug("listing folders for error text failed", "error", err)
		return nil
	}
	names := make([]string, 0, len(mailboxes))
	for _, mbox := range mailboxes {
		if hasAttr(mbox.Attrs, imap.MailboxAttrNoSelect) {
			continue
		}
		names = append(names, mbox.Mailbox)
	}
	slices.Sort(names)
	return names
}

// MailboxStatus returns the STATUS counters for a folder without
// selecting it, which is how the poller seeds and validates its
// high-water mark.
func (c *Client) MailboxStatus(ctx context.Context, folder string) (MailboxStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return MailboxStatus{}, err
	}
	folder = normalizeFolder(folder)

	release := c.guard(ctx)
	data, err := c.client.Status(folder, &imap.StatusOptions{
		NumMessages: true,
		NumUnseen:   true,
		UIDNext:     true,
		UIDValidity: true,
	}).Wait()
	release()
	if err != nil {
		return MailboxStatus{}, c.folderError(ctx, "status of folder", folder, err)
	}

	status := MailboxStatus{
		Folder:      folder,
		UIDNext:     uint32(data.UIDNext),
		UIDValidity: data.UIDValidity,
	}
	if data.NumMessages != nil {
		status.Messages = *data.NumMessages
	}
	if data.NumUnseen != nil {
		status.Unseen = *data.NumUnseen
	}
	return status, nil
}

// AppendMessage stores a complete RFC 5322 message in the named folder
// with the given flags and the current time as its internal date. The
// result carries the new UID when the server returned APPENDUID
// (UIDPLUS or IMAP4rev2); otherwise UID is zero and the message must
// be found by Message-ID. A missing folder is [FailureFolderNotFound].
func (c *Client) AppendMessage(ctx context.Context, folder string, msg []byte, flags []imap.Flag) (AppendResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnected(ctx); err != nil {
		return AppendResult{}, err
	}
	folder = normalizeFolder(folder)

	opts := &imap.AppendOptions{
		Flags: flags,
		Time:  time.Now(),
	}

	release := c.guard(ctx)
	appendCmd := c.client.Append(folder, int64(len(msg)), opts)
	var data *imap.AppendData
	_, err := appendCmd.Write(msg)
	if err == nil {
		err = appendCmd.Close()
	}
	if err == nil {
		data, err = appendCmd.Wait()
	}
	release()
	if err != nil {
		return AppendResult{}, c.folderError(ctx, "append to folder", folder, err)
	}

	result := AppendResult{Folder: folder}
	if data != nil {
		result.UID = uint32(data.UID)
		result.UIDValidity = data.UIDValidity
	}
	return result, nil
}

// hasAttr reports whether attrs contains attr.
func hasAttr(attrs []imap.MailboxAttr, attr imap.MailboxAttr) bool {
	for _, a := range attrs {
		if a == attr {
			return true
		}
	}
	return false
}
