package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/models"
	modelproviders "github.com/nugget/thane-ai-agent/internal/models/providers"
	"github.com/nugget/thane-ai-agent/internal/router"
)

func testModelRegistryFromConfig(t *testing.T, cfg *config.Config) *models.Registry {
	t.Helper()

	cat, err := models.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("models.BuildCatalog: %v", err)
	}
	registry, err := models.NewRegistry(cat)
	if err != nil {
		t.Fatalf("models.NewRegistry: %v", err)
	}
	return registry
}

func TestRun_ExplicitModelRejectsConfiguredToollessDeployment(t *testing.T) {
	mock := &mockLLM{}
	loop := buildTestLoop(mock, []string{"recall_fact"})
	loop.UseModelRegistry(testModelRegistryFromConfig(t, &config.Config{
		Models: config.ModelsConfig{
			Default: "toolless",
			Resources: map[string]config.ModelServerConfig{
				"default": {URL: "http://localhost:11434", Provider: "ollama"},
			},
			Available: []config.ModelConfig{
				{
					Name:          "toolless",
					Resource:      "default",
					SupportsTools: false,
					ContextWindow: 8192,
				},
			},
		},
	}))

	_, err := loop.Run(context.Background(), &Request{
		Model:    "toolless",
		Messages: []Message{{Role: "user", Content: "explain why this model should be used"}},
	}, nil)

	var incompatible *IncompatibleModelError
	if !errors.As(err, &incompatible) {
		t.Fatalf("Run error = %T, want *IncompatibleModelError", err)
	}
	if !strings.Contains(err.Error(), "configured without tool support") {
		t.Fatalf("error = %q, want configured-without-tool-support detail", err)
	}
	if len(mock.calls) != 0 {
		t.Fatalf("llm calls = %d, want 0 when preflight rejects", len(mock.calls))
	}
}

func TestRun_ExplicitModelRejectsStreamingIncompatibleDeployment(t *testing.T) {
	mock := &mockLLM{}
	loop := buildTestLoop(mock, nil)
	loop.UseModelRegistry(testModelRegistryFromConfig(t, &config.Config{
		Models: config.ModelsConfig{
			Default: "nostream",
			Resources: map[string]config.ModelServerConfig{
				"edge": {URL: "http://edge.example", Provider: "custom"},
			},
			Available: []config.ModelConfig{
				{
					Name:          "nostream",
					Resource:      "edge",
					SupportsTools: true,
					ContextWindow: 8192,
				},
			},
		},
	}))

	_, err := loop.Run(context.Background(), &Request{
		Model:    "nostream",
		Messages: []Message{{Role: "user", Content: "summarize the streaming compatibility"}},
	}, func(StreamEvent) {})

	var incompatible *IncompatibleModelError
	if !errors.As(err, &incompatible) {
		t.Fatalf("Run error = %T, want *IncompatibleModelError", err)
	}
	if !strings.Contains(err.Error(), "does not support streaming responses") {
		t.Fatalf("error = %q, want streaming detail", err)
	}
	if len(mock.calls) != 0 {
		t.Fatalf("llm calls = %d, want 0 when preflight rejects", len(mock.calls))
	}
}

func TestRun_ExplicitModelRejectsAmbiguousBareReference(t *testing.T) {
	mock := &mockLLM{}
	loop := buildTestLoop(mock, nil)
	loop.UseModelRegistry(testModelRegistryFromConfig(t, &config.Config{
		Models: config.ModelsConfig{
			Default: "spark/qwen3:4b",
			Resources: map[string]config.ModelServerConfig{
				"mirror": {URL: "http://mirror.example", Provider: "ollama"},
				"spark":  {URL: "http://spark.example", Provider: "ollama"},
			},
			Available: []config.ModelConfig{
				{Name: "qwen3:4b", Resource: "mirror", SupportsTools: true, ContextWindow: 8192},
				{Name: "qwen3:4b", Resource: "spark", SupportsTools: true, ContextWindow: 8192},
			},
		},
	}))

	_, err := loop.Run(context.Background(), &Request{
		Model:    "qwen3:4b",
		Messages: []Message{{Role: "user", Content: "compare the duplicate deployments"}},
	}, nil)

	var ambiguous *llm.AmbiguousModelError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("Run error = %T, want *llm.AmbiguousModelError", err)
	}
	if len(ambiguous.Targets) != 2 {
		t.Fatalf("ambiguous targets = %v, want 2 qualified targets", ambiguous.Targets)
	}
	if len(mock.calls) != 0 {
		t.Fatalf("llm calls = %d, want 0 when preflight rejects", len(mock.calls))
	}
}

func TestRun_ExplicitModelRejectsImageIncompatibleDeployment(t *testing.T) {
	mock := &mockLLM{}
	loop := buildTestLoop(mock, nil)
	loop.UseModelRegistry(testModelRegistryFromConfig(t, &config.Config{
		Models: config.ModelsConfig{
			Default: "noimage",
			Resources: map[string]config.ModelServerConfig{
				"edge": {URL: "http://edge.example", Provider: "custom"},
			},
			Available: []config.ModelConfig{
				{
					Name:          "noimage",
					Resource:      "edge",
					SupportsTools: true,
					ContextWindow: 8192,
				},
			},
		},
	}))

	_, err := loop.Run(context.Background(), &Request{
		Model: "noimage",
		Messages: []Message{{
			Role:    "user",
			Content: "describe the image",
			Images:  []llm.ImageContent{{Data: "Zm9v", MediaType: "image/png"}},
		}},
	}, nil)

	var incompatible *IncompatibleModelError
	if !errors.As(err, &incompatible) {
		t.Fatalf("Run error = %T, want *IncompatibleModelError", err)
	}
	if !strings.Contains(err.Error(), "does not support image inputs") {
		t.Fatalf("error = %q, want image-input detail", err)
	}
	if len(mock.calls) != 0 {
		t.Fatalf("llm calls = %d, want 0 when preflight rejects", len(mock.calls))
	}
}

func TestRun_ExplicitModelRejectsContextOverflow(t *testing.T) {
	mock := &mockLLM{}
	loop := buildTestLoop(mock, nil)
	loop.UseModelRegistry(testModelRegistryFromConfig(t, &config.Config{
		Models: config.ModelsConfig{
			Default: "tiny-context",
			Resources: map[string]config.ModelServerConfig{
				"edge": {URL: "http://edge.example", Provider: "lmstudio"},
			},
			Available: []config.ModelConfig{
				{
					Name:          "tiny-context",
					Resource:      "edge",
					SupportsTools: true,
					ContextWindow: 1024,
				},
			},
		},
	}))

	_, err := loop.Run(context.Background(), &Request{
		Model: "tiny-context",
		Messages: []Message{{
			Role:    "user",
			Content: strings.Repeat("context ", 800),
			Images:  []llm.ImageContent{{Data: "Zm9v", MediaType: "image/png"}},
		}},
	}, nil)

	var incompatible *IncompatibleModelError
	if !errors.As(err, &incompatible) {
		t.Fatalf("Run error = %T, want *IncompatibleModelError", err)
	}
	if !strings.Contains(err.Error(), "context window is too small") {
		t.Fatalf("error = %q, want context-window detail", err)
	}
	if len(mock.calls) != 0 {
		t.Fatalf("llm calls = %d, want 0 when preflight rejects", len(mock.calls))
	}
}

func TestRun_ExplicitModelRejectsLoadedContextOverflowWithMaxHint(t *testing.T) {
	mock := &mockLLM{}
	loop := buildTestLoop(mock, nil)

	cfg := &config.Config{
		Models: config.ModelsConfig{
			Default: "gpt-oss:20b",
			Resources: map[string]config.ModelServerConfig{
				"spark":     {URL: "http://spark.example", Provider: "ollama"},
				"deepslate": {URL: "http://deepslate.example", Provider: "lmstudio"},
			},
			Available: []config.ModelConfig{
				{
					Name:          "gpt-oss:20b",
					Resource:      "spark",
					SupportsTools: true,
					ContextWindow: 8192,
				},
			},
		},
	}

	cat, err := models.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("models.BuildCatalog: %v", err)
	}
	registry, err := models.NewRegistry(cat)
	if err != nil {
		t.Fatalf("models.NewRegistry: %v", err)
	}
	if err := registry.ApplyInventory(&models.Inventory{
		Resources: []models.ResourceInventory{
			{
				ResourceID: "deepslate",
				Provider:   "lmstudio",
				Attempted:  true,
				Models: []models.DiscoveredModel{
					{
						Name:                "google/gemma-3-4b",
						SupportsChat:        true,
						ModelType:           "vlm",
						SupportsTools:       true,
						SupportsStreaming:   true,
						SupportsImages:      true,
						ContextWindow:       4096,
						MaxContextWindow:    131072,
						LoadedContextWindow: 4096,
					},
				},
			},
		},
	}, time.Date(2026, 4, 3, 18, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("registry.ApplyInventory: %v", err)
	}
	loop.UseModelRegistry(registry)

	_, err = loop.Run(context.Background(), &Request{
		Model: "deepslate/google/gemma-3-4b",
		Messages: []Message{{
			Role:    "user",
			Content: strings.Repeat("context ", 2200),
			Images:  []llm.ImageContent{{Data: validTinyPNGBase64ForAgentTest(), MediaType: "image/png"}},
		}},
	}, nil)

	var incompatible *IncompatibleModelError
	if !errors.As(err, &incompatible) {
		t.Fatalf("Run error = %T, want *IncompatibleModelError", err)
	}
	if !strings.Contains(err.Error(), "currently loaded context window") {
		t.Fatalf("error = %q, want loaded-context detail", err)
	}
	if !strings.Contains(err.Error(), "runner advertises max 131072") {
		t.Fatalf("error = %q, want max-context hint", err)
	}
	if len(mock.calls) != 0 {
		t.Fatalf("llm calls = %d, want 0 when preflight rejects", len(mock.calls))
	}
}

func TestRun_ExplicitModelPreparesLoadedContextAndRetries(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{{
			Model: "deepslate/google/gemma-3-4b",
			Message: llm.Message{
				Role:    "assistant",
				Content: "ok",
			},
			InputTokens:  42,
			OutputTokens: 3,
		}},
	}
	loop := buildTestLoop(mock, nil)
	var mu sync.Mutex
	loadedContext := 4096
	loadCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			mu.Lock()
			current := loadedContext
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(struct {
				Models []map[string]any `json:"models"`
			}{
				Models: []map[string]any{{
					"key":                "google/gemma-3-4b",
					"type":               "vlm",
					"architecture":       "gemma3",
					"format":             "mlx",
					"max_context_length": 131072,
					"capabilities":       map[string]any{"vision": true},
					"loaded_instances": []map[string]any{{
						"id": "google/gemma-3-4b",
						"config": map[string]any{
							"context_length": current,
						},
					}},
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
			if req.ContextLength <= 4096 {
				t.Fatalf("context_length = %d, want > 4096", req.ContextLength)
			}
			mu.Lock()
			loadedContext = req.ContextLength
			loadCalls++
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
			Default: "gpt-oss:20b",
			Resources: map[string]config.ModelServerConfig{
				"spark":     {URL: "http://spark.example", Provider: "ollama"},
				"deepslate": {URL: srv.URL, Provider: "lmstudio"},
			},
			Available: []config.ModelConfig{
				{
					Name:          "gpt-oss:20b",
					Resource:      "spark",
					SupportsTools: true,
					ContextWindow: 8192,
				},
			},
		},
	}

	cat, err := models.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("models.BuildCatalog: %v", err)
	}
	runtime, err := models.NewRuntime(context.Background(), cat, cfg, nil)
	if err != nil {
		t.Fatalf("models.NewRuntime: %v", err)
	}
	registry := runtime.Registry()
	loop.UseModelRegistry(registry)
	loop.UseModelRuntime(runtime)

	resp, err := loop.Run(context.Background(), &Request{
		Model: "deepslate/google/gemma-3-4b",
		Messages: []Message{{
			Role:    "user",
			Content: strings.Repeat("context ", 1500),
			Images:  []llm.ImageContent{{Data: validTinyPNGBase64ForAgentTest(), MediaType: "image/png"}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if resp.Model != "deepslate/google/gemma-3-4b" {
		t.Fatalf("resp.Model = %q, want deepslate/google/gemma-3-4b", resp.Model)
	}
	mu.Lock()
	gotLoadCalls := loadCalls
	gotLoadedContext := loadedContext
	mu.Unlock()
	if gotLoadCalls != 1 {
		t.Fatalf("load calls = %d, want 1", gotLoadCalls)
	}
	if gotLoadedContext <= 4096 {
		t.Fatalf("loadedContext = %d, want > 4096 after prepare", gotLoadedContext)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("llm calls = %d, want 1", len(mock.calls))
	}
	if mock.calls[0].Model != "deepslate/google/gemma-3-4b" {
		t.Fatalf("llm call model = %q, want deepslate/google/gemma-3-4b", mock.calls[0].Model)
	}
}

func TestEstimateRequestContextTokens_IncludesImages(t *testing.T) {
	got := estimateRequestContextTokens("abcd", []Message{{
		Role:    "user",
		Content: "abcdefgh",
		Images: []llm.ImageContent{
			{Data: validTinyPNGBase64ForAgentTest(), MediaType: "image/png"},
			{Data: validTinyPNGBase64ForAgentTest(), MediaType: "image/png"},
		},
	}})

	want := 1 + 2 + (2 * estimatedImageContextTokens)
	if got != want {
		t.Fatalf("estimateRequestContextTokens() = %d, want %d", got, want)
	}
}

func TestNoEligibleImageRoutingError_IncludesContextHint(t *testing.T) {
	cat, err := models.BuildCatalog(&config.Config{
		Models: config.ModelsConfig{
			Default: "qwen3-vl:4b",
			Resources: map[string]config.ModelServerConfig{
				"vision": {URL: "http://vision.example", Provider: "ollama"},
			},
			Available: []config.ModelConfig{
				{
					Name:          "qwen3-vl:4b",
					Resource:      "vision",
					SupportsTools: true,
					ContextWindow: 4096,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("models.BuildCatalog: %v", err)
	}
	cat.Deployments[0].ContextWindow = 4096
	cat.Deployments[0].LoadedContextWindow = 4096
	cat.Deployments[0].MaxContextWindow = 131072

	err = noEligibleImageRoutingError(cat, &router.Decision{
		RejectedModels: map[string][]string{
			"qwen3-vl:4b": {"context window too small"},
		},
	})

	var noEligible *NoEligibleModelError
	if !errors.As(err, &noEligible) {
		t.Fatalf("error = %T, want *NoEligibleModelError", err)
	}
	if !strings.Contains(err.Error(), "too small for the current prompt") {
		t.Fatalf("error = %q, want context-fit hint", err)
	}
	if !strings.Contains(err.Error(), "larger max window than is currently loaded") {
		t.Fatalf("error = %q, want loaded-vs-max hint", err)
	}
}

func validTinyPNGBase64ForAgentTest() string {
	return "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aW3cAAAAASUVORK5CYII="
}

func TestRun_RoutedImageRequestRejectsWhenNoEligibleImageCapableDeploymentExists(t *testing.T) {
	mock := &mockLLM{}
	loop := buildTestLoop(mock, nil)

	cfg := &config.Config{
		Models: config.ModelsConfig{
			Default: "gpt-oss:20b",
			Resources: map[string]config.ModelServerConfig{
				"spark":     {URL: "http://spark.example", Provider: "ollama"},
				"deepslate": {URL: "http://deepslate.example", Provider: "lmstudio"},
			},
			Available: []config.ModelConfig{
				{
					Name:          "gpt-oss:20b",
					Resource:      "spark",
					SupportsTools: true,
					ContextWindow: 8192,
				},
			},
		},
	}

	cat, err := models.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("models.BuildCatalog: %v", err)
	}
	registry, err := models.NewRegistry(cat)
	if err != nil {
		t.Fatalf("models.NewRegistry: %v", err)
	}
	if err := registry.ApplyInventory(&models.Inventory{
		Resources: []models.ResourceInventory{
			{
				ResourceID: "deepslate",
				Provider:   "lmstudio",
				Attempted:  true,
				Models: []models.DiscoveredModel{
					{
						Name:              "google/gemma-3-4b",
						SupportsTools:     true,
						SupportsStreaming: true,
						SupportsImages:    true,
					},
				},
			},
		},
	}, time.Date(2026, 4, 3, 18, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("registry.ApplyInventory: %v", err)
	}

	loop.UseModelRegistry(registry)
	loop.router = router.NewRouter(slog.Default(), registry.Catalog().RouterConfig(32))

	_, err = loop.Run(context.Background(), &Request{
		Messages: []Message{{
			Role:    "user",
			Content: "describe the image",
			Images:  []llm.ImageContent{{Data: "Zm9v", MediaType: "image/png"}},
		}},
	}, nil)

	var noEligible *NoEligibleModelError
	if !errors.As(err, &noEligible) {
		t.Fatalf("Run error = %T, want *NoEligibleModelError", err)
	}
	if !strings.Contains(err.Error(), "image inputs") {
		t.Fatalf("error = %q, want image-input detail", err)
	}
	if !strings.Contains(err.Error(), "deepslate/google/gemma-3-4b") {
		t.Fatalf("error = %q, want explicit deployment suggestion", err)
	}
	if len(mock.calls) != 0 {
		t.Fatalf("llm calls = %d, want 0 when routing rejects", len(mock.calls))
	}
}
