package email

import (
	"context"
	"fmt"
	"net"
	"time"
)

// This file holds the pieces that keep one IMAP operation from
// outliving its caller: the watchdog that bounds a command, the bound
// applied before go-imap manages the connection, and the
// classification that tells a watchdog-ended command from a
// server-ended one.

// guard binds one operation to ctx. A watcher closes the connection
// when ctx ends or, when ctx has no earlier deadline, when
// defaultOpTimeout passes, so a blocked Wait returns instead of
// holding the mutex until TCP gives up. The watcher is the only bound:
// a connection deadline would not survive the first server response,
// because go-imap resets the read deadline after every response it
// parses. The returned function ends the guard and records whether the
// timeout, rather than the caller, ended the operation; call it as
// soon as the command completes. Caller must hold c.mu.
func (c *Client) guard(ctx context.Context) func() {
	client := c.client
	if client == nil {
		return func() {}
	}
	c.watchdogFired = false
	opCtx, cancel := context.WithTimeout(ctx, defaultOpTimeout)
	stop := make(chan struct{})
	fired := make(chan bool, 1)
	go func() {
		select {
		case <-opCtx.Done():
			_ = client.Close()
			fired <- ctx.Err() == nil
		case <-stop:
			fired <- false
		}
	}()
	return func() {
		// Stop the watcher before cancelling the derived context: with
		// both ready at once, select would pick at random and could
		// close a healthy connection on the way out.
		close(stop)
		c.watchdogFired = <-fired
		cancel()
	}
}

// withConnBound runs fn against a connection that go-imap does not yet
// manage, bounding it by the caller's context or dialTimeout: the
// connection gets a deadline and a watcher closes it on cancellation.
func withConnBound(ctx context.Context, conn net.Conn, fn func() error) error {
	deadline := time.Now().Add(dialTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()
	err := fn()
	close(stop)
	_ = conn.SetDeadline(time.Time{})
	return err
}

// classify maps an error to a FailureKind, reading the watchdog flag
// the last guard left: a connection the watchdog closed reads as a
// timeout rather than as the closed-connection error it surfaces as.
// Caller must hold c.mu.
func (c *Client) classify(ctx context.Context, err error) FailureKind {
	if c.watchdogFired {
		c.watchdogFired = false
		return FailureTimeout
	}
	return classifyIMAPError(ctx, err)
}

// classified classifies err and, for a timeout, names the bound in
// the error so the text says how long the server was given.
func (c *Client) classified(ctx context.Context, err error) (FailureKind, error) {
	kind := c.classify(ctx, err)
	if kind == FailureTimeout {
		err = fmt.Errorf("no answer within %s: %w", defaultOpTimeout, err)
	}
	return kind, err
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
