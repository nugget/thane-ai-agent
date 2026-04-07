package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/events"
	"github.com/nugget/thane-ai-agent/internal/logging"
	"github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/models"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/usage"
)

// --- Test Doubles ---

// stubRegistry implements [LoopRegistry] for tests.
type stubRegistry struct {
	statuses []loop.Status
	loops    map[string]*loop.Loop
}

func (r *stubRegistry) Statuses() []loop.Status { return r.statuses }
func (r *stubRegistry) Get(id string) *loop.Loop {
	if r.loops == nil {
		return nil
	}
	return r.loops[id]
}

// stubLogQuerier implements [LogQuerier] for tests.
type stubLogQuerier struct {
	entries []logging.LogEntry
	err     error
}

func (q *stubLogQuerier) Query(_ logging.QueryParams) ([]logging.LogEntry, error) {
	return q.entries, q.err
}

// stubContentQuerier implements [ContentQuerier] for tests.
type stubContentQuerier struct {
	detail        *logging.RequestDetail
	err           error
	lastRequestID string
}

func (q *stubContentQuerier) QueryRequestDetail(requestID string) (*logging.RequestDetail, error) {
	q.lastRequestID = requestID
	return q.detail, q.err
}

func newUsageStoreForTest(t *testing.T, records ...usage.Record) *usage.Store {
	t.Helper()

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store, err := usage.NewStore(db)
	if err != nil {
		t.Fatalf("usage.NewStore: %v", err)
	}
	for _, rec := range records {
		if err := store.Record(t.Context(), rec); err != nil {
			t.Fatalf("store.Record(%q): %v", rec.RequestID, err)
		}
	}
	return store
}

func newTestServer(reg LoopRegistry, lq LogQuerier, bus *events.Bus) *WebServer {
	return NewWebServer(Config{
		LoopRegistry: reg,
		EventBus:     bus,
		LogQuerier:   lq,
	})
}

// stubSystemStatus implements [SystemStatusProvider] for tests.
type stubSystemStatus struct {
	health        map[string]ServiceHealth
	uptime        time.Duration
	version       map[string]string
	modelRegistry *models.RegistrySnapshot
	routerStats   *router.Stats
	capCatalog    *toolcatalog.CapabilityCatalogView
}

func (s *stubSystemStatus) Health() map[string]ServiceHealth { return s.health }
func (s *stubSystemStatus) Uptime() time.Duration            { return s.uptime }
func (s *stubSystemStatus) Version() map[string]string       { return s.version }
func (s *stubSystemStatus) ModelRegistry() *models.RegistrySnapshot {
	return s.modelRegistry
}
func (s *stubSystemStatus) RouterStats() *router.Stats { return s.routerStats }
func (s *stubSystemStatus) CapabilityCatalog() *toolcatalog.CapabilityCatalogView {
	return s.capCatalog
}

// --- Tests ---

func TestHandleIndex(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Cognition Engine") {
		t.Error("GET / response does not contain 'Cognition Engine'")
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html", ct)
	}
}

func TestHandleStatic_CSS(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/static/style.css", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /static/style.css status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("Content-Type = %q, want text/css", ct)
	}
}

func TestHandleStatic_JS(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/static/app.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /static/app.js status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Errorf("Content-Type = %q, want application/javascript", ct)
	}
}

func TestHandleStatic_Blocked(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/static/secrets.txt", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("blocked extension status = %d, want 404", w.Code)
	}
}

func TestHandleStatic_NotFound(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/static/nonexistent.js", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("nonexistent file status = %d, want 404", w.Code)
	}
}

func TestHandleLoops(t *testing.T) {
	t.Parallel()

	reg := &stubRegistry{
		statuses: []loop.Status{
			{
				ID:         "loop-1",
				Name:       "metacognitive",
				State:      loop.StateSleeping,
				Iterations: 42,
			},
		},
	}
	srv := newTestServer(reg, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/loops", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/loops status = %d, want 200", resp.StatusCode)
	}

	var statuses []loop.Status
	if err := json.NewDecoder(resp.Body).Decode(&statuses); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	if statuses[0].Name != "metacognitive" {
		t.Errorf("name = %q, want metacognitive", statuses[0].Name)
	}
}

func TestHandleLoops_Empty(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/loops", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	body, _ := io.ReadAll(w.Result().Body)
	// Should return valid JSON (null or empty array), not an error.
	if strings.TrimSpace(string(body)) == "" {
		t.Error("empty response body")
	}
}

func TestHandleLoopLogs_NoQuerier(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/loops/loop-1/logs", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestHandleLoopLogs_NotFound(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, &stubLogQuerier{}, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/loops/nonexistent/logs", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleLoopEvents_Snapshot(t *testing.T) {
	t.Parallel()

	reg := &stubRegistry{
		statuses: []loop.Status{
			{ID: "loop-1", Name: "metacognitive", State: loop.StateSleeping},
		},
	}
	bus := events.New()
	srv := newTestServer(reg, nil, bus)
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	// Use a real test server to get a proper streaming response.
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/loops/events")
	if err != nil {
		t.Fatalf("GET /api/loops/events: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	// Read the initial snapshot event. We need to read enough bytes
	// to get the first event. Use a deadline to avoid blocking forever.
	buf := make([]byte, 4096)
	// Set a short read deadline via the response body.
	done := make(chan string, 1)
	go func() {
		n, _ := resp.Body.Read(buf)
		done <- string(buf[:n])
	}()

	select {
	case data := <-done:
		if !strings.Contains(data, "event: snapshot") {
			t.Errorf("first event should be snapshot, got: %s", data)
		}
		if !strings.Contains(data, "metacognitive") {
			t.Errorf("snapshot should contain loop name, got: %s", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for snapshot event")
	}
}

func TestHandleSystem_Healthy(t *testing.T) {
	t.Parallel()

	sys := &stubSystemStatus{
		health: map[string]ServiceHealth{
			"mqtt":          {Name: "MQTT", Ready: true, LastCheck: "2025-01-01T00:00:00Z"},
			"homeassistant": {Name: "Home Assistant", Ready: true},
		},
		uptime:  3*time.Hour + 42*time.Minute,
		version: map[string]string{"version": "v0.1.0", "git_commit": "abc1234"},
		modelRegistry: &models.RegistrySnapshot{
			Generation:   2,
			DefaultModel: "spark/gpt-oss:20b",
			Resources: []models.RegistryResourceSnapshot{
				{ID: "spark", Provider: "ollama", DiscoveredModels: 14},
			},
			Deployments: []models.RegistryDeploymentSnapshot{
				{ID: "spark/gpt-oss:20b", Model: "gpt-oss:20b", Resource: "spark", Source: models.DeploymentSourceConfig, Routable: true},
				{ID: "spark/qwen3:8b", Model: "qwen3:8b", Resource: "spark", Source: models.DeploymentSourceDiscovered, Routable: false},
			},
		},
		routerStats: &router.Stats{
			TotalRequests: 3,
			DeploymentStats: map[string]router.DeploymentStats{
				"spark/gpt-oss:20b": {Provider: "ollama", Resource: "spark", UpstreamModel: "gpt-oss:20b", Requests: 3, Successes: 3, AvgLatencyMs: 420, AvgTokensUsed: 1800},
			},
		},
		capCatalog: &toolcatalog.CapabilityCatalogView{
			Kind: "capability_catalog",
			ActivationTools: toolcatalog.CapabilityActionTools{
				Activate:   "activate_capability",
				Deactivate: "deactivate_capability",
				List:       "list_loaded_capabilities",
			},
			Capabilities: []toolcatalog.CapabilityCatalogEntry{
				{Tag: "forge", Status: "available", Description: "Forge tools", ToolCount: 12},
			},
		},
	}

	srv := NewWebServer(Config{
		LoopRegistry: &stubRegistry{},
		EventBus:     events.New(),
		SystemStatus: sys,
	})
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/system", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/system status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body["status"] != "healthy" {
		t.Errorf("status = %v, want healthy", body["status"])
	}
	if body["uptime"] != "3h42m0s" {
		t.Errorf("uptime = %v, want 3h42m0s", body["uptime"])
	}
	health, ok := body["health"].(map[string]any)
	if !ok {
		t.Fatal("health field missing or not a map")
	}
	if len(health) != 2 {
		t.Errorf("got %d services, want 2", len(health))
	}
	registry, ok := body["model_registry"].(map[string]any)
	if !ok {
		t.Fatal("model_registry field missing or not a map")
	}
	if registry["default_model"] != "spark/gpt-oss:20b" {
		t.Errorf("default_model = %v, want spark/gpt-oss:20b", registry["default_model"])
	}
	deployments, ok := registry["deployments"].([]any)
	if !ok || len(deployments) != 2 {
		t.Fatalf("deployments = %T len=%d, want 2 entries", registry["deployments"], len(deployments))
	}
	routerStats, ok := body["router_stats"].(map[string]any)
	if !ok {
		t.Fatal("router_stats field missing or not a map")
	}
	if routerStats["total_requests"] != float64(3) {
		t.Errorf("total_requests = %v, want 3", routerStats["total_requests"])
	}
	capCatalog, ok := body["capability_catalog"].(map[string]any)
	if !ok {
		t.Fatal("capability_catalog field missing or not a map")
	}
	caps, ok := capCatalog["capabilities"].([]any)
	if !ok || len(caps) != 1 {
		t.Fatalf("capability catalog entries = %T len=%d, want 1 entry", capCatalog["capabilities"], len(caps))
	}
}

func TestHandleSystem_Degraded(t *testing.T) {
	t.Parallel()

	sys := &stubSystemStatus{
		health: map[string]ServiceHealth{
			"mqtt": {Name: "MQTT", Ready: true},
			"ha":   {Name: "Home Assistant", Ready: false, LastError: "connection refused"},
		},
		uptime:  time.Minute,
		version: map[string]string{"version": "dev"},
	}

	srv := NewWebServer(Config{
		LoopRegistry: &stubRegistry{},
		EventBus:     events.New(),
		SystemStatus: sys,
	})
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/system", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	var body map[string]any
	if err := json.NewDecoder(w.Result().Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body["status"] != "degraded" {
		t.Errorf("status = %v, want degraded", body["status"])
	}
}

func TestHandleSystem_Nil(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/system", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when SystemStatus is nil", w.Code)
	}
}

func TestHandleSystemLogs(t *testing.T) {
	t.Parallel()

	lq := &stubLogQuerier{
		entries: []logging.LogEntry{
			{ID: 1, Level: "INFO", Msg: "startup complete", SourceFile: "cmd/thane/main.go"},
			{ID: 2, Level: "INFO", Msg: "service connected", SourceFile: "cmd/thane/main.go"},
		},
	}

	srv := newTestServer(&stubRegistry{}, lq, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/system/logs", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/system/logs status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	count, ok := body["count"].(float64)
	if !ok {
		t.Fatal("count field missing")
	}
	if count != 2 {
		t.Errorf("count = %v, want 2", count)
	}
}

func TestHandleSystemLogs_NoQuerier(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/system/logs", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when LogQuerier is nil", w.Code)
	}
}

func TestHandleRequestDetail_Found(t *testing.T) {
	t.Parallel()

	cq := &stubContentQuerier{
		detail: &logging.RequestDetail{
			RequestID:        "r_abc123",
			Model:            "test-model",
			UserContent:      "Hello",
			AssistantContent: "Hi!",
			IterationCount:   1,
			InputTokens:      100,
			OutputTokens:     50,
			ToolCalls:        []logging.ToolDetail{},
			CreatedAt:        "2025-01-01T00:00:00Z",
		},
	}

	srv := NewWebServer(Config{
		LoopRegistry:   &stubRegistry{},
		EventBus:       events.New(),
		ContentQuerier: cq,
	})
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/requests/r_abc123", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body["request_id"] != "r_abc123" {
		t.Errorf("request_id = %v, want r_abc123", body["request_id"])
	}
	if body["model"] != "test-model" {
		t.Errorf("model = %v, want test-model", body["model"])
	}
}

func TestHandleRequestDetail_NotFound(t *testing.T) {
	t.Parallel()

	cq := &stubContentQuerier{detail: nil}

	srv := NewWebServer(Config{
		LoopRegistry:   &stubRegistry{},
		EventBus:       events.New(),
		ContentQuerier: cq,
	})
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/requests/r_nonexistent", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleRequestDetail_NoQuerier(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/requests/r_test", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when ContentQuerier is nil", w.Code)
	}
}

func TestHandleRequestDetail_ProbeAvailable(t *testing.T) {
	t.Parallel()

	srv := NewWebServer(Config{
		LoopRegistry:   &stubRegistry{},
		EventBus:       events.New(),
		ContentQuerier: &stubContentQuerier{},
	})
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/request-detail/_probe", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if got := w.Header().Get("X-Request-Detail-Available"); got != "true" {
		t.Fatalf("header X-Request-Detail-Available = %q, want true", got)
	}
}

func TestHandleRequestDetail_ProbeUnavailable(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/request-detail/_probe", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if got := w.Header().Get("X-Request-Detail-Available"); got != "false" {
		t.Fatalf("header X-Request-Detail-Available = %q, want false", got)
	}
}

func TestHandleRequestDetail_AllowsLiteralProbeRequestID(t *testing.T) {
	t.Parallel()

	cq := &stubContentQuerier{
		detail: &logging.RequestDetail{
			RequestID: "_probe",
			Model:     "test-model",
		},
	}

	srv := NewWebServer(Config{
		LoopRegistry:   &stubRegistry{},
		EventBus:       events.New(),
		ContentQuerier: cq,
	})
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/requests/_probe", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if cq.lastRequestID != "_probe" {
		t.Fatalf("queried request id = %q, want _probe", cq.lastRequestID)
	}
}

func TestHandleUsageOverview(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	store := newUsageStoreForTest(t,
		usage.Record{
			Timestamp:      now.Add(-2 * time.Hour),
			RequestID:      "r_123",
			ConversationID: "conv-1",
			SessionID:      "sess-1",
			LoopID:         "loop-a",
			LoopName:       "battery watch",
			Model:          "edge/claude-sonnet",
			UpstreamModel:  "claude-sonnet",
			Resource:       "edge",
			Provider:       "anthropic",
			InputTokens:    100,
			OutputTokens:   50,
			CostUSD:        3.50,
			Role:           "scheduled",
			TaskName:       "battery_scan",
		},
		usage.Record{
			Timestamp:      now.Add(-1 * time.Hour),
			RequestID:      "r_456",
			ConversationID: "conv-2",
			SessionID:      "sess-2",
			LoopID:         "loop-a",
			LoopName:       "battery watch",
			Model:          "edge/claude-sonnet",
			UpstreamModel:  "claude-sonnet",
			Resource:       "edge",
			Provider:       "anthropic",
			InputTokens:    200,
			OutputTokens:   100,
			CostUSD:        0.75,
			Role:           "scheduled",
			TaskName:       "battery_scan",
		},
	)

	srv := NewWebServer(Config{
		LoopRegistry: &stubRegistry{},
		EventBus:     events.New(),
		UsageStore:   store,
	})
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/usage/overview?hours=48&limit=7", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp usageOverviewResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Hours != 48 {
		t.Fatalf("hours = %d, want 48", resp.Hours)
	}
	if resp.Summary == nil || resp.Summary.TotalCostUSD != 4.25 {
		t.Fatalf("summary = %#v, want total cost 4.25", resp.Summary)
	}
	if len(resp.ByProvider) != 1 || resp.ByProvider[0].Key != "anthropic" {
		t.Fatalf("by_provider = %#v, want anthropic", resp.ByProvider)
	}
	if len(resp.ByLoop) != 1 || resp.ByLoop[0].LoopID != "loop-a" {
		t.Fatalf("by_loop = %#v, want loop-a", resp.ByLoop)
	}
	if len(resp.TopRequests) != 2 || resp.TopRequests[0].RequestID != "r_123" {
		t.Fatalf("top_requests = %#v, want r_123 first and two rows", resp.TopRequests)
	}
	if resp.TopRequests[0].Summary.TotalCostUSD != 3.50 {
		t.Fatalf("top_requests[0] = %#v, want total cost 3.50", resp.TopRequests[0])
	}
}

func TestHandleUsageOverview_RequiresUsageQuerier(t *testing.T) {
	t.Parallel()

	srv := newTestServer(&stubRegistry{}, nil, events.New())
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/usage/overview", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestHandleUsageOverview_RejectsBadHours(t *testing.T) {
	t.Parallel()

	srv := NewWebServer(Config{
		LoopRegistry: &stubRegistry{},
		EventBus:     events.New(),
		UsageStore:   newUsageStoreForTest(t),
	})
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/usage/overview?hours=bogus", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
