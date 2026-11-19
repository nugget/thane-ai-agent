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

// errCallCutOff wraps the error of a tools/call that was in flight when
// the client closed. Whatever the server or transport reported (stdio's
// -32000 "Connection closed", a lost HTTP connection), the shutdown ended
// the call, and its outcome is unknown.
var errCallCutOff = fmt.Errorf("%w during the call", errClientClosed)

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
	cfg.connectTimeout = durationOr(cfg.connectTimeout, defaultConnectTimeout)
	cfg.callTimeout = durationOr(cfg.callTimeout, defaultCallTimeout)
	cfg.probeTimeout = durationOr(cfg.probeTimeout, defaultProbeTimeout)
	cfg.backoffMin = durationOr(cfg.backoffMin, defaultBackoffMin)
	cfg.backoffMax = durationOr(cfg.backoffMax, defaultBackoffMax)
	cfg.listRefreshFloor = durationOr(cfg.listRefreshFloor, defaultListRefreshFloor)
	cfg.stableAfter = durationOr(cfg.stableAfter, defaultStableAfter)
	if cfg.backoffMax < cfg.backoffMin {
		cfg.backoffMax = cfg.backoffMin
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	return cfg
}

// durationOr returns d, or def when d is not positive: an unset or
// negative duration setting means "use the default".
func durationOr(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
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

	// exitWarned gates the stdio exit WARN across this server's
	// processes (transportHooks.exitWarned); serve re-arms it once a
	// connection has lived stableAfter.
	exitWarned atomic.Bool

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

	warnings logDeduper // warnOnce
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
		warnings:    logDeduper{max: maxWarnKeys},
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
// attempt, the first lost connection and the first stdio process exit
// log at WARN, repeats at Debug.
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
		c.thaneBugLogged.Store(false)
		c.logReady(conn, set, flapping)
		finishFirst()
		c.fireToolsChanged()
		end, stable := c.serve(conn, flapping)
		if end == serveStop {
			c.closeConn(conn)
			return
		}
		if stable {
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
		if !stable {
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
		return nil, nil, fmt.Errorf("%s: %w", methodToolsList, err)
	}
	return conn, set, nil
}

// serve waits on a ready connection until it is lost, expires, or the
// client stops, handling list refreshes in between, and reports whether
// the connection lived stableAfter, which ends the run of failures. A
// stable connection re-arms the stdio exit WARN and, while flapping,
// logs that it is stable again, because its connect was logged only at
// Debug. The timer does that at stableAfter, before the process can
// exit. A refresh runs inline and can outlast stableAfter, though, and
// when the connection then ends, the fired timer and the end are both
// ready and the select picks one at random. So how long the connection
// lived decides, and serve applies a fired timer's effects before it
// returns.
func (c *serverClient) serve(conn *clientConn, flapping bool) (serveEnd, bool) {
	readyAt := time.Now()
	timer := time.NewTimer(c.cfg.stableAfter)
	defer timer.Stop()
	stable := false
	markStable := func() {
		if stable {
			return
		}
		stable = true
		c.exitWarned.Store(false)
		if flapping {
			c.logger.Info("mcp: server connection is stable again", "up_for", c.cfg.stableAfter)
		}
	}
	end := c.await(conn, timer.C, markStable)
	select {
	case <-timer.C:
		markStable()
	default:
	}
	if time.Since(readyAt) >= c.cfg.stableAfter {
		markStable()
	}
	switch end {
	case serveLost:
		c.setDisconnected(fmt.Errorf("%w: the server connection closed", errConnectionLost))
		c.closeConn(conn)
	case serveReconnect:
		c.logger.Info("mcp: server session expired; reconnecting")
		c.retireConn(conn)
	}
	return end, stable
}

// await is serve's loop: it returns how the connection ended, and calls
// markStable when the stable timer fires.
func (c *serverClient) await(conn *clientConn, stableTimer <-chan time.Time, markStable func()) serveEnd {
	for {
		select {
		case <-c.lifeCtx.Done():
			return serveStop
		case <-conn.tr.Done():
			if c.lifeCtx.Err() != nil {
				return serveStop
			}
			return serveLost
		case <-c.reconnectCh:
			if c.takeReconnect(conn) {
				return serveReconnect
			}
		case <-c.refreshCh:
			if c.refresh(conn) {
				return serveReconnect
			}
		case <-c.notifyCh:
			c.fireToolsChanged()
		case <-stableTimer:
			markStable()
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
