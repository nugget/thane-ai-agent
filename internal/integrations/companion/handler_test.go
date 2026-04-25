package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// testTokenIndex returns a token index mapping "test-secret" to "nugget".
func testTokenIndex() map[string]string {
	return map[string]string{"test-secret": "nugget"}
}

// dialTestServer creates an httptest.Server with the companion handler and
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
	if err := conn1.WriteJSON(authMessage{
		Type: typeAuth, Token: "nugget-token",
		ClientName: "deepslate", ClientID: "uuid-nugget",
	}); err != nil {
		t.Fatalf("nugget auth WriteJSON: %v", err)
	}
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
	if err := conn2.WriteJSON(authMessage{
		Type: typeAuth, Token: "aimee-token",
		ClientName: "pocket", ClientID: "uuid-aimee",
	}); err != nil {
		t.Fatalf("aimee auth WriteJSON: %v", err)
	}
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
	if err := conn1.WriteJSON(authMessage{
		Type: typeAuth, Token: "nugget-laptop",
		ClientName: "deepslate", ClientID: "uuid-laptop",
	}); err != nil {
		t.Fatalf("laptop auth WriteJSON: %v", err)
	}
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
	if err := conn2.WriteJSON(authMessage{
		Type: typeAuth, Token: "nugget-desktop",
		ClientName: "granite", ClientID: "uuid-desktop",
	}); err != nil {
		t.Fatalf("desktop auth WriteJSON: %v", err)
	}
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
	if err := conn.WriteJSON(authMessage{
		Type:       typeAuth,
		Token:      "test-secret",
		ClientName: "Disconnect Test",
		ClientID:   "test-uuid-4",
	}); err != nil {
		t.Fatalf("send auth: %v", err)
	}
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
		if err := conn.WriteJSON(authMessage{
			Type:       typeAuth,
			Token:      "test-secret",
			ClientName: "Multi Test",
			ClientID:   "test-uuid-multi",
		}); err != nil {
			t.Fatalf("send auth %d: %v", i, err)
		}
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
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("unmarshal auth_failed: %v (raw: %s)", err, raw)
	}
	if msg.Type != typeAuthFailed {
		t.Fatalf("expected %q, got %q (raw: %s)", typeAuthFailed, msg.Type, raw)
	}
}

func TestUpgradeOnCorrectPath(t *testing.T) {
	registry := NewRegistry(nil)
	handler := NewHandler(map[string]string{"tok": "test"}, registry, nil)
	mux := http.NewServeMux()
	mux.Handle("GET /v1/companion/ws", handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + srv.URL[len("http"):] + "/v1/companion/ws"
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

func TestRegisterCapabilitiesAndCallRoundTrip(t *testing.T) {
	registry := NewRegistry(nil)
	handler := NewHandler(testTokenIndex(), registry, nil)
	mux := http.NewServeMux()
	mux.Handle("GET /v1/companion/ws", handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	wsURL := "ws" + srv.URL[len("http"):] + "/v1/companion/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	var authReq authRequired
	readJSON(t, conn, &authReq)
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(authMessage{
		Type:       typeAuth,
		Token:      "test-secret",
		ClientName: "Calendar Host",
		ClientID:   "test-uuid-calendar",
	}); err != nil {
		t.Fatalf("send auth: %v", err)
	}

	var ok authOK
	readJSON(t, conn, &ok)

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(registerCapabilitiesMessage{
		ID:   1,
		Type: typeRegisterCaps,
		Capabilities: []Capability{{
			Name:    "macos.calendar",
			Version: "1",
			Methods: []string{"list_events"},
		}},
	}); err != nil {
		t.Fatalf("send capability registration: %v", err)
	}

	var ack Message
	readJSON(t, conn, &ack)
	if ack.Type != typeResult {
		t.Fatalf("expected result ack, got %q", ack.Type)
	}
	if !ack.Success {
		t.Fatalf("expected successful capability ack, got %+v", ack)
	}

	type companionRequestRead struct {
		req companionRequestMessage
		err error
	}
	requestSeen := make(chan companionRequestRead, 1)
	go func() {
		var req companionRequestMessage
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := conn.ReadJSON(&req); err != nil {
			requestSeen <- companionRequestRead{err: fmt.Errorf("read companion request: %w", err)}
			return
		}
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteJSON(Message{
			ID:      req.ID,
			Type:    typeResult,
			Success: true,
			Result:  json.RawMessage(`{"events":[{"title":"Design Review"}]}`),
		}); err != nil {
			requestSeen <- companionRequestRead{err: fmt.Errorf("send result: %w", err)}
			return
		}
		requestSeen <- companionRequestRead{req: req}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := registry.Call(ctx, CallRequest{
		Account:    "nugget",
		Capability: "macos.calendar",
		Method:     "list_events",
		Params:     json.RawMessage(`{"start":"2026-04-02T09:00:00-05:00","end":"2026-04-02T17:00:00-05:00"}`),
	})
	if err != nil {
		t.Fatalf("registry.Call: %v", err)
	}

	var payload struct {
		Events []struct {
			Title string `json:"title"`
		} `json:"events"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(payload.Events) != 1 || payload.Events[0].Title != "Design Review" {
		t.Fatalf("unexpected result payload: %s", result)
	}

	request := <-requestSeen
	if request.err != nil {
		t.Fatal(request.err)
	}
	if request.req.Type != typeCompanionReq {
		t.Fatalf("expected companion_request, got %q", request.req.Type)
	}
	if request.req.Capability != "macos.calendar" {
		t.Errorf("capability: got %q, want %q", request.req.Capability, "macos.calendar")
	}
	if request.req.Method != "list_events" {
		t.Errorf("method: got %q, want %q", request.req.Method, "list_events")
	}
}

func TestRegistryCallRequiresAccountWhenMultipleAccountsMatch(t *testing.T) {
	registry := NewRegistry(nil)

	nugget := &Provider{
		ID:          "prov_nugget",
		Account:     "nugget",
		ClientName:  "MacBook Pro",
		ClientID:    "mbp",
		ConnectedAt: time.Now(),
		done:        make(chan struct{}),
	}
	nugget.setCapabilities([]Capability{{
		Name:    "macos.calendar",
		Methods: []string{"list_events"},
	}})
	registry.Add(nugget)

	aimee := &Provider{
		ID:          "prov_aimee",
		Account:     "aimee",
		ClientName:  "Studio Mac",
		ClientID:    "studio",
		ConnectedAt: time.Now(),
		done:        make(chan struct{}),
	}
	aimee.setCapabilities([]Capability{{
		Name:    "macos.calendar",
		Methods: []string{"list_events"},
	}})
	registry.Add(aimee)

	_, err := registry.Call(context.Background(), CallRequest{
		Capability: "macos.calendar",
		Method:     "list_events",
	})
	if err == nil {
		t.Fatal("expected ambiguity error, got nil")
	}
	if !strings.Contains(err.Error(), "multiple accounts") {
		t.Fatalf("expected multiple accounts error, got %v", err)
	}
}

func TestRegistryCallTrimsRoutingSelectors(t *testing.T) {
	registry := NewRegistry(nil)
	handler := NewHandler(testTokenIndex(), registry, nil)
	mux := http.NewServeMux()
	mux.Handle("GET /v1/platform/ws", handler)
	server := httptest.NewServer(mux)
	defer server.Close()

	wsURL := "ws" + server.URL[len("http"):] + "/v1/platform/ws"
	providerConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial provider: %v", err)
	}
	defer providerConn.Close()

	var authReq authRequired
	readJSON(t, providerConn, &authReq)
	providerConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := providerConn.WriteJSON(authMessage{
		Type:       typeAuth,
		Token:      "test-secret",
		ClientName: "Calendar Host",
		ClientID:   "test-uuid-calendar",
	}); err != nil {
		t.Fatalf("send auth: %v", err)
	}

	var ok authOK
	readJSON(t, providerConn, &ok)
	if ok.Account != "nugget" {
		t.Fatalf("expected account %q, got %q", "nugget", ok.Account)
	}

	providerConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := providerConn.WriteJSON(registerCapabilitiesMessage{
		ID:   1,
		Type: typeRegisterCaps,
		Capabilities: []Capability{{
			Name:    "macos.calendar",
			Version: "1",
			Methods: []string{"list_events"},
		}},
	}); err != nil {
		t.Fatalf("send capability registration: %v", err)
	}

	var ack Message
	readJSON(t, providerConn, &ack)
	if !ack.Success {
		t.Fatalf("expected successful capability ack, got %+v", ack)
	}

	type companionRequestRead struct {
		req companionRequestMessage
		err error
	}
	requestSeen := make(chan companionRequestRead, 1)
	go func() {
		var req companionRequestMessage
		providerConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := providerConn.ReadJSON(&req); err != nil {
			requestSeen <- companionRequestRead{err: fmt.Errorf("read companion request: %w", err)}
			return
		}
		providerConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := providerConn.WriteJSON(Message{
			ID:      req.ID,
			Type:    typeResult,
			Success: true,
			Result:  json.RawMessage(`{"events":[]}`),
		}); err != nil {
			requestSeen <- companionRequestRead{err: fmt.Errorf("send result: %w", err)}
			return
		}
		requestSeen <- companionRequestRead{req: req}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := registry.Call(ctx, CallRequest{
		Account:    " nugget ",
		ClientID:   " test-uuid-calendar ",
		Capability: " macos.calendar ",
		Method:     " list_events ",
		Params:     json.RawMessage(`{"start":"2026-04-02T09:00:00-05:00","end":"2026-04-02T17:00:00-05:00"}`),
	}); err != nil {
		t.Fatalf("registry.Call: %v", err)
	}

	request := <-requestSeen
	if request.err != nil {
		t.Fatal(request.err)
	}
	if request.req.Type != typeLegacyPlatformReq {
		t.Fatalf("expected legacy platform_request, got %q", request.req.Type)
	}
	if request.req.Capability != "macos.calendar" {
		t.Fatalf("capability: got %q, want %q", request.req.Capability, "macos.calendar")
	}
	if request.req.Method != "list_events" {
		t.Fatalf("method: got %q, want %q", request.req.Method, "list_events")
	}
}
