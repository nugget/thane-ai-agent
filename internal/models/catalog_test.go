package models

import (
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/config"
)

func TestBuildCatalog_LegacyOllamaURLCreatesDefaultResource(t *testing.T) {
	cfg := &config.Config{}
	cfg.Models.OllamaURL = "http://localhost:11434"
	cfg.Models.Default = "qwen3:8b"
	cfg.Models.RecoveryModel = "qwen3:4b"
	cfg.Models.Available = []config.ModelConfig{
		{
			Name:          "qwen3:8b",
			SupportsTools: true,
			ContextWindow: 65536,
		},
		{
			Name:          "qwen3:4b",
			SupportsTools: true,
			ContextWindow: 32768,
		},
	}

	cat, err := BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("BuildCatalog() error = %v", err)
	}

	if got := len(cat.Resources); got != 1 {
		t.Fatalf("len(Resources) = %d, want 1", got)
	}
	if cat.Resources[0].ID != "default" {
		t.Fatalf("Resources[0].ID = %q, want %q", cat.Resources[0].ID, "default")
	}
	if cat.Resources[0].URL != "http://localhost:11434" {
		t.Fatalf("Resources[0].URL = %q, want localhost ollama url", cat.Resources[0].URL)
	}
	if cat.DefaultModel != "qwen3:8b" {
		t.Fatalf("DefaultModel = %q, want %q", cat.DefaultModel, "qwen3:8b")
	}
	if cat.RecoveryModel != "qwen3:4b" {
		t.Fatalf("RecoveryModel = %q, want %q", cat.RecoveryModel, "qwen3:4b")
	}
	if got := cat.PrimaryOllamaURL(); got != "http://localhost:11434" {
		t.Fatalf("PrimaryOllamaURL() = %q, want localhost ollama url", got)
	}
}

func TestBuildCatalog_AmbiguousModelRequiresQualifiedReference(t *testing.T) {
	cfg := &config.Config{}
	cfg.Models.Servers = map[string]config.ModelServerConfig{
		"default": {URL: "http://localhost:11434", Provider: "ollama"},
		"edge":    {URL: "http://edge:11434", Provider: "ollama"},
	}
	cfg.Models.Available = []config.ModelConfig{
		{
			Name:          "qwen3:4b",
			Server:        "default",
			SupportsTools: true,
			ContextWindow: 32768,
		},
		{
			Name:          "qwen3:4b",
			Server:        "edge",
			SupportsTools: true,
			ContextWindow: 65536,
		},
	}

	cat, err := BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("BuildCatalog() error = %v", err)
	}

	if _, err := cat.ResolveModelRef("qwen3:4b"); err == nil {
		t.Fatal("ResolveModelRef(qwen3:4b) should fail for ambiguous shorthand")
	} else {
		msg := err.Error()
		if !strings.Contains(msg, "default/qwen3:4b") || !strings.Contains(msg, "edge/qwen3:4b") {
			t.Fatalf("ResolveModelRef(qwen3:4b) error = %q, want both qualified ids", msg)
		}
	}

	if got, err := cat.ResolveModelRef("edge/qwen3:4b"); err != nil {
		t.Fatalf("ResolveModelRef(edge/qwen3:4b) error = %v", err)
	} else if got != "edge/qwen3:4b" {
		t.Fatalf("ResolveModelRef(edge/qwen3:4b) = %q, want %q", got, "edge/qwen3:4b")
	}
}

func TestCatalogContextWindowForModel_UsesLargestMatchingDeployment(t *testing.T) {
	cfg := &config.Config{}
	cfg.Models.Servers = map[string]config.ModelServerConfig{
		"default": {URL: "http://localhost:11434", Provider: "ollama"},
		"edge":    {URL: "http://edge:11434", Provider: "ollama"},
	}
	cfg.Models.Available = []config.ModelConfig{
		{Name: "qwen3:4b", Server: "default", ContextWindow: 32768},
		{Name: "qwen3:4b", Server: "edge", ContextWindow: 65536},
	}

	cat, err := BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("BuildCatalog() error = %v", err)
	}

	if got := cat.ContextWindowForModel("qwen3:4b", 4096); got != 65536 {
		t.Fatalf("ContextWindowForModel(qwen3:4b) = %d, want %d", got, 65536)
	}
	if got := cat.ContextWindowForModel("unknown", 4096); got != 4096 {
		t.Fatalf("ContextWindowForModel(unknown) = %d, want fallback %d", got, 4096)
	}
}
