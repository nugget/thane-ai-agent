package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Values of clientConfig.protocol.
const (
	protocolAuto   = "auto"
	protocolModern = "modern"
	protocolLegacy = "legacy"
)

// Client defaults. Zero clientConfig durations mean these.
const (
	defaultConnectTimeout   = 30 * time.Second
	defaultCallTimeout      = 2 * time.Minute
	defaultProbeTimeout     = 10 * time.Second
	defaultBackoffMin       = 2 * time.Second
	defaultBackoffMax       = 60 * time.Second
	defaultListRefreshFloor = 60 * time.Second
	defaultStableAfter      = 60 * time.Second

	// refreshDebounce coalesces bursts of list_changed notifications and
	// stale-list accesses into one tools/list run.
	refreshDebounce = 250 * time.Millisecond

	// transportCloseTimeout bounds closing a transport the supervisor
	// abandons (failed attempt, lost or expired connection, shutdown).
	transportCloseTimeout = 5 * time.Second
)

// errClientClosed is the last error of a client after close.
var errClientClosed = fmt.Errorf("%w: client closed", errNotReady)

// clientConfig configures one serverClient. Zero durations mean defaults.
type clientConfig struct {
	name      string // configured server name (logs only)
	transport string // "stdio" | "http" (selects S-rules vs H-rules)
	protocol  string // "auto" | "modern" | "legacy"; "" = "auto"

	connectTimeout   time.Duration // one whole connect attempt incl. tools/list (30s)
	callTimeout      time.Duration // per tools/call when ctx has no earlier deadline (2m)
	probeTimeout     time.Duration // the server/discover era probe (10s)
	backoffMin       time.Duration // supervisor reconnect backoff start (2s)
	backoffMax       time.Duration // supervisor reconnect backoff cap (60s)
	listRefreshFloor time.Duration // modern ttl refresh floor (60s)
	stableAfter      time.Duration // a connection that lived this long ends a run of failures (60s)

	// onToolsChanged fires after any successful listing whose result
	// differs from the previous one (including the first). It is called
	// from the supervisor goroutine with no client lock held.
	onToolsChanged func()

	logger *slog.Logger
}

// withDefaults returns cfg with every zero value replaced by its default.
func (cfg clientConfig) withDefaults() clientConfig {
	fill := func(d *time.Duration, def time.Duration) {
		if *d <= 0 {
			*d = def
		}
	}
	fill(&cfg.connectTimeout, defaultConnectTimeout)
	fill(&cfg.callTimeout, defaultCallTimeout)
	fill(&cfg.probeTimeout, defaultProbeTimeout)
	fill(&cfg.backoffMin, defaultBackoffMin)
	fill(&cfg.backoffMax, defaultBackoffMax)
	fill(&cfg.listRefreshFloor, defaultListRefreshFloor)
	fill(&cfg.stableAfter, defaultStableAfter)
	if cfg.backoffMax < cfg.backoffMin {
		cfg.backoffMax = cfg.backoffMin
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	return cfg
}

// toolResult is a rendered complete tools/call result. IsError=true is a
// tool-reported failure (the server's isError), not a transport error.
type toolResult struct {
	Text    string
	IsError bool
}

// connState is the supervisor's view of one server.
type connState int

const (
	stateDisconnected connState = iota
	stateConnecting
	stateReady
)

// serverClient supervises one configured MCP server: it connects,
// negotiates the era, lists tools, reconnects with backoff, and serves
// tool calls through the current Transport.
type serverClient struct {
	cfg     clientConfig
	factory transportFactory
	logger  *slog.Logger

	lifeCtx context.Context
	cancel  context.CancelFunc

	firstDone chan struct{} // closed after the first connect attempt
	exited    chan struct{} // closed when the supervisor returns
	startOnce sync.Once

	refreshCh   chan struct{} // cap 1: re-list requested (list_changed, stale ttl)
	reconnectCh chan struct{} // cap 1: session expired (see reconnectFor)
	notifyCh    chan struct{} // cap 1: a synchronous re-list changed the tools
	listSem     chan struct{} // cap 1: one tools/list run at a time

	debounce time.Duration                     // refresh debounce; tests shorten it
	jitter   func(time.Duration) time.Duration // spreads reconnect waits; tests make it exact

	// thaneBugLogged gates the "needs a Thane fix" ERROR to once per run
	// of failed attempts; a successful connect re-arms it.
	thaneBugLogged atomic.Bool

	retiring sync.WaitGroup // retireConn goroutines; run waits for them

	mu           sync.Mutex
	state        connState
	stateCh      chan struct{} // closed and replaced on every state change
	conn         *clientConn   // non-nil only while ready
	lastErr      error         // last connect failure or loss
	lastEra      era           // era of the last successful negotiation
	reconnectFor *clientConn   // the connection a reconnect request targets
	tl           *toolSet      // nil until the first successful listing
	listGen      uint64        // bumped when the published list changes
	listedAt     time.Time     // last successful listing
	refreshBase  time.Time     // staleness base for noteAccess

	notifiedGen uint64 // supervisor goroutine only

	warnMu sync.Mutex
	warned map[string]struct{}
}

// clientConn is one negotiated connection: a transport plus what the
// negotiation learned. Fields other than eraState are written before
// the connection is published (under serverClient.mu) and read-only
// afterwards.
type clientConn struct {
	tr           Transport
	eraState     atomic.Int32 // era; read by the inbound hooks
	version      string
	server       implementation
	instructions string
}

// currentEra returns the negotiated era (eraUnknown during negotiation).
func (cc *clientConn) currentEra() era { return era(cc.eraState.Load()) }

// newServerClient builds the client. Cancelling ctx stops the client
// like close does; the Manager passes a ctx that is never cancelled, so
// only close ends it. No I/O, no goroutines.
func newServerClient(ctx context.Context, cfg clientConfig, f transportFactory) *serverClient {
	cfg = cfg.withDefaults()
	logger := cfg.logger.With("mcp_server", cfg.name)
	switch cfg.protocol {
	case protocolAuto, protocolModern, protocolLegacy:
	case "":
		cfg.protocol = protocolAuto
	default:
		logger.Warn("mcp: unknown protocol setting; using auto", "protocol", logText(cfg.protocol))
		cfg.protocol = protocolAuto
	}
	lifeCtx, cancel := context.WithCancel(ctx)
	return &serverClient{
		cfg:         cfg,
		factory:     f,
		logger:      logger,
		lifeCtx:     lifeCtx,
		cancel:      cancel,
		firstDone:   make(chan struct{}),
		exited:      make(chan struct{}),
		refreshCh:   make(chan struct{}, 1),
		reconnectCh: make(chan struct{}, 1),
		notifyCh:    make(chan struct{}, 1),
		listSem:     make(chan struct{}, 1),
		debounce:    refreshDebounce,
		jitter:      jitterDuration,
		stateCh:     make(chan struct{}),
		warned:      make(map[string]struct{}),
	}
}

// start launches the supervisor goroutine. Call exactly once; later
// calls, and calls after close, do nothing.
func (c *serverClient) start() {
	c.startOnce.Do(func() { go c.run() })
}

// firstAttempt is closed when the first connect attempt has finished,
// successfully or not.
func (c *serverClient) firstAttempt() <-chan struct{} { return c.firstDone }

// close stops the supervisor, closes the current transport bounded by
// ctx, and waits (bounded by ctx) for the supervisor to exit. Idempotent.
func (c *serverClient) close(ctx context.Context) error {
	c.cancel()
	// A client that was never started has no supervisor to wait for.
	c.startOnce.Do(func() {
		c.setDisconnected(errClientClosed)
		close(c.firstDone)
		close(c.exited)
	})
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		if err := conn.tr.Close(ctx); err != nil {
			c.logger.Debug("mcp: closing transport failed", "error", err)
		}
	}
	select {
	case <-c.exited:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("mcp: close server %s: %w", c.cfg.name, ctx.Err())
	}
}

// serveEnd says why serve returned.
type serveEnd int

const (
	serveStop      serveEnd = iota // lifetime ctx done
	serveLost                      // the transport died
	serveReconnect                 // session expired: reconnect at once
)

// run is the supervisor loop. It is the only place that connects,
// reconnects and calls onToolsChanged.
//
// A failed attempt, or a connection lost before stableAfter, is a
// failure: the next attempt waits backoff, which doubles up to
// backoffMax, so a server that crashes right after connecting is not
// respawned at a fixed short interval. A connection that lived
// stableAfter ends the run of failures: backoff returns to backoffMin.
// Every wait is jittered. Within a run of failures the first failed
// attempt and the first lost connection log at WARN, repeats at Debug.
func (c *serverClient) run() {
	first := true
	finishFirst := func() {
		if first {
			first = false
			close(c.firstDone)
		}
	}
	defer close(c.exited)
	defer c.retiring.Wait()
	defer finishFirst()
	defer c.setDisconnected(errClientClosed)

	backoff := c.cfg.backoffMin
	failures := 0     // failed attempts and quick losses in this run
	flapping := false // this run includes a connection lost before stableAfter
	for c.lifeCtx.Err() == nil {
		c.setConnecting()
		conn, set, err := c.connectAttempt()
		if err != nil {
			c.setDisconnected(err)
			finishFirst()
			if c.lifeCtx.Err() != nil {
				return
			}
			failures++
			wait := c.jitter(backoff)
			c.logAttemptFailure(failures, err, wait)
			if !c.sleep(wait) {
				return
			}
			backoff = min(2*backoff, c.cfg.backoffMax)
			continue
		}
		c.setReady(conn, set)
		readyAt := time.Now()
		c.thaneBugLogged.Store(false)
		c.logReady(conn, set, flapping)
		finishFirst()
		c.fireToolsChanged()
		end := c.serve(conn, flapping)
		if end == serveStop {
			c.closeConn(conn)
			return
		}
		quick := time.Since(readyAt) < c.cfg.stableAfter
		if !quick {
			failures, backoff, flapping = 0, c.cfg.backoffMin, false
		}
		if end == serveReconnect {
			continue // the session expired: reconnect at once
		}
		wait := c.jitter(backoff)
		c.logLost(conn, flapping, wait)
		if !c.sleep(wait) {
			return
		}
		if quick {
			failures++
			flapping = true
			backoff = min(2*backoff, c.cfg.backoffMax)
		}
	}
}

// connectAttempt runs one bounded attempt: factory, negotiation (with
// any respawn), legacy notifications/initialized, and a full tools/list.
// Every transport the attempt created is closed when it fails.
func (c *serverClient) connectAttempt() (*clientConn, *toolSet, error) {
	ctx, cancel := context.WithTimeout(c.lifeCtx, c.cfg.connectTimeout)
	defer cancel()
	conn, err := c.negotiate(ctx)
	if err != nil {
		if conn != nil {
			c.closeConn(conn)
		}
		return nil, nil, err
	}
	set, err := c.fetchTools(ctx, conn)
	if err != nil {
		c.closeConn(conn)
		return nil, nil, fmt.Errorf("mcp: %s: %w", methodToolsList, err)
	}
	return conn, set, nil
}

// serve waits on a ready connection until it is lost, expires, or the
// client stops, handling list refreshes in between. While flapping, it
// logs once the connection has lived stableAfter, because the reconnect
// itself was logged only at Debug.
func (c *serverClient) serve(conn *clientConn, flapping bool) serveEnd {
	var stable <-chan time.Time
	if flapping {
		t := time.NewTimer(c.cfg.stableAfter)
		defer t.Stop()
		stable = t.C
	}
	for {
		select {
		case <-c.lifeCtx.Done():
			return serveStop
		case <-conn.tr.Done():
			if c.lifeCtx.Err() != nil {
				return serveStop
			}
			c.setDisconnected(fmt.Errorf("%w: the server connection closed", errConnectionLost))
			c.closeConn(conn)
			return serveLost
		case <-c.reconnectCh:
			if !c.takeReconnect(conn) {
				continue
			}
			c.logger.Info("mcp: server session expired; reconnecting")
			c.retireConn(conn)
			return serveReconnect
		case <-c.refreshCh:
			if c.refresh(conn) {
				c.logger.Info("mcp: server session expired; reconnecting")
				c.retireConn(conn)
				return serveReconnect
			}
		case <-c.notifyCh:
			c.fireToolsChanged()
		case <-stable:
			stable = nil
			c.logger.Info("mcp: server connection is stable again", "up_for", c.cfg.stableAfter)
		}
	}
}

// refresh re-lists tools after the debounce. It reports true when the
// session expired and the supervisor must reconnect.
func (c *serverClient) refresh(conn *clientConn) bool {
	t := time.NewTimer(c.debounce)
	select {
	case <-t.C:
	case <-c.lifeCtx.Done():
		t.Stop()
		return false
	case <-conn.tr.Done():
		t.Stop()
		return false // serve sees Done next
	}
	select {
	case <-c.refreshCh: // coalesce requests made during the debounce
	default:
	}
	ctx, cancel := context.WithTimeout(c.lifeCtx, c.cfg.connectTimeout)
	defer cancel()
	err := c.relist(ctx, conn)
	switch {
	case err == nil:
		c.fireToolsChanged()
	case errors.Is(err, errSessionExpired):
		return true
	default:
		select {
		case <-conn.tr.Done(): // serve reports the loss
		default:
			if c.lifeCtx.Err() == nil {
				c.logger.Warn("mcp: refreshing the tool list failed; keeping the previous list",
					append([]any{"error", err}, httpStatusLogArgs(err)...)...)
			}
		}
	}
	return false
}

// probe is the connwatch health check (AM-7). Not ready: returns the
// last connect error (wrapping errNotReady) with no I/O. Ready: legacy →
// ping, modern → server/discover; any JSON-RPC response (result or
// error) counts as healthy. Failures are reported, never acted on,
// except errSessionExpired, which triggers an immediate re-initialize.
func (c *serverClient) probe(ctx context.Context) error {
	conn, err := c.readyConn()
	if err != nil {
		return err
	}
	method := methodPing
	if conn.currentEra() == eraModern {
		method = methodDiscover
	}
	if _, err := conn.call(ctx, method, nil, conn.frame(false)); err != nil {
		if errors.Is(err, errSessionExpired) {
			c.requestReconnect(conn)
		}
		return fmt.Errorf("mcp: health check (%s): %w", method, err)
	}
	return nil
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
