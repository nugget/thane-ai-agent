package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/models"
	"github.com/nugget/thane-ai-agent/internal/usage"
)

func testAPILogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testAPIUsageStore(t *testing.T) *usage.Store {
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
	return store
}

func testAPIModelRegistry(t *testing.T) *models.Registry {
	t.Helper()

	cfg := &config.Config{}
	cfg.Models.LocalFirst = true
	cfg.Models.Default = "spark/gpt-oss:20b"
	cfg.Models.Resources = map[string]config.ModelServerConfig{
		"mirror": {URL: "http://mirror.example", Provider: "ollama"},
		"spark":  {URL: "http://spark.example", Provider: "ollama"},
	}
	cfg.Models.Available = []config.ModelConfig{
		{
			Name:          "gpt-oss:20b",
			Resource:      "mirror",
			SupportsTools: true,
			ContextWindow: 8192,
			Speed:         6,
			Quality:       6,
			CostTier:      0,
		},
		{
			Name:          "gpt-oss:20b",
			Resource:      "spark",
			SupportsTools: true,
			ContextWindow: 8192,
			Speed:         6,
			Quality:       6,
			CostTier:      0,
		},
	}

	base, err := models.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("models.BuildCatalog: %v", err)
	}

	registry, err := models.NewRegistry(base)
	if err != nil {
		t.Fatalf("models.NewRegistry: %v", err)
	}
	return registry
}

func TestSimpleChatRequest_Parsing(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantMsg string
		wantID  string
	}{
		{
			name:    "full request",
			json:    `{"message": "turn on the lights", "conversation_id": "test-conv"}`,
			wantMsg: "turn on the lights",
			wantID:  "test-conv",
		},
		{
			name:    "message only",
			json:    `{"message": "hello"}`,
			wantMsg: "hello",
			wantID:  "", // Should default to "default" in handler
		},
		{
			name:    "empty message",
			json:    `{"message": ""}`,
			wantMsg: "",
			wantID:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req SimpleChatRequest
			if err := json.NewDecoder(bytes.NewReader([]byte(tt.json))).Decode(&req); err != nil {
				t.Fatalf("failed to parse: %v", err)
			}

			if req.Message != tt.wantMsg {
				t.Errorf("message = %q, want %q", req.Message, tt.wantMsg)
			}
			if req.ConversationID != tt.wantID {
				t.Errorf("conversation_id = %q, want %q", req.ConversationID, tt.wantID)
			}
		})
	}
}

func TestSimpleChatRequest_DefaultConversationID(t *testing.T) {
	req := SimpleChatRequest{Message: "hello"}

	// Simulate handler logic
	convID := req.ConversationID
	if convID == "" {
		convID = "default"
	}

	if convID != "default" {
		t.Errorf("expected 'default', got %q", convID)
	}
}

func TestSimpleChatResponse_JSON(t *testing.T) {
	resp := SimpleChatResponse{
		Response:       "The kitchen is 22°C.",
		Model:          "qwen2.5:72b",
		ConversationID: "kitchen-conv",
		ToolCalls:      []string{"get_state"},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded SimpleChatResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.Response != resp.Response {
		t.Errorf("response mismatch")
	}
	if decoded.Model != resp.Model {
		t.Errorf("model mismatch")
	}
	if decoded.ConversationID != resp.ConversationID {
		t.Errorf("conversation_id mismatch")
	}
	if len(decoded.ToolCalls) != 1 || decoded.ToolCalls[0] != "get_state" {
		t.Errorf("tool_calls mismatch")
	}
}

func TestSimpleChatResponse_OmitEmptyToolCalls(t *testing.T) {
	resp := SimpleChatResponse{
		Response:       "Hello!",
		Model:          "test",
		ConversationID: "default",
		// No ToolCalls
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// tool_calls should be omitted when empty
	if bytes.Contains(data, []byte(`"tool_calls":[]`)) {
		t.Error("empty tool_calls should be omitted")
	}
}

func TestSessionStatsSnapshot_IncludesDeploymentBreakdowns(t *testing.T) {
	stats := &SessionStats{
		ByModel:         make(map[string]usage.Summary),
		ByUpstreamModel: make(map[string]usage.Summary),
		ByProvider:      make(map[string]usage.Summary),
		ByResource:      make(map[string]usage.Summary),
	}

	stats.Record(usage.ModelIdentity{
		Model:         "mirror/gpt-oss:20b",
		UpstreamModel: "gpt-oss:20b",
		Resource:      "mirror",
		Provider:      "ollama",
	}, 100, 25)
	stats.Record(usage.ModelIdentity{
		Model:         "mirror/gpt-oss:20b",
		UpstreamModel: "gpt-oss:20b",
		Resource:      "mirror",
		Provider:      "ollama",
	}, 50, 10)

	snap := stats.Snapshot()
	if snap.TotalRequests != 2 {
		t.Fatalf("TotalRequests = %d, want 2", snap.TotalRequests)
	}
	if snap.ByModel["mirror/gpt-oss:20b"].TotalRecords != 2 {
		t.Fatalf("by_model records = %d, want 2", snap.ByModel["mirror/gpt-oss:20b"].TotalRecords)
	}
	if snap.ByUpstreamModel["gpt-oss:20b"].TotalInputTokens != 150 {
		t.Fatalf("by_upstream_model input = %d, want 150", snap.ByUpstreamModel["gpt-oss:20b"].TotalInputTokens)
	}
	if snap.ByProvider["ollama"].TotalOutputTokens != 35 {
		t.Fatalf("by_provider output = %d, want 35", snap.ByProvider["ollama"].TotalOutputTokens)
	}
	if snap.ByResource["mirror"].TotalRecords != 2 {
		t.Fatalf("by_resource records = %d, want 2", snap.ByResource["mirror"].TotalRecords)
	}
}

func TestHandleUsageSummary(t *testing.T) {
	store := testAPIUsageStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, rec := range []usage.Record{
		{
			Timestamp:     now,
			RequestID:     "r1",
			Model:         "mirror/gpt-oss:20b",
			UpstreamModel: "gpt-oss:20b",
			Resource:      "mirror",
			Provider:      "ollama",
			InputTokens:   120,
			OutputTokens:  30,
			CostUSD:       1.5,
			Role:          "interactive",
		},
		{
			Timestamp:     now,
			RequestID:     "r2",
			Model:         "spark/gpt-oss:20b",
			UpstreamModel: "gpt-oss:20b",
			Resource:      "spark",
			Provider:      "ollama",
			InputTokens:   80,
			OutputTokens:  20,
			CostUSD:       1.0,
			Role:          "delegate",
		},
	} {
		if err := store.Record(ctx, rec); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	server := NewServer("", 0, nil, nil, nil, nil, store, testAPILogger())

	req := httptest.NewRequest(http.MethodGet, "/v1/usage/summary?hours=48&group_by=resource", nil)
	rec := httptest.NewRecorder()
	server.handleUsageSummary(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp usageSummaryResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Hours != 48 {
		t.Fatalf("Hours = %d, want 48", resp.Hours)
	}
	if resp.GroupBy != "resource" {
		t.Fatalf("GroupBy = %q, want %q", resp.GroupBy, "resource")
	}
	if resp.Summary == nil || resp.Summary.TotalRecords != 2 {
		t.Fatalf("summary total_records = %#v, want 2", resp.Summary)
	}
	if len(resp.Groups) != 2 {
		t.Fatalf("groups len = %d, want 2", len(resp.Groups))
	}
}

func TestHandleUsageSummary_InvalidGroupBy(t *testing.T) {
	server := NewServer("", 0, nil, nil, nil, nil, testAPIUsageStore(t), testAPILogger())

	req := httptest.NewRequest(http.MethodGet, "/v1/usage/summary?group_by=bogus", nil)
	rec := httptest.NewRecorder()
	server.handleUsageSummary(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleUsageSummary_InvalidHours(t *testing.T) {
	server := NewServer("", 0, nil, nil, nil, nil, testAPIUsageStore(t), testAPILogger())

	req := httptest.NewRequest(http.MethodGet, "/v1/usage/summary?hours=zero", nil)
	rec := httptest.NewRecorder()
	server.handleUsageSummary(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleModelRegistry(t *testing.T) {
	registry := testAPIModelRegistry(t)
	server := NewServer("", 0, nil, nil, nil, registry, nil, testAPILogger())

	req := httptest.NewRequest(http.MethodGet, "/v1/model-registry", nil)
	rec := httptest.NewRecorder()
	server.handleModelRegistry(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var snap models.RegistrySnapshot
	if err := json.NewDecoder(rec.Body).Decode(&snap); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(snap.Deployments) != 2 {
		t.Fatalf("deployments len = %d, want 2", len(snap.Deployments))
	}
	if snap.Deployments[1].ID != "spark/gpt-oss:20b" {
		t.Fatalf("deployment id = %q, want %q", snap.Deployments[1].ID, "spark/gpt-oss:20b")
	}
	if snap.Deployments[1].PolicyState != models.DeploymentPolicyStateActive {
		t.Fatalf("policy state = %q, want %q", snap.Deployments[1].PolicyState, models.DeploymentPolicyStateActive)
	}
	if snap.Deployments[1].PolicySource != models.DeploymentPolicySourceDefault {
		t.Fatalf("policy source = %q, want %q", snap.Deployments[1].PolicySource, models.DeploymentPolicySourceDefault)
	}
}

func TestHandleModelRegistryPolicySetAndDelete(t *testing.T) {
	registry := testAPIModelRegistry(t)
	server := NewServer("", 0, nil, nil, nil, registry, nil, testAPILogger())

	body := bytes.NewBufferString(`{"deployment":"spark/gpt-oss:20b","state":"flagged","reason":"manual review"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/model-registry/policy", body)
	rec := httptest.NewRecorder()
	server.handleModelRegistryPolicySet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d, want 200", rec.Code)
	}

	var setResp modelRegistryPolicyResponse
	if err := json.NewDecoder(rec.Body).Decode(&setResp); err != nil {
		t.Fatalf("decode set response: %v", err)
	}
	if setResp.Deployment.PolicyState != models.DeploymentPolicyStateFlagged {
		t.Fatalf("set policy state = %q, want %q", setResp.Deployment.PolicyState, models.DeploymentPolicyStateFlagged)
	}
	if setResp.Deployment.PolicySource != models.DeploymentPolicySourceOverlay {
		t.Fatalf("set policy source = %q, want %q", setResp.Deployment.PolicySource, models.DeploymentPolicySourceOverlay)
	}
	if setResp.Deployment.PolicyReason != "manual review" {
		t.Fatalf("set policy reason = %q, want %q", setResp.Deployment.PolicyReason, "manual review")
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/model-registry/policy?deployment=spark/gpt-oss:20b", nil)
	deleteRec := httptest.NewRecorder()
	server.handleModelRegistryPolicyDelete(deleteRec, deleteReq)

	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", deleteRec.Code)
	}

	var deleteResp modelRegistryPolicyResponse
	if err := json.NewDecoder(deleteRec.Body).Decode(&deleteResp); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if deleteResp.Deployment.PolicyState != models.DeploymentPolicyStateActive {
		t.Fatalf("delete policy state = %q, want %q", deleteResp.Deployment.PolicyState, models.DeploymentPolicyStateActive)
	}
	if deleteResp.Deployment.PolicySource != models.DeploymentPolicySourceDefault {
		t.Fatalf("delete policy source = %q, want %q", deleteResp.Deployment.PolicySource, models.DeploymentPolicySourceDefault)
	}
}

func TestHandleModelRegistryPolicySet_InvalidState(t *testing.T) {
	registry := testAPIModelRegistry(t)
	server := NewServer("", 0, nil, nil, nil, registry, nil, testAPILogger())

	body := bytes.NewBufferString(`{"deployment":"spark/gpt-oss:20b","state":"bogus"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/model-registry/policy", body)
	rec := httptest.NewRecorder()
	server.handleModelRegistryPolicySet(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
