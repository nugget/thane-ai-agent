package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/llm"
	modelproviders "github.com/nugget/thane-ai-agent/internal/models/providers"
)

func TestRuntimePrepareExplicitModel_LoadsLMStudioContext(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	loadedContext := 4096

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/models":
			mu.Lock()
			current := loadedContext
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(struct {
				Data []modelproviders.LMStudioModelInfo `json:"data"`
			}{
				Data: []modelproviders.LMStudioModelInfo{{
					ID:                  "google/gemma-3-4b",
					Type:                "vlm",
					State:               "loaded",
					MaxContextLength:    131072,
					LoadedContextLength: current,
				}},
			})
		case "/api/v1/models/load":
			var req struct {
				Model         string `json:"model"`
				ContextLength int    `json:"context_length"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode load request: %v", err)
			}
			if req.Model != "google/gemma-3-4b" {
				t.Fatalf("load model = %q, want google/gemma-3-4b", req.Model)
			}
			if req.ContextLength != 6144 {
				t.Fatalf("context_length = %d, want 6144", req.ContextLength)
			}
			mu.Lock()
			loadedContext = req.ContextLength
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(modelproviders.LMStudioLoadResponse{
				Type:       "llm",
				InstanceID: req.Model,
				Status:     "loaded",
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	cfg := &config.Config{
		Models: config.ModelsConfig{
			Resources: map[string]config.ModelServerConfig{
				"deepslate": {URL: srv.URL, Provider: "lmstudio"},
			},
		},
	}
	base, err := BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("BuildCatalog: %v", err)
	}
	runtime, err := NewRuntime(context.Background(), base, cfg, nil)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	dep, err := runtime.Registry().Catalog().ResolveDeploymentRef("deepslate/google/gemma-3-4b")
	if err != nil {
		t.Fatalf("ResolveDeploymentRef before prepare: %v", err)
	}
	if dep.LoadedContextWindow != 4096 {
		t.Fatalf("initial LoadedContextWindow = %d, want 4096", dep.LoadedContextWindow)
	}

	changed, err := runtime.PrepareExplicitModel(context.Background(), "deepslate/google/gemma-3-4b", 6144)
	if err != nil {
		t.Fatalf("PrepareExplicitModel: %v", err)
	}
	if !changed {
		t.Fatal("PrepareExplicitModel changed = false, want true")
	}

	dep, err = runtime.Registry().Catalog().ResolveDeploymentRef("deepslate/google/gemma-3-4b")
	if err != nil {
		t.Fatalf("ResolveDeploymentRef after prepare: %v", err)
	}
	if dep.LoadedContextWindow != 6144 {
		t.Fatalf("LoadedContextWindow = %d, want 6144", dep.LoadedContextWindow)
	}
	if dep.ContextWindow != 6144 {
		t.Fatalf("ContextWindow = %d, want 6144", dep.ContextWindow)
	}

	client := runtime.Client()
	if client == nil {
		t.Fatal("runtime.Client() = nil, want non-nil")
	}
	if _, ok := client.(*llm.DynamicClient); !ok {
		t.Fatalf("runtime.Client() = %T, want *llm.DynamicClient", client)
	}
}
