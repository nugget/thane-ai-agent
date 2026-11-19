package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// clientTToolList is a server-side tool list tests can swap at runtime.
type clientTToolList struct {
	mu    sync.Mutex
	tools []string
}

func (l *clientTToolList) set(tools ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tools = tools
}

func (l *clientTToolList) handler(_ context.Context, _ *clientTFakeTransport, _ *Request, _ callInfo) (*Response, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return clientTToolsPage(nil, l.tools...), nil
}

func TestClient_FirstAttemptPublishesTools(t *testing.T) {
	srv := &clientTFakeServer{handle: clientTLegacyRoutes("", clientTTool("a"), clientTTool("b")).handler()}
	cfg := clientTConfig(transportStdio, "")
	var changed atomic.Int32
	cfg.onToolsChanged = func() { changed.Add(1) }
	c := clientTNew(t, srv, cfg)
	if c.tools() != nil {
		t.Fatal("tools() before the first listing should be nil")
	}
	clientTStart(t, c)
	// Ready and listed before firstAttempt closes (Manager.Start relies on it).
	if _, err := c.readyConn(); err != nil {
		t.Fatalf("not ready after the first attempt: %v", err)
	}
	if got := clientTToolNames(c); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("tools = %q", got)
	}
	clientTEventually(t, "onToolsChanged", func() bool { return changed.Load() == 1 })
	c.start() // a second start is a no-op
	time.Sleep(20 * time.Millisecond)
	if srv.spawns() != 1 || changed.Load() != 1 {
		t.Errorf("spawns = %d, onToolsChanged = %d; want 1, 1", srv.spawns(), changed.Load())
	}
}

func TestClient_FirstAttemptFailureKeepsRetrying(t *testing.T) {
	srv := &clientTFakeServer{
		handle: clientTLegacyRoutes("", clientTTool("a")).handler(),
		dialErr: func(spawn int) error {
			if spawn <= 3 {
				return fmt.Errorf("%w: connection refused", errUnreachable)
			}
			return nil
		},
	}
	cfg := clientTConfig(transportHTTP, "")
	cfg.backoffMin, cfg.backoffMax = 20*time.Millisecond, 40*time.Millisecond
	var changed atomic.Int32
	cfg.onToolsChanged = func() { changed.Add(1) }
	c := clientTNew(t, srv, cfg)
	clientTStart(t, c)

	err := c.probe(t.Context())
	if !errors.Is(err, errNotReady) || !errors.Is(err, errUnreachable) {
		t.Fatalf("probe while down = %v, want errNotReady wrapping the connect error", err)
	}
	if _, err := c.callTool(t.Context(), "a", nil); !errors.Is(err, errNotReady) {
		t.Fatalf("callTool while down = %v, want errNotReady", err)
	}
	if c.tools() != nil {
		t.Error("tools() should stay nil before any listing")
	}
	clientTReadyConn(t, c)
	if got := clientTToolNames(c); !slices.Equal(got, []string{"a"}) {
		t.Errorf("tools = %q", got)
	}
	clientTEventually(t, "onToolsChanged", func() bool { return changed.Load() == 1 })
	if err := c.probe(t.Context()); err != nil {
		t.Errorf("probe when ready = %v", err)
	}
}

func TestClient_BackoffDoublesToTheCap(t *testing.T) {
	var mu sync.Mutex
	var dials []time.Time
	srv := &clientTFakeServer{
		handle: clientTLegacyBase().handler(),
		dialErr: func(spawn int) error {
			mu.Lock()
			dials = append(dials, time.Now())
			mu.Unlock()
			if spawn <= 5 {
				return errUnreachable
			}
			return nil
		},
	}
	cfg := clientTConfig(transportHTTP, "")
	cfg.backoffMin, cfg.backoffMax = 10*time.Millisecond, 40*time.Millisecond
	buf := &coreTLogBuffer{}
	cfg.logger = buf.logger()
	c := clientTNew(t, srv, cfg)
	c.start()
	clientTReadyConn(t, c)
	mu.Lock()
	defer mu.Unlock()
	want := []time.Duration{10, 20, 40, 40, 40}
	for i, w := range want {
		if gap := dials[i+1].Sub(dials[i]); gap < w*time.Millisecond {
			t.Errorf("gap %d = %v, want >= %v", i, gap, w*time.Millisecond)
		}
	}
	warns := strings.Count(buf.String(), "level=WARN msg=\"mcp: connecting to the server failed")
	if warns != 1 {
		t.Errorf("WARN lines for the failure run = %d, want 1 (repeats at Debug)\n%s", warns, buf.String())
	}
}

func TestClient_ReconnectsAfterConnectionLoss(t *testing.T) {
	srv := &clientTFakeServer{handle: clientTLegacyRoutes("", clientTTool("a")).handler()}
	cfg := clientTConfig(transportStdio, "")
	cfg.backoffMin = 150 * time.Millisecond
	var changed atomic.Int32
	cfg.onToolsChanged = func() { changed.Add(1) }
	c := clientTNew(t, srv, cfg)
	clientTStart(t, c)
	first := clientTReadyConn(t, c)

	srv.last().lose()
	clientTEventually(t, "disconnected", func() bool {
		_, err := c.readyConn()
		return err != nil
	})
	if _, err := c.callTool(t.Context(), "a", nil); !errors.Is(err, errNotReady) || !errors.Is(err, errConnectionLost) {
		t.Errorf("callTool while reconnecting = %v, want errNotReady wrapping errConnectionLost", err)
	}
	before := len(srv.messages())
	if err := c.probe(t.Context()); !errors.Is(err, errNotReady) {
		t.Errorf("probe while reconnecting = %v", err)
	}
	if len(srv.messages()) != before {
		t.Error("probe did I/O while not ready")
	}
	if got := clientTToolNames(c); !slices.Equal(got, []string{"a"}) {
		t.Errorf("tools while disconnected = %q, want the last list kept", got)
	}

	second := clientTReadyConn(t, c)
	if second == first || srv.spawns() != 2 {
		t.Fatalf("spawns = %d, want a new connection", srv.spawns())
	}
	// The restarted server is re-initialized, skipping the probe
	// because the era is known.
	var methods []string
	for _, m := range srv.last().sent() {
		methods = append(methods, m.method)
	}
	if want := []string{methodInitialize, methodInitialized, methodToolsList}; !slices.Equal(methods, want) {
		t.Errorf("second connection methods = %q, want %q", methods, want)
	}
	if !srv.transportAt(0).isClosed() {
		t.Error("the lost transport was not closed")
	}
	if res, err := c.callTool(t.Context(), "a", nil); err != nil || res.IsError {
		t.Errorf("callTool after reconnect = %+v, %v", res, err)
	}
	time.Sleep(10 * time.Millisecond)
	if changed.Load() != 1 {
		t.Errorf("onToolsChanged = %d, want 1 (the list did not change)", changed.Load())
	}
}

func TestClient_Probe(t *testing.T) {
	cases := []struct {
		name       string
		routes     func() clientTRoutes
		wantMethod string
		wantErr    error
	}{
		{name: "legacy pings", routes: clientTLegacyBase, wantMethod: methodPing},
		{name: "legacy JSON-RPC error still proves liveness",
			routes:     clientTOverride(clientTLegacyBase, map[string]clientTHandler{methodPing: clientTReply(clientTRPCErr(codeMethodNotFound, ""), nil)}),
			wantMethod: methodPing},
		{name: "modern discovers and never pings", routes: clientTModernBase, wantMethod: methodDiscover},
		{name: "modern JSON-RPC error still proves liveness",
			routes: clientTOverride(clientTModernBase, map[string]clientTHandler{
				methodDiscover: clientTSeq(clientTReply(clientTOK(clientTModernResult), nil), clientTReply(clientTRPCErr(-32603, ""), nil)),
			}),
			wantMethod: methodDiscover},
		{name: "timeout is reported, nothing torn down",
			routes:     clientTOverride(clientTLegacyBase, map[string]clientTHandler{methodPing: clientTHang}),
			wantMethod: methodPing, wantErr: context.DeadlineExceeded},
		{name: "HTTP status is reported, nothing torn down",
			routes:     clientTOverride(clientTLegacyBase, map[string]clientTHandler{methodPing: clientTReply(nil, clientTStatus(503, nil))}),
			wantMethod: methodPing, wantErr: &httpStatusError{}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			srv := &clientTFakeServer{handle: tt.routes().handler()}
			c := clientTNew(t, srv, clientTConfig(transportHTTP, ""))
			clientTStart(t, c)
			conn := clientTReadyConn(t, c)
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			err := c.probe(ctx)
			switch target := tt.wantErr.(type) {
			case nil:
				if err != nil {
					t.Fatalf("probe = %v, want healthy", err)
				}
			case *httpStatusError:
				if !errors.As(err, &target) {
					t.Fatalf("probe = %v, want an HTTP status error", err)
				}
			default:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("probe = %v, want %v", err, tt.wantErr)
				}
			}
			msgs := srv.messages()
			last := msgs[len(msgs)-1]
			if last.method != tt.wantMethod || last.info.cancellable {
				t.Errorf("probe sent %s (cancellable %v), want non-cancellable %s", last.method, last.info.cancellable, tt.wantMethod)
			}
			if srv.count(methodPing) > 0 && conn.currentEra() == eraModern {
				t.Error("a modern server was pinged")
			}
			time.Sleep(10 * time.Millisecond)
			if cur, rerr := c.readyConn(); rerr != nil || cur != conn || srv.spawns() != 1 {
				t.Error("a failed health check tore the connection down")
			}
		})
	}
}

func TestClient_ProbeSessionExpiredReconnects(t *testing.T) {
	expired := fmt.Errorf("%w: %w", errSessionExpired, clientTStatus(404, nil))
	routes := clientTLegacyRoutes("", clientTTool("a"))
	routes[methodPing] = func(_ context.Context, tr *clientTFakeTransport, _ *Request, _ callInfo) (*Response, error) {
		if tr.spawn == 1 {
			return nil, expired
		}
		return clientTOK(`{}`), nil
	}
	srv := &clientTFakeServer{handle: routes.handler()}
	c := clientTNew(t, srv, clientTConfig(transportHTTP, ""))
	clientTStart(t, c)
	first := clientTReadyConn(t, c)
	if err := c.probe(t.Context()); !errors.Is(err, errSessionExpired) {
		t.Fatalf("probe = %v, want errSessionExpired", err)
	}
	clientTEventually(t, "re-initialized session", func() bool {
		cur, err := c.readyConn()
		return err == nil && cur != first
	})
	if !srv.transportAt(0).isClosed() {
		t.Error("the expired session's transport was not closed")
	}
	if err := c.probe(t.Context()); err != nil {
		t.Errorf("probe on the new session = %v", err)
	}
}

func TestClient_Close(t *testing.T) {
	t.Run("running client", func(t *testing.T) {
		srv := &clientTFakeServer{handle: clientTLegacyRoutes("", clientTTool("a")).handler()}
		c := clientTNew(t, srv, clientTConfig(transportStdio, ""))
		clientTStart(t, c)
		clientTReadyConn(t, c)
		for i := range 2 {
			if err := c.close(t.Context()); err != nil {
				t.Fatalf("close #%d = %v", i+1, err)
			}
		}
		if !srv.last().isClosed() {
			t.Error("transport not closed")
		}
		if _, err := c.callTool(t.Context(), "a", nil); !errors.Is(err, errNotReady) {
			t.Errorf("callTool after close = %v", err)
		}
		if err := c.probe(t.Context()); !errors.Is(err, errNotReady) {
			t.Errorf("probe after close = %v", err)
		}
	})
	t.Run("never started", func(t *testing.T) {
		srv := &clientTFakeServer{handle: clientTLegacyBase().handler()}
		c := clientTNew(t, srv, clientTConfig(transportStdio, ""))
		if err := c.close(t.Context()); err != nil {
			t.Fatal(err)
		}
		select {
		case <-c.firstAttempt():
		default:
			t.Error("firstAttempt not closed by close")
		}
		c.start()
		time.Sleep(10 * time.Millisecond)
		if srv.spawns() != 0 {
			t.Error("start after close connected")
		}
	})
	t.Run("during a connect attempt", func(t *testing.T) {
		routes := clientTLegacyBase()
		routes[methodDiscover] = clientTHang
		srv := &clientTFakeServer{handle: routes.handler()}
		cfg := clientTConfig(transportStdio, protocolModern)
		cfg.connectTimeout = time.Minute
		c := clientTNew(t, srv, cfg)
		c.start()
		clientTEventually(t, "discover sent", func() bool { return srv.count(methodDiscover) == 1 })
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		if err := c.close(ctx); err != nil {
			t.Fatal(err)
		}
		if !srv.last().isClosed() {
			t.Error("the attempt's transport was not closed")
		}
	})
	t.Run("bounded by ctx", func(t *testing.T) {
		srv := &clientTFakeServer{handle: clientTLegacyBase().handler()}
		c := clientTNew(t, srv, clientTConfig(transportStdio, ""))
		clientTStart(t, c)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_ = c.close(ctx) // may or may not win the race; must not block
	})
}

func TestClient_ListChangedRefreshes(t *testing.T) {
	list := &clientTToolList{}
	list.set(clientTTool("a"))
	var failList atomic.Bool
	routes := clientTLegacyBase()
	routes[methodToolsList] = clientTLegacyGuard(func(ctx context.Context, tr *clientTFakeTransport, req *Request, info callInfo) (*Response, error) {
		if failList.Load() {
			return clientTRPCErr(-32603, ""), nil
		}
		return list.handler(ctx, tr, req, info)
	})
	srv := &clientTFakeServer{handle: routes.handler()}
	var changed atomic.Int32
	c := clientTNew(t, srv, clientTConfig(transportStdio, ""))
	c.cfg.onToolsChanged = func() {
		changed.Add(1)
		_ = c.tools() // no client lock is held during the callback
	}
	clientTStart(t, c)
	clientTEventually(t, "first onToolsChanged", func() bool { return changed.Load() == 1 })

	list.set(clientTTool("a"), clientTTool("b"))
	srv.last().serverNotify(methodToolsListChanged, "")
	clientTEventually(t, "refreshed list", func() bool { return slices.Equal(clientTToolNames(c), []string{"a", "b"}) })
	clientTEventually(t, "second onToolsChanged", func() bool { return changed.Load() == 2 })

	// An unchanged list does not fire onToolsChanged.
	lists := srv.count(methodToolsList)
	srv.last().serverNotify(methodToolsListChanged, "")
	clientTEventually(t, "third listing", func() bool { return srv.count(methodToolsList) == lists+1 })
	time.Sleep(20 * time.Millisecond)
	if changed.Load() != 2 {
		t.Errorf("onToolsChanged = %d after an unchanged refresh, want 2", changed.Load())
	}

	// A failing refresh keeps the list and the connection.
	failList.Store(true)
	srv.last().serverNotify(methodToolsListChanged, "")
	clientTEventually(t, "failed refresh", func() bool { return srv.count(methodToolsList) == lists+2 })
	time.Sleep(20 * time.Millisecond)
	if got := clientTToolNames(c); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("tools after a failed refresh = %q, want the previous list", got)
	}
	if _, err := c.readyConn(); err != nil {
		t.Errorf("a failed refresh disconnected the client: %v", err)
	}
}

func TestClient_RefreshSessionExpiredReconnects(t *testing.T) {
	routes := clientTLegacyRoutes("", clientTTool("a"))
	base := routes[methodToolsList]
	var expire atomic.Bool
	routes[methodToolsList] = func(ctx context.Context, tr *clientTFakeTransport, req *Request, info callInfo) (*Response, error) {
		if expire.Load() && tr.spawn == 1 {
			return nil, fmt.Errorf("%w: 404", errSessionExpired)
		}
		return base(ctx, tr, req, info)
	}
	srv := &clientTFakeServer{handle: routes.handler()}
	c := clientTNew(t, srv, clientTConfig(transportHTTP, ""))
	clientTStart(t, c)
	clientTReadyConn(t, c)
	expire.Store(true)
	srv.last().serverNotify(methodToolsListChanged, "")
	clientTEventually(t, "reconnect", func() bool { return srv.spawns() == 2 })
	clientTReadyConn(t, c)
}

// TestClient_ConcurrentUse hammers a client from many goroutines while
// connections drop and lists change (run with -race).
func TestClient_ConcurrentUse(t *testing.T) {
	list := &clientTToolList{}
	list.set(clientTTool("a"), clientTTool("b"))
	routes := clientTLegacyRoutes("", clientTTool("a"))
	routes[methodToolsList] = clientTLegacyGuard(list.handler)
	routes[methodToolsCall] = clientTLegacyGuard(clientTDelay(time.Millisecond, clientTEcho))
	srv := &clientTFakeServer{handle: routes.handler()}
	cfg := clientTConfig(transportStdio, "")
	var changed atomic.Int32
	cfg.onToolsChanged = func() { changed.Add(1) }
	c := clientTNew(t, srv, cfg)
	clientTStart(t, c)
	clientTReadyConn(t, c)

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	var calls, ok atomic.Int64
	for i := range 12 {
		wg.Go(func() {
			for ctx.Err() == nil {
				switch i % 4 {
				case 0, 1:
					calls.Add(1)
					res, err := c.callTool(ctx, "a", map[string]any{"i": i})
					switch {
					case err == nil && !res.IsError:
						ok.Add(1)
					case err == nil:
						t.Errorf("unexpected isError result %q", res.Text)
					case errors.Is(err, errNotReady), errors.Is(err, errUnreachable),
						errors.Is(err, errConnectionLost), errors.Is(err, errToolGone), ctx.Err() != nil:
					default:
						t.Errorf("callTool: unexpected error class: %v", err)
					}
				case 2:
					_ = c.probe(ctx)
					_ = c.tools()
				case 3:
					c.noteAccess()
					if tr := srv.last(); tr != nil {
						tr.serverNotify(methodToolsListChanged, "")
					}
					time.Sleep(time.Millisecond)
				}
			}
		})
	}
	wg.Go(func() {
		for i := 0; ctx.Err() == nil; i++ {
			time.Sleep(40 * time.Millisecond)
			if i%2 == 0 {
				list.set(clientTTool("a"), clientTTool("c"))
			} else {
				list.set(clientTTool("a"), clientTTool("b"))
			}
			if tr := srv.last(); tr != nil {
				tr.lose()
			}
		}
	})
	wg.Wait()
	if ok.Load() == 0 {
		t.Errorf("no call succeeded out of %d", calls.Load())
	}
	clientTReadyConn(t, c)
	if srv.spawns() < 2 {
		t.Errorf("spawns = %d, want reconnects", srv.spawns())
	}
}

// clientTWaits records every reconnect wait the supervisor plans (the
// jitter hook sees each one) and returns it unchanged.
type clientTWaits struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (w *clientTWaits) jitter(d time.Duration) time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.waits = append(w.waits, d)
	return d
}

func (w *clientTWaits) get() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.waits...)
}

// clientTReadyOn waits until c is ready on the spawn-th transport
// (1-based).
func clientTReadyOn(t *testing.T, c *serverClient, srv *clientTFakeServer, spawn int) {
	t.Helper()
	clientTEventually(t, fmt.Sprintf("ready on spawn %d", spawn), func() bool {
		conn, err := c.readyConn()
		return err == nil && srv.spawns() == spawn && conn.tr == srv.transportAt(spawn-1)
	})
}

// clientTLoseWhenReady waits until c is ready on the spawn-th transport,
// then kills that connection.
func clientTLoseWhenReady(t *testing.T, c *serverClient, srv *clientTFakeServer, spawn int) {
	t.Helper()
	clientTReadyOn(t, c, srv, spawn)
	srv.transportAt(spawn - 1).lose()
}

// TestClient_FlappingServerBacksOff: a server that dies right after
// every connect is a run of failures, not a series of fresh starts. The
// reconnect wait doubles to the cap instead of staying at backoffMin,
// and the run logs one WARN for the loss and one INFO for the connect;
// repeats go to Debug.
func TestClient_FlappingServerBacksOff(t *testing.T) {
	srv := &clientTFakeServer{handle: clientTLegacyRoutes("", clientTTool("echo")).handler()}
	cfg := clientTConfig(transportStdio, protocolLegacy)
	cfg.backoffMin, cfg.backoffMax = 2*time.Millisecond, 16*time.Millisecond
	cfg.stableAfter = time.Hour
	logs := &coreTLogBuffer{}
	cfg.logger = logs.logger()
	c := clientTNew(t, srv, cfg)
	waits := &clientTWaits{}
	c.jitter = waits.jitter
	clientTStart(t, c)
	for spawn := 1; spawn <= 6; spawn++ {
		clientTLoseWhenReady(t, c, srv, spawn)
	}
	clientTReadyOn(t, c, srv, 7)

	ms := time.Millisecond
	if got, want := waits.get(), []time.Duration{2 * ms, 4 * ms, 8 * ms, 16 * ms, 16 * ms, 16 * ms}; !slices.Equal(got, want) {
		t.Errorf("reconnect waits = %v, want %v (doubling to the cap)", got, want)
	}
	out := logs.String()
	if n := strings.Count(out, `level=WARN msg="mcp: server connection lost; reconnecting"`); n != 1 {
		t.Errorf("WARN connection-lost lines = %d, want 1\n%s", n, out)
	}
	if n := strings.Count(out, `level=INFO msg="mcp: server connected"`); n != 1 {
		t.Errorf("INFO connected lines = %d, want 1\n%s", n, out)
	}
	if n := strings.Count(out, `level=DEBUG msg="mcp: server connection lost; reconnecting"`); n != 5 {
		t.Errorf("Debug connection-lost lines = %d, want 5", n)
	}
}

// TestClient_StableConnectionResetsBackoff: once a connection has lived
// stableAfter, the run of failures is over. It logs that the connection
// is stable again, and its loss waits only backoffMin.
func TestClient_StableConnectionResetsBackoff(t *testing.T) {
	srv := &clientTFakeServer{handle: clientTLegacyRoutes("", clientTTool("echo")).handler()}
	cfg := clientTConfig(transportStdio, protocolLegacy)
	cfg.backoffMin, cfg.backoffMax = 2*time.Millisecond, time.Second
	cfg.stableAfter = 300 * time.Millisecond
	logs := &coreTLogBuffer{}
	cfg.logger = logs.logger()
	c := clientTNew(t, srv, cfg)
	waits := &clientTWaits{}
	c.jitter = waits.jitter
	clientTStart(t, c)
	for spawn := 1; spawn <= 3; spawn++ {
		clientTLoseWhenReady(t, c, srv, spawn)
	}
	clientTEventually(t, "the fourth connection proves stable", func() bool {
		return strings.Contains(logs.String(), `level=INFO msg="mcp: server connection is stable again"`)
	})
	clientTLoseWhenReady(t, c, srv, 4)
	clientTReadyOn(t, c, srv, 5)

	ms := time.Millisecond
	if got, want := waits.get(), []time.Duration{2 * ms, 4 * ms, 8 * ms, 2 * ms}; !slices.Equal(got, want) {
		t.Errorf("reconnect waits = %v, want %v (reset after the stable connection)", got, want)
	}
	if n := strings.Count(logs.String(), `level=WARN msg="mcp: server connection lost; reconnecting"`); n != 2 {
		t.Errorf("WARN connection-lost lines = %d, want 2 (one per run of failures)", n)
	}
}

// TestClient_StableThroughABlockedRefresh: how long a connection lived
// decides whether its loss ends the run of failures, not which ready
// case the supervisor's select happens to pick. A refresh that hangs
// past stableAfter keeps the supervisor from servicing the stable timer,
// so when the connection is then lost the timer and the loss are ready
// together. The loss must still count as stable: the stdio exit WARN is
// re-armed before the reconnect, the reconnect logs at INFO, and the
// backoff is back at backoffMin. The select picks at random, so the case
// runs many times.
func TestClient_StableThroughABlockedRefresh(t *testing.T) {
	const stableAfter = 30 * time.Millisecond
	for i := range 30 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			routes := clientTLegacyRoutes("", clientTTool("echo"))
			list := routes[methodToolsList]
			routes[methodToolsList] = func(ctx context.Context, tr *clientTFakeTransport, req *Request, info callInfo) (*Response, error) {
				if tr.spawn == 1 && clientTSentCount(tr, methodToolsList) > 1 {
					return clientTHang(ctx, tr, req, info) // the refresh never answers
				}
				return list(ctx, tr, req, info)
			}
			srv := &clientTFakeServer{handle: routes.handler()}
			cfg := clientTConfig(transportStdio, protocolLegacy)
			cfg.backoffMin, cfg.backoffMax = 2*time.Millisecond, time.Second
			cfg.stableAfter = stableAfter
			logs := &coreTLogBuffer{}
			cfg.logger = logs.logger()
			c := clientTNew(t, srv, cfg)
			waits := &clientTWaits{}
			c.jitter = waits.jitter
			var rearmed atomic.Bool // the exit WARN was re-armed when the reconnect began
			srv.dialErr = func(dial int) error {
				if dial == 2 {
					rearmed.Store(!c.exitWarned.Load())
				}
				return nil
			}
			clientTStart(t, c)
			clientTReadyOn(t, c, srv, 1)
			clientTEventually(t, "the connect logged", func() bool {
				return strings.Contains(logs.String(), `msg="mcp: server connected"`)
			})
			c.exitWarned.Store(true) // an exit earlier in this run warned
			srv.last().serverNotify(methodToolsListChanged, "")
			clientTEventually(t, "the refresh hanging", func() bool { return srv.count(methodToolsList) == 2 })
			time.Sleep(4 * stableAfter)
			srv.transportAt(0).lose()
			clientTLoseWhenReady(t, c, srv, 2)
			clientTReadyOn(t, c, srv, 3)

			out := logs.String()
			if !rearmed.Load() {
				t.Errorf("the stdio exit WARN was not re-armed after a connection that lived %v\n%s", 4*stableAfter, out)
			}
			if n := strings.Count(out, `level=INFO msg="mcp: server connected"`); n < 2 {
				t.Errorf("INFO connected lines = %d, want the reconnect after the stable connection at INFO too\n%s", n, out)
			}
			if got, want := waits.get(), []time.Duration{2 * time.Millisecond, 2 * time.Millisecond}; !slices.Equal(got, want) {
				t.Errorf("reconnect waits = %v, want %v (the stable connection reset the backoff)", got, want)
			}
		})
	}
}

// TestClient_StableRearmsExitWarnDuringABlockedRefresh: a connection
// that reaches stableAfter re-arms the stdio exit WARN at that moment,
// even while serve is stuck in an inline refresh, so a process that then
// dies during the refresh warns with its own stderr tail instead of
// finding the gate still taken by an earlier quick crash.
func TestClient_StableRearmsExitWarnDuringABlockedRefresh(t *testing.T) {
	const stableAfter = 30 * time.Millisecond
	routes := clientTLegacyRoutes("", clientTTool("echo"))
	list := routes[methodToolsList]
	routes[methodToolsList] = func(ctx context.Context, tr *clientTFakeTransport, req *Request, info callInfo) (*Response, error) {
		if clientTSentCount(tr, methodToolsList) > 1 {
			return clientTHang(ctx, tr, req, info) // the refresh never answers
		}
		return list(ctx, tr, req, info)
	}
	srv := &clientTFakeServer{handle: routes.handler()}
	cfg := clientTConfig(transportStdio, protocolLegacy)
	cfg.stableAfter = 4 * stableAfter
	cfg.connectTimeout = time.Minute // the refresh outlasts the test
	c := clientTNew(t, srv, cfg)
	clientTStart(t, c)
	clientTReadyOn(t, c, srv, 1)
	c.exitWarned.Store(true) // an exit earlier in this run warned
	srv.last().serverNotify(methodToolsListChanged, "")
	clientTEventually(t, "the refresh hanging", func() bool { return srv.count(methodToolsList) == 2 })
	deadline := time.Now().Add(time.Second)
	for c.exitWarned.Load() && time.Now().Before(deadline) {
		time.Sleep(stableAfter / 3)
	}
	if c.exitWarned.Load() {
		t.Fatalf("the exit WARN is still taken %v after connecting (stableAfter %v) while a refresh blocks serve", time.Second, cfg.stableAfter)
	}
	select {
	case <-srv.transportAt(0).Done():
		t.Fatal("the connection ended; the re-arm must happen while it is live")
	default:
	}
}

// TestClient_RearmExitWarnAtInfoConnect: a connect logged at INFO
// re-arms the stdio exit WARN, so that connection's crash warns even
// after a start-up failure took the WARN. If the transport already
// ended, its exit may have been logged before the re-arm, so the gate
// stays set rather than pass the WARN to a later quick crash.
func TestClient_RearmExitWarnAtInfoConnect(t *testing.T) {
	srv := &clientTFakeServer{handle: clientTLegacyRoutes("").handler()}
	for _, tc := range []struct {
		name      string
		ended     bool
		wantArmed bool
	}{
		{name: "live transport", ended: false, wantArmed: true},
		{name: "transport already ended", ended: true, wantArmed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := clientTNew(t, srv, clientTConfig(transportStdio, protocolLegacy))
			tr, err := srv.factory()(t.Context(), transportHooks{})
			if err != nil {
				t.Fatal(err)
			}
			if tc.ended {
				tr.(*clientTFakeTransport).lose()
			}
			c.exitWarned.Store(true)
			c.rearmExitWarn(&clientConn{tr: tr})
			if armed := !c.exitWarned.Load(); armed != tc.wantArmed {
				t.Errorf("exit WARN armed = %v, want %v", armed, tc.wantArmed)
			}
		})
	}
}

// clientTSentCount counts the requests for method sent on tr.
func clientTSentCount(tr *clientTFakeTransport, method string) int {
	n := 0
	for _, m := range tr.sent() {
		if m.call && m.method == method {
			n++
		}
	}
	return n
}

// TestClient_JitterSpreadsWaits: the default jitter keeps every wait
// within ±20% and actually varies it.
func TestClient_JitterSpreadsWaits(t *testing.T) {
	if got := jitterDuration(0); got != 0 {
		t.Errorf("jitterDuration(0) = %v", got)
	}
	lo, hi := time.Hour, time.Duration(0)
	for range 1000 {
		d := jitterDuration(time.Second)
		if d < 800*time.Millisecond || d > 1200*time.Millisecond {
			t.Fatalf("jitterDuration(1s) = %v, outside ±20%%", d)
		}
		lo, hi = min(lo, d), max(hi, d)
	}
	if lo > 900*time.Millisecond || hi < 1100*time.Millisecond {
		t.Errorf("1000 jittered waits span only [%v, %v]", lo, hi)
	}
}

// TestClient_ThaneBugLoggedOncePerRun: a server that rejects Thane's own
// framing (HTTP 406) logs ERROR once per run of failed attempts, not on
// every retry; a successful connect re-arms it.
func TestClient_ThaneBugLoggedOncePerRun(t *testing.T) {
	var healthy atomic.Bool
	modern := clientTModernRoutes(clientTTool("echo")).handler()
	srv := &clientTFakeServer{handle: func(ctx context.Context, tr *clientTFakeTransport, req *Request, info callInfo) (*Response, error) {
		if !healthy.Load() {
			return nil, clientTStatus(406, nil)
		}
		return modern(ctx, tr, req, info)
	}}
	cfg := clientTConfig(transportHTTP, protocolAuto)
	logs := &coreTLogBuffer{}
	cfg.logger = logs.logger()
	c := clientTNew(t, srv, cfg)
	clientTStart(t, c)
	const errLine = `level=ERROR msg="mcp: the server rejected Thane's own request framing; this needs a Thane fix"`
	clientTEventually(t, "four failed attempts", func() bool { return srv.spawns() >= 4 })
	if n := strings.Count(logs.String(), errLine); n != 1 {
		t.Fatalf("ERROR lines after %d attempts = %d, want 1\n%s", srv.spawns(), n, logs.String())
	}
	if n := strings.Count(logs.String(), "level=ERROR"); n != 1 {
		t.Errorf("ERROR lines in total = %d, want 1", n)
	}

	healthy.Store(true)
	clientTReadyConn(t, c)
	healthy.Store(false)
	spawns := srv.spawns()
	srv.last().lose()
	clientTEventually(t, "three more failed attempts", func() bool { return srv.spawns() >= spawns+3 })
	if n := strings.Count(logs.String(), errLine); n != 2 {
		t.Errorf("ERROR lines after a recovery and a new run = %d, want 2", n)
	}
}

// TestClient_AttemptFailureLogsHTTPDetails: the first-failure WARN of a
// connect run carries the status, the WWW-Authenticate challenge and the
// body, which Error() leaves out.
func TestClient_AttemptFailureLogsHTTPDetails(t *testing.T) {
	srv := &clientTFakeServer{handle: clientTReply(nil, clientTAuthRefusal())}
	cfg := clientTConfig(transportHTTP, protocolAuto)
	logs := &coreTLogBuffer{}
	cfg.logger = logs.logger()
	c := clientTNew(t, srv, cfg)
	clientTStart(t, c)
	const msg = `level=WARN msg="mcp: connecting to the server failed`
	clientTEventually(t, "the first-failure WARN", func() bool { return strings.Contains(logs.String(), msg) })
	var warn string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, msg) {
			warn = line
		}
	}
	for _, want := range []string{"http_status=401", "token expired", "BODY-MARKER"} {
		if !strings.Contains(warn, want) {
			t.Errorf("first-failure WARN lacks %q: %q", want, warn)
		}
	}
}

// TestClient_RefreshFailureLogsHTTPDetails: a failed background re-list
// logs the HTTP details too.
func TestClient_RefreshFailureLogsHTTPDetails(t *testing.T) {
	routes := clientTLegacyRoutes("", clientTTool("echo"))
	routes[methodToolsList] = clientTSeq(
		clientTReply(clientTToolsPage(nil, clientTTool("echo")), nil),
		clientTReply(nil, clientTAuthRefusal()),
	)
	srv := &clientTFakeServer{handle: routes.handler()}
	cfg := clientTConfig(transportHTTP, protocolLegacy)
	logs := &coreTLogBuffer{}
	cfg.logger = logs.logger()
	c := clientTNew(t, srv, cfg)
	clientTStart(t, c)
	clientTReadyConn(t, c)
	srv.last().serverNotify(methodToolsListChanged, "")
	const warn = `level=WARN msg="mcp: refreshing the tool list failed; keeping the previous list"`
	clientTEventually(t, "the refresh failure WARN", func() bool { return strings.Contains(logs.String(), warn) })
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, warn) && (!strings.Contains(line, "http_status=401") || !strings.Contains(line, "BODY-MARKER")) {
			t.Errorf("refresh WARN lacks the HTTP details: %q", line)
		}
	}
}

// TestClient_ConnectErrorTextIsNotRepetitive checks the text of a failed
// connect as the operator reads it in the logs: the class layers say
// "mcp:" at most twice (not connected, plus the cause's own class) and
// the failing method is named once, while errors.Is still finds every
// class in the chain.
func TestClient_ConnectErrorTextIsNotRepetitive(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedURL := "http://" + ln.Addr().String() + "/mcp"
	_ = ln.Close()
	notAcceptable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotAcceptable)
	}))
	t.Cleanup(notAcceptable.Close)

	tests := []struct {
		name   string
		srv    ServerConfig
		wantIs []error
	}{
		{
			name:   "http connection refused",
			srv:    ServerConfig{Name: "refused", Transport: transportHTTP, URL: closedURL},
			wantIs: []error{errNotReady, errUnreachable},
		},
		{
			name:   "http status 406",
			srv:    ServerConfig{Name: "406", Transport: transportHTTP, URL: notAcceptable.URL + "/mcp"},
			wantIs: []error{errNotReady},
		},
		{
			name:   "stdio server exits at once",
			srv:    ServerConfig{Name: "exits", Transport: transportStdio, Command: "/bin/sh", Args: []string{"-c", "exit 1"}},
			wantIs: []error{errNotReady},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := coreTLogger(t)
			m := NewManager(nil, logger)
			cfg := e2eTConfig(tt.srv.Transport, protocolAuto)
			cfg.backoffMin, cfg.backoffMax = time.Minute, time.Minute
			c := e2eTNew(t, cfg, m.factoryFor(tt.srv, logger))
			e2eTStart(t, c)
			_, err := c.readyConn()
			if err == nil {
				t.Fatal("readyConn succeeded against a broken server")
			}
			for _, target := range tt.wantIs {
				if !errors.Is(err, target) {
					t.Errorf("errors.Is(err, %v) = false; err = %v", target, err)
				}
			}
			text := err.Error()
			if n := strings.Count(text, "mcp:"); n > 2 {
				t.Errorf("%q says mcp: %d times, want at most 2", text, n)
			}
			if n := strings.Count(text, methodDiscover) + strings.Count(text, methodInitialize); n != 1 {
				t.Errorf("%q names the negotiation method %d times, want once", text, n)
			}
			if perr := c.probe(t.Context()); perr == nil || perr.Error() != text {
				t.Errorf("probe error = %v, want the connect error %q", perr, text)
			}
		})
	}
}
