package mcp

import (
	"context"
	"errors"
	"fmt"
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
	// The restarted server is re-initialized (C1/C16), skipping the probe
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
				t.Error("a failed health check tore the connection down (C1/C17)")
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
