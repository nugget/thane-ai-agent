package homeassistant

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeHA is a controllable stand-in for the Home Assistant WebSocket
// endpoint. It performs the auth handshake, acks subscribe_events, and lets
// a test push events, force-drop the live connection, or refuse the first N
// connections — enough to exercise the self-healing supervisor.
type fakeHA struct {
	upgrader       websocket.Upgrader
	mu             sync.Mutex // guards conns/cur and serializes server-side writes
	conns          int
	cur            *websocket.Conn
	failFirst      int         // close the first N connections right after upgrade
	stallHandshake bool        // upgrade, then never send auth_required (hang)
	subscribed     chan string // event_type of each acked subscribe
}

func newFakeHA() *fakeHA {
	return &fakeHA{subscribed: make(chan string, 16)}
}

func (f *fakeHA) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/websocket", f.handle)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeHA) handle(w http.ResponseWriter, r *http.Request) {
	conn, err := f.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	f.mu.Lock()
	f.conns++
	n := f.conns
	f.cur = conn
	fail := n <= f.failFirst
	stall := f.stallHandshake
	f.mu.Unlock()

	if fail {
		conn.Close()
		return
	}
	if stall {
		// Upgrade succeeds, but auth_required is never sent. Block until the
		// client abandons the handshake (its deadline) and closes the conn.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}

	if err := f.write(conn, map[string]any{"type": "auth_required"}); err != nil {
		return
	}
	var auth map[string]any
	if err := conn.ReadJSON(&auth); err != nil {
		return
	}
	if err := f.write(conn, map[string]any{"type": "auth_ok"}); err != nil {
		return
	}

	for {
		var m map[string]any
		if err := conn.ReadJSON(&m); err != nil {
			return
		}
		id, _ := m["id"].(float64)
		if m["type"] == "subscribe_events" {
			_ = f.write(conn, map[string]any{"id": int64(id), "type": "result", "success": true})
			if et, ok := m["event_type"].(string); ok {
				select {
				case f.subscribed <- et:
				default:
				}
			}
			continue
		}
		// Ack anything else so request/response calls don't hang.
		_ = f.write(conn, map[string]any{"id": int64(id), "type": "result", "success": true})
	}
}

// write serializes all server-side writes: gorilla forbids concurrent
// writers, and a test's pushStateChanged can race the handler's acks.
func (f *fakeHA) write(conn *websocket.Conn, v any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return conn.WriteJSON(v)
}

func (f *fakeHA) pushStateChanged(entityID string) {
	f.mu.Lock()
	conn := f.cur
	f.mu.Unlock()
	if conn == nil {
		return
	}
	_ = f.write(conn, map[string]any{
		"type": "event",
		"event": map[string]any{
			"event_type": "state_changed",
			"data":       map[string]any{"entity_id": entityID},
		},
	})
}

func (f *fakeHA) dropCurrent() {
	f.mu.Lock()
	conn := f.cur
	f.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
}

func (f *fakeHA) connCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newFastWS(t *testing.T, url string) *WSClient {
	t.Helper()
	ws := NewWSClient(url, "test-token", discardLogger())
	// Tiny backoff so reconnect tests run in milliseconds, not seconds.
	ws.SetReconnectBackoff(time.Millisecond, 10*time.Millisecond, 2.0)
	return ws
}

func waitSubscribe(t *testing.T, f *fakeHA, want string) {
	t.Helper()
	select {
	case got := <-f.subscribed:
		if got != want {
			t.Fatalf("subscribed to %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for subscribe to %q", want)
	}
}

func waitEvent(t *testing.T, ws *WSClient) Event {
	t.Helper()
	select {
	case ev := <-ws.Events():
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

func waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within timeout: %s", msg)
}

// TestWSClient_ConnectSubscribeEvent covers the happy path: a subscription
// recorded before Start is applied on connect, and events flow. It also
// exercises the connected-path Subscribe (sent immediately).
func TestWSClient_ConnectSubscribeEvent(t *testing.T) {
	f := newFakeHA()
	srv := f.start(t)
	ws := newFastWS(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Recorded as sticky intent while disconnected; applied on connect.
	if err := ws.Subscribe(ctx, "state_changed"); err != nil {
		t.Fatalf("Subscribe (pre-connect intent): %v", err)
	}
	ws.Start(ctx)

	waitSubscribe(t, f, "state_changed")
	waitUntil(t, ws.IsConnected, "client should report connected")
	if got := f.connCount(); got != 1 {
		t.Fatalf("connCount = %d, want 1", got)
	}

	// Live subscribe (while connected) is sent immediately.
	if err := ws.Subscribe(ctx, "automation_triggered"); err != nil {
		t.Fatalf("Subscribe (live): %v", err)
	}
	waitSubscribe(t, f, "automation_triggered")

	f.pushStateChanged("light.kitchen")
	ev := waitEvent(t, ws)
	if ev.Type != "state_changed" {
		t.Fatalf("event type = %q, want state_changed", ev.Type)
	}
}

// TestWSClient_ReconnectsAndResubscribesAfterDrop is the core regression:
// a dropped connection (while the REST side stays healthy) must self-heal —
// the supervisor reconnects and re-applies the subscription with no external
// trigger, and events flow again on the new connection.
func TestWSClient_ReconnectsAndResubscribesAfterDrop(t *testing.T) {
	f := newFakeHA()
	srv := f.start(t)
	ws := newFastWS(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ws.Subscribe(ctx, "state_changed"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	ws.Start(ctx)

	waitSubscribe(t, f, "state_changed")
	waitUntil(t, ws.IsConnected, "initial connect")

	// Kill the live connection out from under the client.
	f.dropCurrent()

	// Supervisor reconnects and re-applies the subscription unprompted.
	waitSubscribe(t, f, "state_changed")
	waitUntil(t, func() bool { return f.connCount() >= 2 }, "should have reconnected")
	waitUntil(t, ws.IsConnected, "reconnected")

	// Events flow again on the fresh connection.
	f.pushStateChanged("binary_sensor.door")
	ev := waitEvent(t, ws)
	if ev.Type != "state_changed" {
		t.Fatalf("post-reconnect event type = %q, want state_changed", ev.Type)
	}
}

// TestWSClient_RetriesUntilServerAccepts verifies the supervisor keeps
// dialing through repeated failures (the transient-DNS/dial-timeout case
// from the incident) and connects once the endpoint is healthy.
func TestWSClient_RetriesUntilServerAccepts(t *testing.T) {
	f := newFakeHA()
	f.failFirst = 2 // first two connections are refused right after upgrade
	srv := f.start(t)
	ws := newFastWS(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ws.Subscribe(ctx, "state_changed"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	ws.Start(ctx)

	waitUntil(t, ws.IsConnected, "should connect after retries")
	if got := f.connCount(); got < 3 {
		t.Fatalf("connCount = %d, want >= 3 (two refusals then success)", got)
	}
	waitSubscribe(t, f, "state_changed")
}

// TestWSClient_StopsOnContextCancel verifies the supervisor tears down when
// its Start context is cancelled.
func TestWSClient_StopsOnContextCancel(t *testing.T) {
	f := newFakeHA()
	srv := f.start(t)
	ws := newFastWS(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	ws.Start(ctx)
	waitUntil(t, ws.IsConnected, "initial connect")

	cancel()
	waitUntil(t, func() bool { return !ws.IsConnected() }, "should disconnect on ctx cancel")
}

// TestWSClient_HandshakeTimeoutDoesNotHang verifies that a connection which
// upgrades but never completes the HA auth handshake cannot block Connect
// forever: the attempt times out and the supervisor keeps retrying. If
// Connect hung, connCount would stay at 1 and this would never reach 2.
func TestWSClient_HandshakeTimeoutDoesNotHang(t *testing.T) {
	f := newFakeHA()
	f.stallHandshake = true // upgrade succeeds, but auth_required never arrives
	srv := f.start(t)
	ws := newFastWS(t, srv.URL)
	ws.SetHandshakeTimeout(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws.Start(ctx)

	waitUntil(t, func() bool { return f.connCount() >= 2 },
		"supervisor should time out the stalled handshake and retry")
	if ws.IsConnected() {
		t.Fatal("must not report connected to a server that never completes the auth handshake")
	}
}

// TestWSClient_HealthyIdleConnectionStaysUp verifies the handshake deadline
// is cleared before the long-lived readLoop: a healthy but idle socket must
// not trip the deadline and force a reconnect. With the deadline leaked, the
// connection would drop ~one handshake-timeout after connecting.
func TestWSClient_HealthyIdleConnectionStaysUp(t *testing.T) {
	f := newFakeHA()
	srv := f.start(t)
	ws := newFastWS(t, srv.URL)
	ws.SetHandshakeTimeout(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ws.Subscribe(ctx, "state_changed"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	ws.Start(ctx)
	waitSubscribe(t, f, "state_changed")
	waitUntil(t, ws.IsConnected, "initial connect")

	// Stay idle well past the handshake timeout.
	time.Sleep(150 * time.Millisecond)

	if got := f.connCount(); got != 1 {
		t.Fatalf("idle healthy connection reconnected (connCount=%d); handshake deadline leaked into readLoop", got)
	}
	if !ws.IsConnected() {
		t.Fatal("idle healthy connection should still be connected")
	}
}
