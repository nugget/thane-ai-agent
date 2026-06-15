package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/platform/logging"
)

func TestHandleSystem(t *testing.T) {
	s := &Server{logger: testAPILogger()}
	s.SetConnManager(func() map[string]DependencyStatus {
		return map[string]DependencyStatus{
			"mqtt": {Name: "MQTT", Ready: true},
			"ha":   {Name: "Home Assistant", Ready: false, LastError: "refused"},
		}
	})
	rr := httptest.NewRecorder()
	s.handleSystem(rr, httptest.NewRequest(http.MethodGet, "/v1/system", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "degraded" {
		t.Errorf("status = %v, want degraded (a dependency is down)", body["status"])
	}
	if _, ok := body["uptime_seconds"]; !ok {
		t.Error("uptime_seconds missing")
	}
	if _, ok := body["version"]; !ok {
		t.Error("version missing")
	}
	if _, ok := body["health"].(map[string]any); !ok {
		t.Error("health missing or not a map")
	}
}

func TestHandleSystemLogs_BareArrayNewestFirst(t *testing.T) {
	s := &Server{logger: testAPILogger()}
	s.UseLogQuerier(fakeLogQuerier{byConv: map[string][]logging.LogEntry{
		"": {{ID: 1, Timestamp: at(100)}, {ID: 2, Timestamp: at(200)}},
	}})
	rr := httptest.NewRecorder()
	s.handleSystemLogs(rr, httptest.NewRequest(http.MethodGet, "/v1/system/logs", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if b := strings.TrimSpace(rr.Body.String()); !strings.HasPrefix(b, "[") {
		t.Fatalf("body = %s, want a bare JSON array", b)
	}
	var entries []logging.LogEntry
	if err := json.Unmarshal(rr.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 2 || entries[0].ID != 2 {
		t.Errorf("entries = %+v, want newest-first [2,1]", entries)
	}
}

func TestHandleSystemLogs_NoQuerier(t *testing.T) {
	s := &Server{logger: testAPILogger()}
	rr := httptest.NewRecorder()
	s.handleSystemLogs(rr, httptest.NewRequest(http.MethodGet, "/v1/system/logs", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when log querier unset", rr.Code)
	}
}
