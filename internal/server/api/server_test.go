package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/database"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/models"
	"github.com/nugget/thane-ai-agent/internal/router"
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

func testAPILoopDefinitionRegistry(t *testing.T) *looppkg.DefinitionRegistry {
	t.Helper()

	registry, err := looppkg.NewDefinitionRegistry([]looppkg.Spec{
		{
			Name:       "metacog_like",
			Enabled:    true,
			Task:       "Observe and reflect.",
			Operation:  looppkg.OperationService,
			Completion: looppkg.CompletionNone,
			Profile: router.LoopProfile{
				Mission: "background",
			},
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
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
	}, 100, 25, 0, 0)
	stats.Record(usage.ModelIdentity{
		Model:         "mirror/gpt-oss:20b",
		UpstreamModel: "gpt-oss:20b",
		Resource:      "mirror",
		Provider:      "ollama",
	}, 50, 10, 0, 0)

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

	server := NewServer("", 0, nil, nil, nil, nil, store, nil, nil, nil, nil, testAPILogger())

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
	end, err := time.Parse(time.RFC3339, resp.End)
	if err != nil {
		t.Fatalf("parse end: %v", err)
	}
	if end.After(time.Now().UTC().Add(2 * time.Second)) {
		t.Fatalf("End = %s, want a non-future timestamp", resp.End)
	}
	if resp.Summary == nil || resp.Summary.TotalRecords != 2 {
		t.Fatalf("summary total_records = %#v, want 2", resp.Summary)
	}
	if len(resp.Groups) != 2 {
		t.Fatalf("groups len = %d, want 2", len(resp.Groups))
	}
}

func TestHandleUsageSummary_InvalidGroupBy(t *testing.T) {
	server := NewServer("", 0, nil, nil, nil, nil, testAPIUsageStore(t), nil, nil, nil, nil, testAPILogger())

	req := httptest.NewRequest(http.MethodGet, "/v1/usage/summary?group_by=bogus", nil)
	rec := httptest.NewRecorder()
	server.handleUsageSummary(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleUsageSummary_InvalidHours(t *testing.T) {
	server := NewServer("", 0, nil, nil, nil, nil, testAPIUsageStore(t), nil, nil, nil, nil, testAPILogger())

	req := httptest.NewRequest(http.MethodGet, "/v1/usage/summary?hours=zero", nil)
	rec := httptest.NewRecorder()
	server.handleUsageSummary(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleModelRegistry(t *testing.T) {
	registry := testAPIModelRegistry(t)
	server := NewServer("", 0, nil, nil, nil, registry, nil, nil, nil, nil, nil, testAPILogger())

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
	rtr := router.NewRouter(testAPILogger(), registry.Catalog().RouterConfig(10))
	server := NewServer("", 0, nil, rtr, nil, registry, nil, nil, nil, nil, nil, testAPILogger())

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

func TestHandleModelRegistryPolicySet_UpdatesRouterConfig(t *testing.T) {
	registry := testAPIModelRegistry(t)
	rtr := router.NewRouter(testAPILogger(), registry.Catalog().RouterConfig(10))
	server := NewServer("", 0, nil, rtr, nil, registry, nil, nil, nil, nil, nil, testAPILogger())

	body := bytes.NewBufferString(`{"deployment":"spark/gpt-oss:20b","state":"inactive","reason":"drain this node"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/model-registry/policy", body)
	rec := httptest.NewRecorder()
	server.handleModelRegistryPolicySet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	models := rtr.GetModels()
	if len(models) != 1 {
		t.Fatalf("len(GetModels()) = %d, want 1", len(models))
	}
	if models[0].Name != "mirror/gpt-oss:20b" {
		t.Fatalf("GetModels()[0].Name = %q, want %q", models[0].Name, "mirror/gpt-oss:20b")
	}
	if got := rtr.DefaultModel(); got != "mirror/gpt-oss:20b" {
		t.Fatalf("DefaultModel() = %q, want %q", got, "mirror/gpt-oss:20b")
	}
}

func TestHandleModelRegistryPolicySet_PromotesDiscoveredDeploymentIntoRouter(t *testing.T) {
	registry := testAPIModelRegistry(t)
	if err := registry.ApplyInventory(&models.Inventory{
		Resources: []models.ResourceInventory{
			{
				ResourceID: "mirror",
				Provider:   "ollama",
				Attempted:  true,
				Models: []models.DiscoveredModel{
					{Name: "qwen3-vl:latest", SupportsTools: true, SupportsStreaming: true, SupportsImages: true},
				},
			},
		},
	}, time.Now()); err != nil {
		t.Fatalf("ApplyInventory: %v", err)
	}

	rtr := router.NewRouter(testAPILogger(), registry.Catalog().RouterConfig(10))
	server := NewServer("", 0, nil, rtr, nil, registry, nil, nil, nil, nil, nil, testAPILogger())

	body := bytes.NewBufferString(`{"deployment":"mirror/qwen3-vl:latest","routable":true,"reason":"promote vision model"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/model-registry/policy", body)
	rec := httptest.NewRecorder()
	server.handleModelRegistryPolicySet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp modelRegistryPolicyResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Deployment.Routable {
		t.Fatalf("response deployment Routable = false, want true")
	}
	if resp.Deployment.RoutableSource != models.DeploymentPolicySourceOverlay {
		t.Fatalf("response RoutableSource = %q, want %q", resp.Deployment.RoutableSource, models.DeploymentPolicySourceOverlay)
	}

	found := false
	for _, model := range rtr.GetModels() {
		if model.Name == "mirror/qwen3-vl:latest" {
			found = true
			if !model.SupportsImages {
				t.Fatalf("router model = %+v, want image support", model)
			}
		}
	}
	if !found {
		t.Fatal("promoted deployment missing from router config")
	}
}

func TestHandleModelRegistryPolicySet_InvalidState(t *testing.T) {
	registry := testAPIModelRegistry(t)
	server := NewServer("", 0, nil, nil, nil, registry, nil, nil, nil, nil, nil, testAPILogger())

	body := bytes.NewBufferString(`{"deployment":"spark/gpt-oss:20b","state":"bogus"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/model-registry/policy", body)
	rec := httptest.NewRecorder()
	server.handleModelRegistryPolicySet(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleModelRegistryPolicySet_RequiresStateOrRoutable(t *testing.T) {
	registry := testAPIModelRegistry(t)
	server := NewServer("", 0, nil, nil, nil, registry, nil, nil, nil, nil, nil, testAPILogger())

	body := bytes.NewBufferString(`{"deployment":"spark/gpt-oss:20b"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/model-registry/policy", body)
	rec := httptest.NewRecorder()
	server.handleModelRegistryPolicySet(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleModelRegistryPolicySet_UnknownDeployment(t *testing.T) {
	registry := testAPIModelRegistry(t)
	server := NewServer("", 0, nil, nil, nil, registry, nil, nil, nil, nil, nil, testAPILogger())

	body := bytes.NewBufferString(`{"deployment":"missing/model","state":"flagged"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/model-registry/policy", body)
	rec := httptest.NewRecorder()
	server.handleModelRegistryPolicySet(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleModelRegistryPolicyDelete_UnknownDeployment(t *testing.T) {
	registry := testAPIModelRegistry(t)
	server := NewServer("", 0, nil, nil, nil, registry, nil, nil, nil, nil, nil, testAPILogger())

	req := httptest.NewRequest(http.MethodDelete, "/v1/model-registry/policy?deployment=missing/model", nil)
	rec := httptest.NewRecorder()
	server.handleModelRegistryPolicyDelete(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleModelRegistryPolicySetAndDelete_PersistenceCallbacks(t *testing.T) {
	registry := testAPIModelRegistry(t)
	var savedID string
	var savedPolicy models.DeploymentPolicy
	var deletedID string
	server := NewServer(
		"",
		0,
		nil,
		nil,
		nil,
		registry,
		nil,
		func(id string, policy models.DeploymentPolicy) error {
			savedID = id
			savedPolicy = policy
			return nil
		},
		func(id string) error {
			deletedID = id
			return nil
		},
		nil,
		nil,
		testAPILogger(),
	)

	body := bytes.NewBufferString(`{"deployment":"spark/gpt-oss:20b","state":"flagged","reason":"manual review"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/model-registry/policy", body)
	rec := httptest.NewRecorder()
	server.handleModelRegistryPolicySet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d, want 200", rec.Code)
	}
	if savedID != "spark/gpt-oss:20b" {
		t.Fatalf("savedID = %q, want %q", savedID, "spark/gpt-oss:20b")
	}
	if savedPolicy.State != models.DeploymentPolicyStateFlagged {
		t.Fatalf("saved state = %q, want %q", savedPolicy.State, models.DeploymentPolicyStateFlagged)
	}
	if savedPolicy.Reason != "manual review" {
		t.Fatalf("saved reason = %q, want %q", savedPolicy.Reason, "manual review")
	}
	if savedPolicy.UpdatedAt.IsZero() {
		t.Fatal("saved UpdatedAt = zero, want populated timestamp")
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/model-registry/policy?deployment=spark/gpt-oss:20b", nil)
	deleteRec := httptest.NewRecorder()
	server.handleModelRegistryPolicyDelete(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", deleteRec.Code)
	}
	if deletedID != "spark/gpt-oss:20b" {
		t.Fatalf("deletedID = %q, want %q", deletedID, "spark/gpt-oss:20b")
	}
}

func TestHandleModelRegistryPolicySet_PersistenceFailure(t *testing.T) {
	registry := testAPIModelRegistry(t)
	server := NewServer(
		"",
		0,
		nil,
		nil,
		nil,
		registry,
		nil,
		func(string, models.DeploymentPolicy) error { return errors.New("boom") },
		nil,
		nil,
		nil,
		testAPILogger(),
	)

	body := bytes.NewBufferString(`{"deployment":"spark/gpt-oss:20b","state":"flagged"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/model-registry/policy", body)
	rec := httptest.NewRecorder()
	server.handleModelRegistryPolicySet(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	snap := registry.Snapshot()
	dep := findRegistryDeployment(snap, "spark/gpt-oss:20b")
	if !dep.found {
		t.Fatal("deployment missing from snapshot")
	}
	if dep.snapshot.PolicySource != models.DeploymentPolicySourceDefault {
		t.Fatalf("PolicySource = %q, want %q after persistence failure", dep.snapshot.PolicySource, models.DeploymentPolicySourceDefault)
	}
}

func TestHandleModelRegistryResourcePolicySetAndDelete(t *testing.T) {
	registry := testAPIModelRegistry(t)
	rtr := router.NewRouter(testAPILogger(), registry.Catalog().RouterConfig(10))
	server := NewServer("", 0, nil, rtr, nil, registry, nil, nil, nil, nil, nil, testAPILogger())

	body := bytes.NewBufferString(`{"resource":"spark","state":"inactive","reason":"office hours"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/model-registry/resource-policy", body)
	rec := httptest.NewRecorder()
	server.handleModelRegistryResourcePolicySet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d, want 200", rec.Code)
	}

	var setResp modelRegistryResourcePolicyResponse
	if err := json.NewDecoder(rec.Body).Decode(&setResp); err != nil {
		t.Fatalf("decode set response: %v", err)
	}
	if setResp.Resource.PolicyState != models.DeploymentPolicyStateInactive {
		t.Fatalf("set resource policy state = %q, want %q", setResp.Resource.PolicyState, models.DeploymentPolicyStateInactive)
	}
	if setResp.Resource.PolicySource != models.DeploymentPolicySourceOverlay {
		t.Fatalf("set resource policy source = %q, want %q", setResp.Resource.PolicySource, models.DeploymentPolicySourceOverlay)
	}
	if setResp.Resource.PolicyReason != "office hours" {
		t.Fatalf("set resource policy reason = %q, want %q", setResp.Resource.PolicyReason, "office hours")
	}

	modelsCfg := rtr.GetModels()
	if len(modelsCfg) != 1 || modelsCfg[0].Name != "mirror/gpt-oss:20b" {
		t.Fatalf("router models after resource disable = %+v, want only mirror/gpt-oss:20b", modelsCfg)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/model-registry/resource-policy?resource=spark", nil)
	deleteRec := httptest.NewRecorder()
	server.handleModelRegistryResourcePolicyDelete(deleteRec, deleteReq)

	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", deleteRec.Code)
	}

	var deleteResp modelRegistryResourcePolicyResponse
	if err := json.NewDecoder(deleteRec.Body).Decode(&deleteResp); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if deleteResp.Resource.PolicySource != models.DeploymentPolicySourceDefault {
		t.Fatalf("delete resource policy source = %q, want %q", deleteResp.Resource.PolicySource, models.DeploymentPolicySourceDefault)
	}
	if len(rtr.GetModels()) != 2 {
		t.Fatalf("len(router models) after clear = %d, want 2", len(rtr.GetModels()))
	}
}

func TestHandleModelRegistryResourcePolicySet_PersistenceFailure(t *testing.T) {
	registry := testAPIModelRegistry(t)
	server := NewServer(
		"",
		0,
		nil,
		nil,
		nil,
		registry,
		nil,
		nil,
		nil,
		func(string, models.ResourcePolicy) error { return errors.New("boom") },
		nil,
		testAPILogger(),
	)

	body := bytes.NewBufferString(`{"resource":"spark","state":"inactive"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/model-registry/resource-policy", body)
	rec := httptest.NewRecorder()
	server.handleModelRegistryResourcePolicySet(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	snap := registry.Snapshot()
	res := findRegistryResource(snap, "spark")
	if !res.found {
		t.Fatal("resource missing from snapshot")
	}
	if res.snapshot.PolicySource != models.DeploymentPolicySourceDefault {
		t.Fatalf("PolicySource = %q, want %q after persistence failure", res.snapshot.PolicySource, models.DeploymentPolicySourceDefault)
	}
}

func TestHandleLoopDefinitions(t *testing.T) {
	registry := testAPILoopDefinitionRegistry(t)
	server := NewServer("", 0, nil, nil, nil, nil, nil, nil, nil, nil, nil, testAPILogger())
	server.UseLoopDefinitionRegistry(registry)
	server.ConfigureLoopDefinitionView(func() *looppkg.DefinitionRegistryView {
		return looppkg.BuildDefinitionRegistryView(registry.Snapshot(), map[string]looppkg.DefinitionRuntimeStatus{
			"metacog_like": {
				Running: true,
				LoopID:  "loop-live-1",
				State:   looppkg.StateSleeping,
			},
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/loop-definitions", nil)
	rec := httptest.NewRecorder()
	server.handleLoopDefinitions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var snap looppkg.DefinitionRegistryView
	if err := json.NewDecoder(rec.Body).Decode(&snap); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(snap.Definitions) != 1 {
		t.Fatalf("definitions len = %d, want 1", len(snap.Definitions))
	}
	if snap.Definitions[0].Name != "metacog_like" {
		t.Fatalf("definition name = %q, want metacog_like", snap.Definitions[0].Name)
	}
	if !snap.Definitions[0].Runtime.Running || snap.Definitions[0].Runtime.LoopID != "loop-live-1" {
		t.Fatalf("runtime = %+v, want running loop-live-1", snap.Definitions[0].Runtime)
	}
}

func TestHandleLoopDefinitionSetAndDelete(t *testing.T) {
	registry := testAPILoopDefinitionRegistry(t)
	var savedSpec looppkg.Spec
	var savedAt time.Time
	var deleted string
	var reconciled []string
	server := NewServer("", 0, nil, nil, nil, nil, nil, nil, nil, nil, nil, testAPILogger())
	server.UseLoopDefinitionRegistry(registry)
	server.ConfigureLoopDefinitionView(func() *looppkg.DefinitionRegistryView {
		return looppkg.BuildDefinitionRegistryView(registry.Snapshot(), nil)
	})
	server.ConfigureLoopDefinitionPersistence(
		func(spec looppkg.Spec, updatedAt time.Time) error {
			savedSpec = spec
			savedAt = updatedAt
			return nil
		},
		func(name string) error {
			deleted = name
			return nil
		},
	)
	server.ConfigureLoopDefinitionLifecycle(nil, nil, func(_ context.Context, name string) error {
		reconciled = append(reconciled, name)
		return nil
	}, nil)

	body := bytes.NewBufferString(`{"spec":{"name":"room_monitor","task":"Watch the office.","operation":"service","completion":"conversation","profile":{"mission":"background"},"sleep_min":"5m","sleep_max":"30m","sleep_default":"10m"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/loop-definitions", body)
	rec := httptest.NewRecorder()
	server.handleLoopDefinitionSet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d, want 200", rec.Code)
	}
	if savedSpec.Name != "room_monitor" {
		t.Fatalf("savedSpec.Name = %q, want room_monitor", savedSpec.Name)
	}
	if savedAt.IsZero() {
		t.Fatal("savedAt = zero, want populated timestamp")
	}

	var setResp loopDefinitionResponse
	if err := json.NewDecoder(rec.Body).Decode(&setResp); err != nil {
		t.Fatalf("decode set response: %v", err)
	}
	if setResp.Definition.Source != looppkg.DefinitionSourceOverlay {
		t.Fatalf("source = %q, want overlay", setResp.Definition.Source)
	}
	if setResp.Definition.Spec.SleepMin != 5*time.Minute {
		t.Fatalf("sleep_min = %v, want 5m", setResp.Definition.Spec.SleepMin)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/loop-definitions/room_monitor", nil)
	deleteReq.SetPathValue("name", "room_monitor")
	deleteRec := httptest.NewRecorder()
	server.handleLoopDefinitionDelete(deleteRec, deleteReq)

	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", deleteRec.Code)
	}
	if deleted != "room_monitor" {
		t.Fatalf("deleted = %q, want room_monitor", deleted)
	}
	if len(reconciled) != 2 || reconciled[0] != "room_monitor" || reconciled[1] != "room_monitor" {
		t.Fatalf("reconciled = %v, want [room_monitor room_monitor]", reconciled)
	}
}

func TestHandleLoopDefinitionSet_ConfigDefinitionConflict(t *testing.T) {
	registry := testAPILoopDefinitionRegistry(t)
	server := NewServer("", 0, nil, nil, nil, nil, nil, nil, nil, nil, nil, testAPILogger())
	server.UseLoopDefinitionRegistry(registry)

	body := bytes.NewBufferString(`{"spec":{"name":"metacog_like","task":"Override config.","operation":"service"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/loop-definitions", body)
	rec := httptest.NewRecorder()
	server.handleLoopDefinitionSet(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}

func TestHandleLoopDefinitionPolicySetAndDelete(t *testing.T) {
	registry := testAPILoopDefinitionRegistry(t)
	var persisted looppkg.DefinitionPolicy
	var deleted string
	server := NewServer("", 0, nil, nil, nil, nil, nil, nil, nil, nil, nil, testAPILogger())
	server.UseLoopDefinitionRegistry(registry)
	server.ConfigureLoopDefinitionView(func() *looppkg.DefinitionRegistryView {
		return looppkg.BuildDefinitionRegistryView(registry.Snapshot(), map[string]looppkg.DefinitionRuntimeStatus{
			"metacog_like": {
				Running: true,
				LoopID:  "loop-live-1",
				State:   looppkg.StateSleeping,
			},
		})
	})
	server.ConfigureLoopDefinitionLifecycle(
		func(name string, policy looppkg.DefinitionPolicy) error {
			if name != "metacog_like" {
				t.Fatalf("persist name = %q, want metacog_like", name)
			}
			persisted = policy
			return nil
		},
		func(name string) error {
			deleted = name
			return nil
		},
		nil,
		nil,
	)

	body := bytes.NewBufferString(`{"name":"metacog_like","state":"inactive","reason":"quiet hours"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/loop-definitions/policy", body)
	rec := httptest.NewRecorder()
	server.handleLoopDefinitionPolicySet(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d, want 200", rec.Code)
	}
	if persisted.Reason != "quiet hours" {
		t.Fatalf("persisted = %+v, want quiet hours", persisted)
	}

	var setResp loopDefinitionResponse
	if err := json.NewDecoder(rec.Body).Decode(&setResp); err != nil {
		t.Fatalf("decode set response: %v", err)
	}
	if setResp.Definition.PolicyState != looppkg.DefinitionPolicyStateInactive || setResp.Definition.PolicySource != looppkg.DefinitionPolicySourceOverlay {
		t.Fatalf("policy = %q/%q, want inactive/overlay", setResp.Definition.PolicyState, setResp.Definition.PolicySource)
	}
	if setResp.Definition.Runtime.LoopID != "loop-live-1" {
		t.Fatalf("runtime loop_id = %q, want loop-live-1", setResp.Definition.Runtime.LoopID)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/loop-definitions/policy?name=metacog_like", nil)
	deleteRec := httptest.NewRecorder()
	server.handleLoopDefinitionPolicyDelete(deleteRec, deleteReq)

	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", deleteRec.Code)
	}
	if deleted != "metacog_like" {
		t.Fatalf("deleted = %q, want metacog_like", deleted)
	}
}

func TestHandleLoopDefinitionLaunch(t *testing.T) {
	registry := testAPILoopDefinitionRegistry(t)
	server := NewServer("", 0, nil, nil, nil, nil, nil, nil, nil, nil, nil, testAPILogger())
	server.UseLoopDefinitionRegistry(registry)
	server.ConfigureLoopDefinitionView(func() *looppkg.DefinitionRegistryView {
		return looppkg.BuildDefinitionRegistryView(registry.Snapshot(), nil)
	})
	server.ConfigureLoopDefinitionLifecycle(nil, nil, nil, func(_ context.Context, name string, launch looppkg.Launch) (looppkg.LaunchResult, error) {
		if name != "metacog_like" {
			t.Fatalf("launch name = %q, want metacog_like", name)
		}
		if launch.CompletionConversationID != "conv-1" {
			t.Fatalf("completion conversation = %q, want conv-1", launch.CompletionConversationID)
		}
		return looppkg.LaunchResult{
			LoopID:    "loop-123",
			Operation: looppkg.OperationService,
			Detached:  true,
		}, nil
	})

	body := bytes.NewBufferString(`{"launch":{"completion_conversation_id":"conv-1"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/loop-definitions/metacog_like/launch", body)
	req.SetPathValue("name", "metacog_like")
	rec := httptest.NewRecorder()
	server.handleLoopDefinitionLaunch(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp loopDefinitionLaunchResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Result.LoopID != "loop-123" {
		t.Fatalf("loop_id = %q, want loop-123", resp.Result.LoopID)
	}
}
