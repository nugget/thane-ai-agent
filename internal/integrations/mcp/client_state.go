package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// readyConn returns the current connection, or errNotReady wrapping the
// last connect error.
func (c *serverClient) readyConn() (*clientConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn, nil
	}
	return nil, notReadyErr(c.lastErr)
}

// notReadyErr wraps cause (the last connect error, may be nil) in
// errNotReady.
func notReadyErr(cause error) error {
	switch {
	case cause == nil:
		return errNotReady
	case errors.Is(cause, errNotReady):
		return cause
	default:
		return fmt.Errorf("%w: %w", errNotReady, cause)
	}
}

// setStateLocked records s and wakes everyone waiting on a state change.
// c.mu must be held.
func (c *serverClient) setStateLocked(s connState) {
	c.state = s
	close(c.stateCh)
	c.stateCh = make(chan struct{})
}

func (c *serverClient) setConnecting() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn = nil
	c.setStateLocked(stateConnecting)
}

// setDisconnected records err as the reason calls fail. The last tool
// list is kept.
func (c *serverClient) setDisconnected(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn = nil
	c.lastErr = err
	c.setStateLocked(stateDisconnected)
}

// setReady publishes conn and its first tool list.
func (c *serverClient) setReady(conn *clientConn, set *toolSet) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn = conn
	c.lastErr = nil
	c.installLocked(set)
	c.setStateLocked(stateReady)
}

// awaitReplacement waits until a connection other than old is ready.
// It fails with errNotReady when the reconnect attempt failed.
func (c *serverClient) awaitReplacement(ctx context.Context, old *clientConn) (*clientConn, error) {
	for {
		c.mu.Lock()
		state, conn, ch, lastErr := c.state, c.conn, c.stateCh, c.lastErr
		c.mu.Unlock()
		switch {
		case state == stateReady && conn != old:
			return conn, nil
		case state == stateDisconnected:
			return nil, notReadyErr(lastErr)
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// requestReconnect asks the supervisor to replace conn (session expired).
func (c *serverClient) requestReconnect(conn *clientConn) {
	c.mu.Lock()
	c.reconnectFor = conn
	c.mu.Unlock()
	select {
	case c.reconnectCh <- struct{}{}:
	default:
	}
}

// takeReconnect consumes a reconnect request that targets conn.
func (c *serverClient) takeReconnect(conn *clientConn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reconnectFor != conn {
		return false
	}
	c.reconnectFor = nil
	return true
}

// postRefresh requests an asynchronous re-list (coalescing, non-blocking).
func (c *serverClient) postRefresh() {
	select {
	case c.refreshCh <- struct{}{}:
	default:
	}
}

// fireToolsChanged calls onToolsChanged once per published change.
// Supervisor goroutine only; no lock is held during the callback.
func (c *serverClient) fireToolsChanged() {
	c.mu.Lock()
	gen := c.listGen
	c.mu.Unlock()
	if gen == c.notifiedGen {
		return
	}
	c.notifiedGen = gen
	if c.cfg.onToolsChanged != nil {
		c.cfg.onToolsChanged()
	}
}

// closeConn closes conn's transport with a bounded ctx that survives
// the lifetime ctx's cancellation.
func (c *serverClient) closeConn(conn *clientConn) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.lifeCtx), transportCloseTimeout)
	defer cancel()
	if err := conn.tr.Close(ctx); err != nil {
		c.logger.Debug("mcp: closing transport failed", "error", err)
	}
}

// sleep waits d or until the client stops; false means stop.
func (c *serverClient) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-c.lifeCtx.Done():
		return false
	}
}

func (c *serverClient) logAttemptFailure(failures int, err error, backoff time.Duration) {
	if failures == 1 {
		c.logger.Warn("mcp: connecting to the server failed; retrying in the background",
			"error", err, "retry_in", backoff)
		return
	}
	c.logger.Debug("mcp: connecting to the server failed again",
		"error", err, "failures", failures, "retry_in", backoff)
}

func (c *serverClient) logReady(conn *clientConn, set *toolSet) {
	c.logger.Info("mcp: server connected",
		"era", conn.currentEra().String(),
		"protocol_version", conn.version,
		"server_name", logText(conn.server.Name),
		"server_version", logText(conn.server.Version),
		"tools", len(set.defs))
	if conn.instructions != "" {
		c.logger.Debug("mcp: server instructions (kept, not added to prompts)",
			"instructions", truncateUTF8(conn.instructions, serverTextCapBytes))
	}
}

// maxWarnKeys bounds warnOnce's memory against a server that invents
// endless distinct names.
const maxWarnKeys = 512

// warnOnce logs msg at WARN the first time key is seen for this server,
// and at Debug afterwards.
func (c *serverClient) warnOnce(key, msg string, args ...any) {
	c.warnMu.Lock()
	_, seen := c.warned[key]
	if !seen && len(c.warned) < maxWarnKeys {
		c.warned[key] = struct{}{}
	} else {
		seen = true
	}
	c.warnMu.Unlock()
	if seen {
		c.logger.Debug(msg, args...)
		return
	}
	c.logger.Warn(msg, args...)
}

// logTextCapBytes bounds server-supplied names echoed in logs.
const logTextCapBytes = 128

// logText caps a server-supplied string for a log attribute.
func logText(s string) string { return truncateUTF8(s, logTextCapBytes) }
