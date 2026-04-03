package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/models"
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

	err = noEligibleImageRoutingError(cat, &router.Decision{
		RejectedModels: map[string][]string{
			"vision/qwen3-vl:4b": {"context window too small"},
		},
	})

	var noEligible *NoEligibleModelError
	if !errors.As(err, &noEligible) {
		t.Fatalf("error = %T, want *NoEligibleModelError", err)
	}
	if !strings.Contains(err.Error(), "too small for the current prompt") {
		t.Fatalf("error = %q, want context-fit hint", err)
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
