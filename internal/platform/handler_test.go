package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// testTokenIndex returns a token index mapping "test-secret" to "nugget".
func testTokenIndex() map[string]string {
	return map[string]string{"test-secret": "nugget"}
}

// dialTestServer creates an httptest.Server with the platform handler and
// returns a connected WebSocket client. The caller must close both.
func dialTestServer(t *testing.T, tokenIndex map[string]string) (*httptest.Server, *websocket.Conn) {
	t.Helper()
	registry := NewRegistry(nil)
	handler := NewHandler(tokenIndex, registry, nil)
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
	srv, conn := dialTestServer(t, testTokenIndex())
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
		Token:      "test-secret",
		ClientName: "Test Mac",
		ClientID:   "test-uuid-1",
	}); err != nil {
		t.Fatalf("send auth: %v", err)
	}

	// Step 3: Expect auth_ok with account.
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
	if ok.Account != "nugget" {
		t.Errorf("expected account %q, got %q", "nugget", ok.Account)
	}
}

func TestAuthHandshakeBadToken(t *testing.T) {
	srv, conn := dialTestServer(t, testTokenIndex())
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

func TestMultiAccountAuth(t *testing.T) {
	tokenIndex := map[string]string{
		"nugget-token": "nugget",
		"aimee-token":  "aimee",
	}
	registry := NewRegistry(nil)
	handler := NewHandler(tokenIndex, registry, nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	wsURL := "ws" + srv.URL[len("http"):]

	// Connect as nugget.
	conn1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial nugget: %v", err)
	}
	defer conn1.Close()

	var authReq1 authRequired
	readJSON(t, conn1, &authReq1)
	conn1.SetWriteDeadline(time.Now().Add(5 * time.Second))
	conn1.WriteJSON(authMessage{
		Type: typeAuth, Token: "nugget-token",
		ClientName: "deepslate", ClientID: "uuid-nugget",
	})
	var ok1 authOK
	readJSON(t, conn1, &ok1)
	if ok1.Account != "nugget" {
		t.Errorf("expected account nugget, got %q", ok1.Account)
	}

	// Connect as aimee.
	conn2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial aimee: %v", err)
	}
	defer conn2.Close()

	var authReq2 authRequired
	readJSON(t, conn2, &authReq2)
	conn2.SetWriteDeadline(time.Now().Add(5 * time.Second))
	conn2.WriteJSON(authMessage{
		Type: typeAuth, Token: "aimee-token",
		ClientName: "pocket", ClientID: "uuid-aimee",
	})
	var ok2 authOK
	readJSON(t, conn2, &ok2)
	if ok2.Account != "aimee" {
		t.Errorf("expected account aimee, got %q", ok2.Account)
	}

	// Verify registry state.
	if got := registry.Count(); got != 2 {
		t.Fatalf("expected 2 providers, got %d", got)
	}

	nuggetProviders := registry.ByAccount("nugget")
	if len(nuggetProviders) != 1 {
		t.Fatalf("expected 1 nugget provider, got %d", len(nuggetProviders))
	}
	if nuggetProviders[0].ClientName != "deepslate" {
		t.Errorf("nugget client: got %q, want %q", nuggetProviders[0].ClientName, "deepslate")
	}

	aimeeProviders := registry.ByAccount("aimee")
	if len(aimeeProviders) != 1 {
		t.Fatalf("expected 1 aimee provider, got %d", len(aimeeProviders))
	}
}

func TestMultiDeviceSameAccount(t *testing.T) {
	tokenIndex := map[string]string{
		"nugget-laptop":  "nugget",
		"nugget-desktop": "nugget",
	}
	registry := NewRegistry(nil)
	handler := NewHandler(tokenIndex, registry, nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	wsURL := "ws" + srv.URL[len("http"):]

	// Connect laptop.
	conn1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial laptop: %v", err)
	}
	defer conn1.Close()

	var authReq1 authRequired
	readJSON(t, conn1, &authReq1)
	conn1.SetWriteDeadline(time.Now().Add(5 * time.Second))
	conn1.WriteJSON(authMessage{
		Type: typeAuth, Token: "nugget-laptop",
		ClientName: "deepslate", ClientID: "uuid-laptop",
	})
	var ok1 authOK
	readJSON(t, conn1, &ok1)

	// Connect desktop.
	conn2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial desktop: %v", err)
	}
	defer conn2.Close()

	var authReq2 authRequired
	readJSON(t, conn2, &authReq2)
	conn2.SetWriteDeadline(time.Now().Add(5 * time.Second))
	conn2.WriteJSON(authMessage{
		Type: typeAuth, Token: "nugget-desktop",
		ClientName: "granite", ClientID: "uuid-desktop",
	})
	var ok2 authOK
	readJSON(t, conn2, &ok2)

	// Both should resolve to nugget account.
	if ok1.Account != "nugget" || ok2.Account != "nugget" {
		t.Errorf("expected both accounts to be nugget, got %q and %q", ok1.Account, ok2.Account)
	}

	// ByAccount should return both.
	nuggetProviders := registry.ByAccount("nugget")
	if len(nuggetProviders) != 2 {
		t.Fatalf("expected 2 nugget providers, got %d", len(nuggetProviders))
	}

	// Verify different client names.
	names := map[string]bool{}
	for _, p := range nuggetProviders {
		names[p.ClientName] = true
	}
	if !names["deepslate"] || !names["granite"] {
		t.Errorf("expected deepslate and granite, got %v", names)
	}
}

func TestPingPong(t *testing.T) {
	registry := NewRegistry(nil)
	handler := NewHandler(testTokenIndex(), registry, nil)
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
		Token:      "test-secret",
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

	// Verify the connection is alive by sending a pong.
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
	registry := NewRegistry(nil)
	handler := NewHandler(testTokenIndex(), registry, nil)
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
		Token:      "test-secret",
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
	registry := NewRegistry(nil)
	handler := NewHandler(testTokenIndex(), registry, nil)
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
			Token:      "test-secret",
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
	srv, conn := dialTestServer(t, testTokenIndex())
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
	registry := NewRegistry(nil)
	handler := NewHandler(map[string]string{"tok": "test"}, registry, nil)
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
