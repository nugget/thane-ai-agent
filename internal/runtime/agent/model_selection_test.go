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

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	modelproviders "github.com/nugget/thane-ai-agent/internal/model/fleet/providers"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"gopkg.in/yaml.v3"
)

func testModelRegistryFromConfig(t *testing.T, cfg *config.Config) *fleet.Registry {
	t.Helper()

	cat, err := fleet.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("fleet.BuildCatalog: %v", err)
	}
	registry, err := fleet.NewRegistry(cat)
	if err != nil {
		t.Fatalf("fleet.NewRegistry: %v", err)
	}
	return registry
}

func TestRun_ExplicitModelRejectsConfiguredToollessDeployment(t *testing.T) {
	mock := &mockLLM{}
	loop := buildTestLoop(mock, []string{"recall_fact"})
	var cfg config.Config
	raw := `
models:
  default: toolless
  resources:
    default:
      url: http://localhost:11434
      provider: ollama
  available:
    - name: toolless
      resource: default
      supports_tools: false
      context_window: 8192
`
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("yaml.Unmarshal: %v", err)
	}
	loop.UseModelRegistry(testModelRegistryFromConfig(t, &cfg))

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

func TestRun_ExplicitModelPreflightUsesModelSpecificPromptSize(t *testing.T) {
	mock := &mockLLM{}
	loop := buildTestLoop(mock, nil)
	cfg := &config.Config{
		Models: config.ModelsConfig{
			Default: "gemma-local",
			Resources: map[string]config.ModelServerConfig{
				"local": {URL: "http://localhost:11434", Provider: "ollama"},
			},
			Available: []config.ModelConfig{
				{
					Name:     "gemma-local",
					Resource: "local",
				},
			},
		},
	}
	loop.UseModelRegistry(testModelRegistryFromConfig(t, cfg))

	userMessage := "please inspect the loaded memory timeline"
	reqMessages := []Message{{Role: "user", Content: userMessage}}
	defaultPrompt, defaultSections := loop.buildSystemPromptWithProfileSections(context.Background(), userMessage, nil, llm.DefaultModelInteractionProfile())
	defaultSize := estimateLLMMessagesContextTokens(buildInitialLLMMessages(defaultPrompt, defaultSections, nil, reqMessages, "default", time.Time{}))
	modelPrompt, modelSections := loop.buildSystemPromptWithProfileSections(context.Background(), userMessage, nil, loop.modelInteractionProfileForModel("gemma-local"))
	modelSize := estimateLLMMessagesContextTokens(buildInitialLLMMessages(modelPrompt, modelSections, nil, reqMessages, "default", time.Time{}))
	if modelSize <= defaultSize {
		t.Fatalf("model-specific prompt size = %d, want > default size %d", modelSize, defaultSize)
	}
	contextWindow := modelSize - 1
	if contextWindow <= defaultSize {
		t.Fatalf("test setup invalid: context window %d must exceed default size %d", contextWindow, defaultSize)
	}

	cfg.Models.Available[0].ContextWindow = contextWindow
	loop.UseModelRegistry(testModelRegistryFromConfig(t, cfg))

	_, err := loop.Run(context.Background(), &Request{
		Model:    "gemma-local",
		Messages: reqMessages,
	}, nil)

	var incompatible *IncompatibleModelError
	if !errors.As(err, &incompatible) {
		t.Fatalf("Run error = %T, want *IncompatibleModelError", err)
	}
	if !strings.Contains(err.Error(), "context window") {
		t.Fatalf("error = %q, want context-window detail", err)
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

	cat, err := fleet.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("fleet.BuildCatalog: %v", err)
	}
	registry, err := fleet.NewRegistry(cat)
	if err != nil {
		t.Fatalf("fleet.NewRegistry: %v", err)
	}
	if err := registry.ApplyInventory(&fleet.Inventory{
		Resources: []fleet.ResourceInventory{
			{
				ResourceID: "deepslate",
				Provider:   "lmstudio",
				Attempted:  true,
				Models: []fleet.DiscoveredModel{
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

	cat, err := fleet.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("fleet.BuildCatalog: %v", err)
	}
	runtime, err := fleet.NewRuntime(context.Background(), cat, cfg, nil)
	if err != nil {
		t.Fatalf("fleet.NewRuntime: %v", err)
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

func TestRun_ExplicitModelPreparesWhenLMStudioLoadedContextUnknown(t *testing.T) {
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
	loadedContext := 0
	loadCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			mu.Lock()
			current := loadedContext
			mu.Unlock()
			model := map[string]any{
				"key":                "google/gemma-3-4b",
				"type":               "vlm",
				"architecture":       "gemma3",
				"format":             "mlx",
				"max_context_length": 131072,
				"capabilities": map[string]any{
					"vision": true,
				},
				"loaded_instances": []map[string]any{},
			}
			if current > 0 {
				model["loaded_instances"] = []map[string]any{{
					"id": "google/gemma-3-4b:2",
					"config": map[string]any{
						"context_length": current,
					},
				}}
			}
			_ = json.NewEncoder(w).Encode(struct {
				Models []map[string]any `json:"models"`
			}{
				Models: []map[string]any{model},
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
				InstanceID: "google/gemma-3-4b:2",
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

	cat, err := fleet.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("fleet.BuildCatalog: %v", err)
	}
	runtime, err := fleet.NewRuntime(context.Background(), cat, cfg, nil)
	if err != nil {
		t.Fatalf("fleet.NewRuntime: %v", err)
	}
	loop.UseModelRegistry(runtime.Registry())
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
}

func TestRun_ExplicitModelRetriesProviderContextErrorAfterLMStudioLoad(t *testing.T) {
	var mu sync.Mutex
	loadedContext := 24000
	loadCalls := 0
	chatCalls := 0
	lastToolCount := -1

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
					"capabilities": map[string]any{
						"vision":               true,
						"trained_for_tool_use": false,
					},
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
			if req.ContextLength != 131072 {
				t.Fatalf("context_length = %d, want 131072", req.ContextLength)
			}
			mu.Lock()
			loadedContext = req.ContextLength
			loadCalls++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(modelproviders.LMStudioLoadResponse{
				Type:       "llm",
				InstanceID: "google/gemma-3-4b:7",
				Status:     "loaded",
			})
		case "/v1/chat/completions":
			var req struct {
				Model string           `json:"model"`
				Tools []map[string]any `json:"tools"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode chat request: %v", err)
			}
			mu.Lock()
			current := loadedContext
			chatCalls++
			lastToolCount = len(req.Tools)
			mu.Unlock()
			if current < 131072 {
				http.Error(w, `{"error":"The number of tokens to keep from the initial prompt is greater than the context length. Try to load the model with a larger context length, or provide a shorter input"}`, http.StatusBadRequest)
				return
			}
			if req.Model != "google/gemma-3-4b:7" {
				http.Error(w, `{"error":"expected retry to target loaded instance"}`, http.StatusBadRequest)
				return
			}
			if len(req.Tools) > 0 {
				http.Error(w, `{"error":"The number of tokens to keep from the initial prompt is greater than the context length. Try to load the model with a larger context length, or provide a shorter input"}`, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model": "deepslate/google/gemma-3-4b",
				"choices": []map[string]any{{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "ok",
					},
				}},
				"usage": map[string]any{
					"prompt_tokens":     42,
					"completion_tokens": 3,
					"total_tokens":      45,
				},
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
				"deepslate": {URL: srv.URL, Provider: "lmstudio", APIKey: "secret-token"},
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

	cat, err := fleet.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("fleet.BuildCatalog: %v", err)
	}
	runtime, err := fleet.NewRuntime(context.Background(), cat, cfg, nil)
	if err != nil {
		t.Fatalf("fleet.NewRuntime: %v", err)
	}
	loop := buildTestLoopWithLLM(runtime.Client(), []string{"get_state"})
	loop.UseModelRegistry(runtime.Registry())
	loop.UseModelRuntime(runtime)

	resp, err := loop.Run(context.Background(), &Request{
		Model: "deepslate/google/gemma-3-4b",
		Messages: []Message{{
			Role:    "user",
			Content: "Reply with exactly ok after checking the image.",
			Images:  []llm.ImageContent{{Data: validTinyPNGBase64ForAgentTest(), MediaType: "image/png"}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if resp.Model != "deepslate/google/gemma-3-4b" {
		t.Fatalf("resp.Model = %q, want deepslate/google/gemma-3-4b", resp.Model)
	}
	if strings.TrimSpace(resp.Content) != "ok" {
		t.Fatalf("resp.Content = %q, want ok", resp.Content)
	}

	mu.Lock()
	gotLoadCalls := loadCalls
	gotChatCalls := chatCalls
	gotLoadedContext := loadedContext
	gotLastToolCount := lastToolCount
	mu.Unlock()
	if gotLoadCalls != 1 {
		t.Fatalf("load calls = %d, want 1", gotLoadCalls)
	}
	if gotChatCalls != 3 {
		t.Fatalf("chat calls = %d, want 3", gotChatCalls)
	}
	if gotLoadedContext != 131072 {
		t.Fatalf("loadedContext = %d, want 131072", gotLoadedContext)
	}
	if gotLastToolCount != 0 {
		t.Fatalf("last tool count = %d, want 0 on final retry", gotLastToolCount)
	}

	dep, err := runtime.Registry().Catalog().ResolveDeploymentRef("deepslate/google/gemma-3-4b")
	if err != nil {
		t.Fatalf("ResolveDeploymentRef after retry: %v", err)
	}
	if dep.LoadedContextWindow != 131072 || dep.ContextWindow != 131072 {
		t.Fatalf("deployment context after retry = loaded:%d context:%d, want 131072/131072", dep.LoadedContextWindow, dep.ContextWindow)
	}
	if dep.TrainedForToolUse {
		t.Fatal("dep.TrainedForToolUse = true, want false from LM Studio inventory")
	}
}

func TestRun_ExplicitModelRetriesProviderContextErrorAfterRegistryRefresh(t *testing.T) {
	var mu sync.Mutex
	loadedContext := 4096
	chatCalls := 0
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
					"capabilities": map[string]any{
						"vision":               true,
						"trained_for_tool_use": true,
					},
					"loaded_instances": []map[string]any{{
						"id": func() string {
							if current >= 131072 {
								return "google/gemma-3-4b:3"
							}
							return "google/gemma-3-4b"
						}(),
						"config": map[string]any{
							"context_length": current,
						},
					}},
				}},
			})
		case "/api/v1/models/load":
			loadCalls++
			http.Error(w, `{"error":"did not expect explicit load"}`, http.StatusBadRequest)
		case "/v1/chat/completions":
			var req struct {
				Model string `json:"model"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode chat request: %v", err)
			}
			mu.Lock()
			current := loadedContext
			chatCalls++
			if chatCalls == 1 {
				loadedContext = 131072
			}
			mu.Unlock()
			if current < 131072 {
				http.Error(w, `{"error":"The number of tokens to keep from the initial prompt is greater than the context length. Try to load the model with a larger context length, or provide a shorter input"}`, http.StatusBadRequest)
				return
			}
			if req.Model != "google/gemma-3-4b:3" {
				http.Error(w, `{"error":"expected retry to target refreshed loaded instance"}`, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model": "deepslate/google/gemma-3-4b",
				"choices": []map[string]any{{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "ok",
					},
				}},
				"usage": map[string]any{
					"prompt_tokens":     42,
					"completion_tokens": 3,
					"total_tokens":      45,
				},
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
				"deepslate": {URL: srv.URL, Provider: "lmstudio", APIKey: "secret-token"},
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

	cat, err := fleet.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("fleet.BuildCatalog: %v", err)
	}
	runtime, err := fleet.NewRuntime(context.Background(), cat, cfg, nil)
	if err != nil {
		t.Fatalf("fleet.NewRuntime: %v", err)
	}
	loop := buildTestLoopWithLLM(runtime.Client(), []string{"get_state"})
	loop.UseModelRegistry(runtime.Registry())
	loop.UseModelRuntime(runtime)

	resp, err := loop.Run(context.Background(), &Request{
		Model: "deepslate/google/gemma-3-4b",
		Messages: []Message{{
			Role:    "user",
			Content: "Reply with exactly ok after checking the image.",
			Images:  []llm.ImageContent{{Data: validTinyPNGBase64ForAgentTest(), MediaType: "image/png"}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.TrimSpace(resp.Content) != "ok" {
		t.Fatalf("resp.Content = %q, want ok", resp.Content)
	}

	mu.Lock()
	gotChatCalls := chatCalls
	gotLoadCalls := loadCalls
	gotLoadedContext := loadedContext
	mu.Unlock()
	if gotChatCalls != 2 {
		t.Fatalf("chat calls = %d, want 2", gotChatCalls)
	}
	if gotLoadCalls != 0 {
		t.Fatalf("load calls = %d, want 0", gotLoadCalls)
	}
	if gotLoadedContext != 131072 {
		t.Fatalf("loadedContext = %d, want 131072", gotLoadedContext)
	}

	dep, err := runtime.Registry().Catalog().ResolveDeploymentRef("deepslate/google/gemma-3-4b")
	if err != nil {
		t.Fatalf("ResolveDeploymentRef after retry: %v", err)
	}
	if dep.LoadedContextWindow != 131072 || dep.ContextWindow != 131072 {
		t.Fatalf("deployment context after retry = loaded:%d context:%d, want 131072/131072", dep.LoadedContextWindow, dep.ContextWindow)
	}
	if dep.LoadedInstanceID != "google/gemma-3-4b:3" {
		t.Fatalf("LoadedInstanceID = %q, want google/gemma-3-4b:3", dep.LoadedInstanceID)
	}
}

func TestRun_ExplicitModelRetriesWithoutToolsWhenLMStudioAlreadyAtMaxContext(t *testing.T) {
	var mu sync.Mutex
	chatCalls := 0
	lastToolCount := -1

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			_ = json.NewEncoder(w).Encode(struct {
				Models []map[string]any `json:"models"`
			}{
				Models: []map[string]any{{
					"key":                "google/gemma-3-4b",
					"type":               "vlm",
					"architecture":       "gemma3",
					"format":             "mlx",
					"max_context_length": 131072,
					"capabilities": map[string]any{
						"vision":               true,
						"trained_for_tool_use": false,
					},
					"loaded_instances": []map[string]any{{
						"id": "google/gemma-3-4b:7",
						"config": map[string]any{
							"context_length": 131072,
						},
					}},
				}},
			})
		case "/v1/chat/completions":
			var req struct {
				Model string           `json:"model"`
				Tools []map[string]any `json:"tools"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode chat request: %v", err)
			}
			mu.Lock()
			chatCalls++
			lastToolCount = len(req.Tools)
			mu.Unlock()
			if req.Model != "google/gemma-3-4b:7" {
				http.Error(w, `{"error":"expected retry to target loaded instance"}`, http.StatusBadRequest)
				return
			}
			if len(req.Tools) > 0 {
				http.Error(w, `{"error":"The number of tokens to keep from the initial prompt is greater than the context length. Try to load the model with a larger context length, or provide a shorter input"}`, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"model": "deepslate/google/gemma-3-4b",
				"choices": []map[string]any{{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": "ok",
					},
				}},
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
				"deepslate": {URL: srv.URL, Provider: "lmstudio", APIKey: "secret-token"},
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

	cat, err := fleet.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("fleet.BuildCatalog: %v", err)
	}
	runtime, err := fleet.NewRuntime(context.Background(), cat, cfg, nil)
	if err != nil {
		t.Fatalf("fleet.NewRuntime: %v", err)
	}
	loop := buildTestLoopWithLLM(runtime.Client(), []string{"get_state"})
	loop.UseModelRegistry(runtime.Registry())
	loop.UseModelRuntime(runtime)

	resp, err := loop.Run(context.Background(), &Request{
		Model: "deepslate/google/gemma-3-4b",
		Messages: []Message{{
			Role:    "user",
			Content: "Reply with exactly ok after checking the image.",
			Images:  []llm.ImageContent{{Data: validTinyPNGBase64ForAgentTest(), MediaType: "image/png"}},
		}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.TrimSpace(resp.Content) != "ok" {
		t.Fatalf("resp.Content = %q, want ok", resp.Content)
	}

	mu.Lock()
	gotChatCalls := chatCalls
	gotLastToolCount := lastToolCount
	mu.Unlock()
	if gotChatCalls != 2 {
		t.Fatalf("chat calls = %d, want 2", gotChatCalls)
	}
	if gotLastToolCount != 0 {
		t.Fatalf("last tool count = %d, want 0 on final retry", gotLastToolCount)
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
	cat, err := fleet.BuildCatalog(&config.Config{
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
		t.Fatalf("fleet.BuildCatalog: %v", err)
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

	cat, err := fleet.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("fleet.BuildCatalog: %v", err)
	}
	registry, err := fleet.NewRegistry(cat)
	if err != nil {
		t.Fatalf("fleet.NewRegistry: %v", err)
	}
	if err := registry.ApplyInventory(&fleet.Inventory{
		Resources: []fleet.ResourceInventory{
			{
				ResourceID: "deepslate",
				Provider:   "lmstudio",
				Attempted:  true,
				Models: []fleet.DiscoveredModel{
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
