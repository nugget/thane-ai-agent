package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/models"
	routepkg "github.com/nugget/thane-ai-agent/internal/router"
)

type testModelRegistryDeps struct {
	reg                   *Registry
	modelRegistry         *models.Registry
	router                *routepkg.Router
	syncRouterCalls       int
	deploymentPersisted   map[string]models.DeploymentPolicy
	resourcePersisted     map[string]models.ResourcePolicy
	deploymentDeleteCalls []string
	resourceDeleteCalls   []string
}

func newTestModelRegistryDeps(t *testing.T) *testModelRegistryDeps {
	t.Helper()

	base, err := models.BuildCatalog(&config.Config{
		Models: config.ModelsConfig{
			Default:    "gpt-oss:20b",
			LocalFirst: true,
			Resources: map[string]config.ModelServerConfig{
				"deepslate": {URL: "http://deepslate:1234", Provider: "lmstudio"},
				"mirror":    {URL: "http://mirror:11434", Provider: "ollama"},
				"spark":     {URL: "http://spark:11434", Provider: "ollama"},
			},
			Available: []config.ModelConfig{
				{
					Name:          "qwen3-vl:4b",
					Resource:      "mirror",
					Provider:      "ollama",
					SupportsTools: true,
					ContextWindow: 8192,
					Speed:         7,
					Quality:       7,
					CostTier:      0,
				},
				{
					Name:          "gpt-oss:20b",
					Resource:      "spark",
					Provider:      "ollama",
					SupportsTools: true,
					ContextWindow: 8192,
					Speed:         6,
					Quality:       6,
					CostTier:      0,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("BuildCatalog: %v", err)
	}
	modelRegistry, err := models.NewRegistry(base)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if err := modelRegistry.ApplyInventory(&models.Inventory{
		Resources: []models.ResourceInventory{
			{
				ResourceID: "deepslate",
				Provider:   "lmstudio",
				Attempted:  true,
				Models: []models.DiscoveredModel{{
					Name:                "google/gemma-3-4b",
					ModelType:           "vlm",
					SupportsChat:        true,
					SupportsTools:       true,
					TrainedForToolUse:   true,
					SupportsStreaming:   true,
					SupportsImages:      true,
					ContextWindow:       131072,
					LoadedContextWindow: 131072,
					LoadedInstanceID:    "google/gemma-3-4b:7",
					CompatibilityType:   "chat-completions",
					State:               "loaded",
					Publisher:           "google",
					Family:              "gemma-3",
					Families:            []string{"gemma-3"},
					ParameterSize:       "4B",
				}},
			},
		},
	}, time.Date(2026, 4, 3, 22, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("ApplyInventory: %v", err)
	}

	router := routepkg.NewRouter(slog.Default(), modelRegistry.Catalog().RouterConfig(25))
	reg := NewEmptyRegistry()
	deps := &testModelRegistryDeps{
		reg:                 reg,
		modelRegistry:       modelRegistry,
		router:              router,
		deploymentPersisted: make(map[string]models.DeploymentPolicy),
		resourcePersisted:   make(map[string]models.ResourcePolicy),
	}
	reg.ConfigureModelRegistryTools(ModelRegistryToolDeps{
		Registry: modelRegistry,
		Router:   router,
		SyncRouter: func() {
			deps.syncRouterCalls++
			router.UpdateConfig(modelRegistry.Catalog().RouterConfig(25))
		},
		PersistDeploymentPolicy: func(id string, policy models.DeploymentPolicy) error {
			deps.deploymentPersisted[id] = policy
			return nil
		},
		DeleteDeploymentPolicy: func(id string) error {
			deps.deploymentDeleteCalls = append(deps.deploymentDeleteCalls, id)
			delete(deps.deploymentPersisted, id)
			return nil
		},
		PersistResourcePolicy: func(id string, policy models.ResourcePolicy) error {
			deps.resourcePersisted[id] = policy
			return nil
		},
		DeleteResourcePolicy: func(id string) error {
			deps.resourceDeleteCalls = append(deps.resourceDeleteCalls, id)
			delete(deps.resourcePersisted, id)
			return nil
		},
	})
	return deps
}

func TestConfigureModelRegistryTools_RegistersTools(t *testing.T) {
	deps := newTestModelRegistryDeps(t)

	for _, name := range []string{
		"model_registry_summary",
		"model_registry_list",
		"model_registry_get",
		"model_resource_set_policy",
		"model_deployment_set_policy",
		"model_route_explain",
	} {
		if deps.reg.Get(name) == nil {
			t.Fatalf("%s tool not registered", name)
		}
	}
	if deps.reg.modelRegistry != deps.modelRegistry {
		t.Fatal("model registry dependency was not stored")
	}
	if deps.reg.modelRouter != deps.router {
		t.Fatal("model router dependency was not stored")
	}
}

func TestModelRegistrySummary_ReturnsLiveCounts(t *testing.T) {
	deps := newTestModelRegistryDeps(t)

	out, err := deps.reg.Get("model_registry_summary").Handler(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("model_registry_summary: %v", err)
	}

	var got struct {
		ResourceCount         int      `json:"resource_count"`
		DeploymentCount       int      `json:"deployment_count"`
		DiscoveredDeployments int      `json:"discovered_deployments"`
		DefaultModel          string   `json:"default_model"`
		ResourceIDs           []string `json:"resource_ids"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if got.ResourceCount != 3 {
		t.Fatalf("resource_count = %d, want 3", got.ResourceCount)
	}
	if got.DeploymentCount != 3 {
		t.Fatalf("deployment_count = %d, want 3", got.DeploymentCount)
	}
	if got.DiscoveredDeployments != 1 {
		t.Fatalf("discovered_deployments = %d, want 1", got.DiscoveredDeployments)
	}
	if got.DefaultModel != "gpt-oss:20b" {
		t.Fatalf("default_model = %q, want gpt-oss:20b", got.DefaultModel)
	}
	if len(got.ResourceIDs) != 3 {
		t.Fatalf("resource_ids = %v, want 3 ids", got.ResourceIDs)
	}
}

func TestModelRegistryList_DeploymentsFilterImages(t *testing.T) {
	deps := newTestModelRegistryDeps(t)

	out, err := deps.reg.Get("model_registry_list").Handler(context.Background(), map[string]any{
		"kind":            "deployments",
		"supports_images": true,
	})
	if err != nil {
		t.Fatalf("model_registry_list: %v", err)
	}

	var got struct {
		Count int `json:"count"`
		Items []struct {
			ID       string `json:"id"`
			Routable bool   `json:"routable"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if got.Count != 2 {
		t.Fatalf("count = %d, want 2 image-capable deployments", got.Count)
	}
	if got.Items[0].ID != "deepslate/google/gemma-3-4b" || got.Items[1].ID != "qwen3-vl:4b" {
		t.Fatalf("image-capable deployments = %+v", got.Items)
	}
	if got.Items[0].Routable {
		t.Fatal("discovered deepslate deployment should start explicit-only")
	}
}

func TestModelRegistryGet_ResourceIncludesDeployments(t *testing.T) {
	deps := newTestModelRegistryDeps(t)

	out, err := deps.reg.Get("model_registry_get").Handler(context.Background(), map[string]any{
		"resource": "deepslate",
	})
	if err != nil {
		t.Fatalf("model_registry_get: %v", err)
	}

	var got struct {
		Kind     string `json:"kind"`
		Resource struct {
			ID              string `json:"id"`
			DeploymentCount int    `json:"deployment_count"`
		} `json:"resource"`
		Deployments []struct {
			ID string `json:"id"`
		} `json:"deployments"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if got.Kind != "resource" {
		t.Fatalf("kind = %q, want resource", got.Kind)
	}
	if got.Resource.ID != "deepslate" || got.Resource.DeploymentCount != 1 {
		t.Fatalf("resource = %+v, want deepslate with one deployment", got.Resource)
	}
	if len(got.Deployments) != 1 || got.Deployments[0].ID != "deepslate/google/gemma-3-4b" {
		t.Fatalf("deployments = %+v, want deepslate/google/gemma-3-4b", got.Deployments)
	}
}

func TestModelResourceSetPolicy_DisablesResourceAndSyncsRouter(t *testing.T) {
	deps := newTestModelRegistryDeps(t)

	out, err := deps.reg.Get("model_resource_set_policy").Handler(context.Background(), map[string]any{
		"resource": "spark",
		"state":    "inactive",
		"reason":   "office hours",
	})
	if err != nil {
		t.Fatalf("model_resource_set_policy: %v", err)
	}

	var got struct {
		Status   string `json:"status"`
		Resource struct {
			ID           string `json:"id"`
			PolicyState  string `json:"policy_state"`
			PolicySource string `json:"policy_source"`
			PolicyReason string `json:"policy_reason"`
		} `json:"resource"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal resource policy response: %v", err)
	}
	if got.Status != "ok" {
		t.Fatalf("status = %q, want ok", got.Status)
	}
	if got.Resource.ID != "spark" || got.Resource.PolicyState != "inactive" {
		t.Fatalf("resource policy = %+v, want spark inactive", got.Resource)
	}
	if got.Resource.PolicySource != "overlay" || got.Resource.PolicyReason != "office hours" {
		t.Fatalf("resource policy metadata = %+v, want overlay/office hours", got.Resource)
	}
	if deps.syncRouterCalls != 1 {
		t.Fatalf("syncRouterCalls = %d, want 1", deps.syncRouterCalls)
	}
	if _, ok := deps.resourcePersisted["spark"]; !ok {
		t.Fatal("resource policy was not persisted")
	}

	modelsAfter := deps.router.GetModels()
	for _, model := range modelsAfter {
		if model.ResourceID == "spark" {
			t.Fatalf("spark model still routable after resource disable: %+v", model)
		}
	}
}

func TestModelDeploymentSetPolicy_PromotesDiscoveredDeployment(t *testing.T) {
	deps := newTestModelRegistryDeps(t)

	out, err := deps.reg.Get("model_deployment_set_policy").Handler(context.Background(), map[string]any{
		"deployment": "deepslate/google/gemma-3-4b",
		"routable":   true,
		"reason":     "promote vision model",
	})
	if err != nil {
		t.Fatalf("model_deployment_set_policy: %v", err)
	}

	var got struct {
		Status     string `json:"status"`
		Deployment struct {
			ID             string `json:"id"`
			Routable       bool   `json:"routable"`
			RoutableSource string `json:"routable_source"`
			PolicyReason   string `json:"policy_reason"`
		} `json:"deployment"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal deployment policy response: %v", err)
	}
	if got.Status != "ok" {
		t.Fatalf("status = %q, want ok", got.Status)
	}
	if got.Deployment.ID != "deepslate/google/gemma-3-4b" || !got.Deployment.Routable {
		t.Fatalf("deployment = %+v, want promoted deepslate deployment", got.Deployment)
	}
	if got.Deployment.RoutableSource != "overlay" || got.Deployment.PolicyReason != "promote vision model" {
		t.Fatalf("deployment metadata = %+v, want overlay/promotion reason", got.Deployment)
	}
	if deps.syncRouterCalls != 1 {
		t.Fatalf("syncRouterCalls = %d, want 1", deps.syncRouterCalls)
	}
	if _, ok := deps.deploymentPersisted["deepslate/google/gemma-3-4b"]; !ok {
		t.Fatal("deployment policy was not persisted")
	}

	found := false
	for _, model := range deps.router.GetModels() {
		if model.Name == "deepslate/google/gemma-3-4b" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("promoted discovered deployment did not become routable")
	}
}

func TestModelRouteExplain_DoesNotMutateAuditLog(t *testing.T) {
	deps := newTestModelRegistryDeps(t)
	before := len(deps.router.GetAuditLog(100))

	out, err := deps.reg.Get("model_route_explain").Handler(context.Background(), map[string]any{
		"query":        "describe this image",
		"needs_images": true,
		"priority":     "interactive",
	})
	if err != nil {
		t.Fatalf("model_route_explain: %v", err)
	}

	var got struct {
		Decision struct {
			ModelSelected  string              `json:"model_selected"`
			RejectedModels map[string][]string `json:"rejected_models"`
		} `json:"decision"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal route explain: %v", err)
	}
	if got.Decision.ModelSelected != "qwen3-vl:4b" {
		t.Fatalf("model_selected = %q, want qwen3-vl:4b", got.Decision.ModelSelected)
	}
	reasons := got.Decision.RejectedModels["gpt-oss:20b"]
	if len(reasons) == 0 || reasons[0] != "missing image support" {
		t.Fatalf("spark rejection = %#v, want missing image support", reasons)
	}

	after := len(deps.router.GetAuditLog(100))
	if after != before {
		t.Fatalf("audit log length changed from %d to %d; explain should not log", before, after)
	}
}
