package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
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

// tools returns the last good list in server order (deduped by name,
// first wins; on HTTP, tools with invalid x-mcp-header annotations are
// already dropped). nil until the first successful listing; kept while
// disconnected. No I/O. The slice is shared and must not be mutated.
func (c *serverClient) tools() []toolDefinition {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tl == nil {
		return nil
	}
	return c.tl.defs
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

// retireConn ends a connection whose session expired. On a transport
// that can retire (HTTP), requests already in flight keep running to
// their own answers instead of being aborted: the server rejects them
// unprocessed, so callTool retries them on the replacement connection,
// where an abort would report an unknown outcome. The wait is bounded by
// callTimeout and ends early when the client closes; then conn is
// closed. Other transports are closed at once.
func (c *serverClient) retireConn(conn *clientConn) {
	r, ok := conn.tr.(interface{ retire(context.Context) })
	if !ok {
		c.closeConn(conn)
		return
	}
	c.retiring.Add(1)
	go func() {
		defer c.retiring.Done()
		ctx, cancel := context.WithTimeout(c.lifeCtx, c.cfg.callTimeout)
		r.retire(ctx)
		cancel()
		c.closeConn(conn)
	}()
}

// jitterFraction is how far jitterDuration moves a wait either way.
const jitterFraction = 0.2

// jitterDuration spreads d uniformly over d±20%, so servers that failed
// together do not reconnect in lockstep.
func jitterDuration(d time.Duration) time.Duration {
	spread := int64(float64(d) * 2 * jitterFraction)
	if spread <= 0 {
		return d
	}
	return d - time.Duration(spread/2) + time.Duration(rand.Int64N(spread+1))
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

// logAttemptFailure logs a failed connect attempt: WARN for the first
// failure of a run, Debug for repeats.
func (c *serverClient) logAttemptFailure(failures int, err error, retryIn time.Duration) {
	status := httpStatusLogArgs(err)
	if failures == 1 {
		c.logger.Warn("mcp: connecting to the server failed; retrying in the background",
			append([]any{"error", err, "retry_in", retryIn}, status...)...)
		return
	}
	c.logger.Debug("mcp: connecting to the server failed again",
		append([]any{"error", err, "failures", failures, "retry_in", retryIn}, status...)...)
}

// logLost logs a lost connection: WARN, or Debug while the run is
// already flapping (an earlier connection of it was lost quickly).
func (c *serverClient) logLost(conn *clientConn, flapping bool, retryIn time.Duration) {
	const msg = "mcp: server connection lost; reconnecting"
	args := []any{"era", conn.currentEra().String(), "retry_in", retryIn}
	if flapping {
		c.logger.Debug(msg, args...)
		return
	}
	c.logger.Warn(msg, args...)
}

// logReady logs a connect at INFO, or at Debug while the run is
// flapping (serve logs at INFO once the connection proves stable).
func (c *serverClient) logReady(conn *clientConn, set *toolSet, flapping bool) {
	level := slog.LevelInfo
	if flapping {
		level = slog.LevelDebug
	}
	c.logger.Log(c.lifeCtx, level, "mcp: server connected",
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
	c.warnings.log(c.logger, slog.LevelWarn, key, msg, args...)
}

// logDeduper logs a message at its level the first time its key is seen
// and at Debug afterwards, so a server that repeats one problem on every
// listing or call is reported once. It remembers at most max keys: a
// server that invents endless distinct names cannot grow it without
// bound, and every key past the cap logs at Debug. A logDeduper with
// max set is ready to use; it must not be copied after first use.
type logDeduper struct {
	max  int
	mu   sync.Mutex
	seen map[string]struct{}
}

// log logs msg at level if key is new, else at Debug.
func (d *logDeduper) log(logger *slog.Logger, level slog.Level, key, msg string, args ...any) {
	if !d.first(key) {
		level = slog.LevelDebug
	}
	logger.Log(context.Background(), level, msg, args...)
}

// first reports whether key is new, remembering it while there is room.
func (d *logDeduper) first(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[key]; ok || len(d.seen) >= d.max {
		return false
	}
	if d.seen == nil {
		d.seen = make(map[string]struct{})
	}
	d.seen[key] = struct{}{}
	return true
}

// logTextCapBytes bounds server-supplied names echoed in logs.
const logTextCapBytes = 128

// logText caps a server-supplied string for a log attribute.
func logText(s string) string { return truncateUTF8(s, logTextCapBytes) }
