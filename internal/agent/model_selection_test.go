package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/llm"
	"github.com/nugget/thane-ai-agent/internal/models"
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
