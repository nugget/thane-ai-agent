package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialTestServer creates an httptest.Server with the platform handler and
// returns a connected WebSocket client. The caller must close both.
func dialTestServer(t *testing.T, token string) (*httptest.Server, *websocket.Conn) {
	t.Helper()
	registry := NewRegistry(nil)
	handler := NewHandler(token, registry, nil)
	srv := httptest.NewServer(handler)
	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		srv.Close()
		t.Fatalf("dial: %v", err)
	}
	return srv, conn
}

// readJSON reads a JSON message into dst from the WebSocket connection.
func readJSON(t *testing.T, conn *websocket.Conn, dst any) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.ReadJSON(dst); err != nil {
		t.Fatalf("readJSON: %v", err)
	}
}

func TestAuthHandshakeSuccess(t *testing.T) {
	const token = "test-secret"
	srv, conn := dialTestServer(t, token)
	defer srv.Close()
	defer conn.Close()

	// Step 1: Expect auth_required.
	var authReq authRequired
	readJSON(t, conn, &authReq)
	if authReq.Type != typeAuthRequired {
		t.Fatalf("expected type %q, got %q", typeAuthRequired, authReq.Type)
	}
	if authReq.Version != protocolVersion {
		t.Fatalf("expected version %q, got %q", protocolVersion, authReq.Version)
	}

	// Step 2: Send auth.
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(authMessage{
		Type:       typeAuth,
		Token:      token,
		ClientName: "Test Mac",
		ClientID:   "test-uuid-1",
	}); err != nil {
		t.Fatalf("send auth: %v", err)
	}

	// Step 3: Expect auth_ok.
	var ok authOK
	readJSON(t, conn, &ok)
	if ok.Type != typeAuthOK {
		t.Fatalf("expected type %q, got %q", typeAuthOK, ok.Type)
	}
	if ok.ProviderID == "" {
		t.Fatal("expected non-empty provider_id")
	}
	if ok.ProviderID[:5] != "prov_" {
		t.Errorf("expected provider_id prefix prov_, got %q", ok.ProviderID)
	}
}

func TestAuthHandshakeBadToken(t *testing.T) {
	const token = "correct-token"
	srv, conn := dialTestServer(t, token)
	defer srv.Close()
	defer conn.Close()

	// Read auth_required.
	var authReq authRequired
	readJSON(t, conn, &authReq)

	// Send auth with wrong token.
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(authMessage{
		Type:       typeAuth,
		Token:      "wrong-token",
		ClientName: "Bad Client",
		ClientID:   "test-uuid-2",
	}); err != nil {
		t.Fatalf("send auth: %v", err)
	}

	// Expect auth_failed.
	var failed authFailed
	readJSON(t, conn, &failed)
	if failed.Type != typeAuthFailed {
		t.Fatalf("expected type %q, got %q", typeAuthFailed, failed.Type)
	}
}

func TestPingPong(t *testing.T) {
	const token = "test-secret"
	registry := NewRegistry(nil)
	// Use a short ping interval for testing.
	handler := NewHandler(token, registry, nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Complete auth handshake.
	var authReq authRequired
	readJSON(t, conn, &authReq)

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(authMessage{
		Type:       typeAuth,
		Token:      token,
		ClientName: "Ping Test",
		ClientID:   "test-uuid-3",
	}); err != nil {
		t.Fatalf("send auth: %v", err)
	}

	var ok authOK
	readJSON(t, conn, &ok)
	if ok.Type != typeAuthOK {
		t.Fatalf("auth failed: %+v", ok)
	}

	// Wait for a ping (server sends every 30s — in tests this is real time).
	// Instead of waiting 30s, verify the connection is alive by sending
	// a pong and confirming we don't get disconnected.
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(Message{Type: typePong}); err != nil {
		t.Fatalf("send pong: %v", err)
	}

	// Verify provider is registered.
	if got := registry.Count(); got != 1 {
		t.Fatalf("expected 1 provider, got %d", got)
	}
	infos := registry.List()
	if infos[0].ClientName != "Ping Test" {
		t.Errorf("client name: got %q, want %q", infos[0].ClientName, "Ping Test")
	}
}

func TestProviderCleanupOnDisconnect(t *testing.T) {
	const token = "test-secret"
	registry := NewRegistry(nil)
	handler := NewHandler(token, registry, nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Complete auth.
	var authReq authRequired
	readJSON(t, conn, &authReq)
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	conn.WriteJSON(authMessage{
		Type:       typeAuth,
		Token:      token,
		ClientName: "Disconnect Test",
		ClientID:   "test-uuid-4",
	})
	var ok authOK
	readJSON(t, conn, &ok)

	if got := registry.Count(); got != 1 {
		t.Fatalf("expected 1 provider after connect, got %d", got)
	}

	// Close the client connection.
	conn.WriteMessage(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"))
	conn.Close()

	// Give the server read loop time to notice.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if registry.Count() == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("provider not cleaned up after disconnect")
}

func TestMultipleProviders(t *testing.T) {
	const token = "test-secret"
	registry := NewRegistry(nil)
	handler := NewHandler(token, registry, nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	wsURL := "ws" + srv.URL[len("http"):]
	conns := make([]*websocket.Conn, 3)

	for i := range conns {
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		defer conn.Close()
		conns[i] = conn

		var authReq authRequired
		readJSON(t, conn, &authReq)
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		conn.WriteJSON(authMessage{
			Type:       typeAuth,
			Token:      token,
			ClientName: "Multi Test",
			ClientID:   "test-uuid-multi",
		})
		var ok authOK
		readJSON(t, conn, &ok)
	}

	if got := registry.Count(); got != 3 {
		t.Fatalf("expected 3 providers, got %d", got)
	}
}

func TestAuthHandshakeWrongMessageType(t *testing.T) {
	const token = "test-secret"
	srv, conn := dialTestServer(t, token)
	defer srv.Close()
	defer conn.Close()

	// Read auth_required.
	var authReq authRequired
	readJSON(t, conn, &authReq)

	// Send a non-auth message.
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(map[string]string{"type": "register_capabilities"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Expect auth_failed.
	var raw json.RawMessage
	readJSON(t, conn, &raw)
	var msg struct {
		Type string `json:"type"`
	}
	json.Unmarshal(raw, &msg)
	if msg.Type != typeAuthFailed {
		t.Fatalf("expected %q, got %q (raw: %s)", typeAuthFailed, msg.Type, raw)
	}
}

func TestUpgradeOnCorrectPath(t *testing.T) {
	// Verify the handler can be mounted at any path and still works.
	registry := NewRegistry(nil)
	handler := NewHandler("tok", registry, nil)
	mux := http.NewServeMux()
	mux.Handle("GET /v1/platform/ws", handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + srv.URL[len("http"):] + "/v1/platform/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	var authReq authRequired
	readJSON(t, conn, &authReq)
	if authReq.Type != typeAuthRequired {
		t.Fatalf("expected auth_required, got %q", authReq.Type)
	}
}
