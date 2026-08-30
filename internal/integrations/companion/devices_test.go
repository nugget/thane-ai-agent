package companion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeDeviceRecorder captures inventory events for assertions.
type fakeDeviceRecorder struct {
	mu           sync.Mutex
	connected    []recordedDevice
	capabilities []recordedManifest
	disconnected []recordedDevice
}

type recordedDevice struct {
	Account  string
	ClientID string
	Meta     DeviceMetadata
}

type recordedManifest struct {
	Account  string
	ClientID string
	Manifest string
}

func (f *fakeDeviceRecorder) RecordConnected(_ context.Context, account, clientID string, meta DeviceMetadata, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = append(f.connected, recordedDevice{account, clientID, meta})
	return nil
}

func (f *fakeDeviceRecorder) RecordCapabilities(_ context.Context, account, clientID string, manifest []byte, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capabilities = append(f.capabilities, recordedManifest{account, clientID, string(manifest)})
	return nil
}

func (f *fakeDeviceRecorder) RecordDisconnected(_ context.Context, account, clientID string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnected = append(f.disconnected, recordedDevice{Account: account, ClientID: clientID})
	return nil
}

func (f *fakeDeviceRecorder) snapshot() (connected, capabilities, disconnected int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.connected), len(f.capabilities), len(f.disconnected)
}

// waitFor polls until cond returns true or the deadline expires. The
// disconnect record is written by the handler goroutine after the
// client closes, so assertions on it need a grace window.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

// dialWithRecorder is dialTestServer with a device recorder wired in.
func dialWithRecorder(t *testing.T, recorder DeviceRecorder) (*httptest.Server, *websocket.Conn) {
	t.Helper()
	registry := NewRegistry(nil)
	handler := NewHandler(testTokenIndex(), registry, nil)
	handler.SetDeviceRecorder(recorder)
	srv := httptest.NewServer(handler)
	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		srv.Close()
		t.Fatalf("dial: %v", err)
	}
	return srv, conn
}

// authAs completes the handshake with the given identity fields.
func authAs(t *testing.T, conn *websocket.Conn, msg authMessage) {
	t.Helper()
	var authReq authRequired
	readJSON(t, conn, &authReq)
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	msg.Type = typeAuth
	msg.Token = "test-secret"
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatalf("send auth: %v", err)
	}
	var ok authOK
	readJSON(t, conn, &ok)
	if ok.Type != typeAuthOK {
		t.Fatalf("expected auth_ok, got %q", ok.Type)
	}
}

func TestDeviceRecorderFullLifecycle(t *testing.T) {
	recorder := &fakeDeviceRecorder{}
	srv, conn := dialWithRecorder(t, recorder)
	defer srv.Close()
	defer conn.Close()

	authAs(t, conn, authMessage{
		ClientName: "Test iPhone",
		ClientID:   "device-uuid-1",
		Platform:   "ios",
		AppVersion: "1.2.0",
		OSVersion:  "26.0",
	})

	// Authentication upserts the device with its reported metadata.
	waitFor(t, func() bool { c, _, _ := recorder.snapshot(); return c == 1 })
	recorder.mu.Lock()
	got := recorder.connected[0]
	recorder.mu.Unlock()
	if got.Account != "nugget" || got.ClientID != "device-uuid-1" {
		t.Errorf("connected identity = %s/%s", got.Account, got.ClientID)
	}
	if got.Meta.Platform != "ios" || got.Meta.AppVersion != "1.2.0" || got.Meta.OSVersion != "26.0" || got.Meta.ClientName != "Test iPhone" {
		t.Errorf("connected metadata = %+v", got.Meta)
	}

	// Capability registration persists the normalized manifest.
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(registerCapabilitiesMessage{
		ID:   1,
		Type: typeRegisterCaps,
		Capabilities: []Capability{
			{Name: "location", Version: "1", Methods: []string{"current"}},
		},
	}); err != nil {
		t.Fatalf("send register_capabilities: %v", err)
	}
	var result Message
	readJSON(t, conn, &result)
	if !result.Success {
		t.Fatalf("capability registration failed: %+v", result)
	}
	waitFor(t, func() bool { _, c, _ := recorder.snapshot(); return c == 1 })
	recorder.mu.Lock()
	manifest := recorder.capabilities[0]
	recorder.mu.Unlock()
	if manifest.Account != "nugget" || manifest.ClientID != "device-uuid-1" {
		t.Errorf("manifest identity = %s/%s", manifest.Account, manifest.ClientID)
	}
	if !strings.Contains(manifest.Manifest, `"location"`) {
		t.Errorf("manifest missing capability: %s", manifest.Manifest)
	}

	// Disconnect stamps the record; the registry removal and inventory
	// stamp share the connection-teardown path.
	conn.Close()
	waitFor(t, func() bool { _, _, d := recorder.snapshot(); return d == 1 })
	recorder.mu.Lock()
	gone := recorder.disconnected[0]
	recorder.mu.Unlock()
	if gone.Account != "nugget" || gone.ClientID != "device-uuid-1" {
		t.Errorf("disconnected identity = %s/%s", gone.Account, gone.ClientID)
	}
}

// TestDeviceRecorderSkipsAnonymousClients pins the published contract:
// a connection without a client_id stays fully functional live but is
// never recorded in the durable inventory. The assertion is
// deterministic: the wrapped handler signals when connection teardown
// (the last recorder call site) has fully returned, and a barrier op
// drains the recording queue behind anything mistakenly enqueued.
func TestDeviceRecorderSkipsAnonymousClients(t *testing.T) {
	recorder := &fakeDeviceRecorder{}
	registry := NewRegistry(nil)
	handler := NewHandler(testTokenIndex(), registry, nil)
	handler.SetDeviceRecorder(recorder)

	teardown := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		close(teardown)
	}))
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):]
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	authAs(t, conn, authMessage{ClientName: "Legacy Mac"})

	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(registerCapabilitiesMessage{
		ID:           1,
		Type:         typeRegisterCaps,
		Capabilities: []Capability{{Name: "calendar", Methods: []string{"events"}}},
	}); err != nil {
		t.Fatalf("send register_capabilities: %v", err)
	}
	var result Message
	readJSON(t, conn, &result)
	if !result.Success {
		t.Fatalf("capability registration failed: %+v", result)
	}
	conn.Close()

	select {
	case <-teardown:
	case <-time.After(5 * time.Second):
		t.Fatal("handler teardown did not complete")
	}
	drained := make(chan struct{})
	handler.enqueueDeviceOp(func(context.Context) { close(drained) })
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("device op queue did not drain")
	}

	c, caps, d := recorder.snapshot()
	if c != 0 || caps != 0 || d != 0 {
		t.Errorf("anonymous client was recorded: connected=%d capabilities=%d disconnected=%d", c, caps, d)
	}
}

// TestAuthCarriesDeviceMetadata pins the optional auth fields onto the
// provider snapshot, where the registry (and later the joined context
// view) reads them.
func TestAuthCarriesDeviceMetadata(t *testing.T) {
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

	authAs(t, conn, authMessage{
		ClientName: "Test iPhone",
		ClientID:   "device-uuid-2",
		Platform:   "ios",
		AppVersion: "1.2.0",
		OSVersion:  "26.0",
	})

	infos := registry.List()
	if len(infos) != 1 {
		t.Fatalf("registry has %d providers, want 1", len(infos))
	}
	info := infos[0]
	if info.Platform != "ios" || info.AppVersion != "1.2.0" || info.OSVersion != "26.0" {
		t.Errorf("provider metadata = platform=%q app=%q os=%q", info.Platform, info.AppVersion, info.OSVersion)
	}
}
