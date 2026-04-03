package models

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/router"
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
	cfg.Models.Resources = map[string]config.ModelServerConfig{
		"default": {URL: "http://localhost:11434", Provider: "ollama"},
		"edge":    {URL: "http://edge:11434", Provider: "ollama"},
	}
	cfg.Models.Available = []config.ModelConfig{
		{
			Name:          "qwen3:4b",
			Resource:      "default",
			SupportsTools: true,
			ContextWindow: 32768,
		},
		{
			Name:          "qwen3:4b",
			Resource:      "edge",
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
	cfg.Models.Resources = map[string]config.ModelServerConfig{
		"default": {URL: "http://localhost:11434", Provider: "ollama"},
		"edge":    {URL: "http://edge:11434", Provider: "ollama"},
	}
	cfg.Models.Available = []config.ModelConfig{
		{Name: "qwen3:4b", Resource: "default", ContextWindow: 32768},
		{Name: "qwen3:4b", Resource: "edge", ContextWindow: 65536},
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

func TestMergeInventory_AddsDiscoveredDeploymentsAsNonRoutable(t *testing.T) {
	cfg := &config.Config{}
	cfg.Models.OllamaURL = "http://localhost:11434"
	cfg.Models.Default = "qwen3:4b"
	cfg.Models.Available = []config.ModelConfig{
		{Name: "qwen3:4b", SupportsTools: true, ContextWindow: 32768, Speed: 7, Quality: 6, CostTier: 0},
	}

	base, err := BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("BuildCatalog() error = %v", err)
	}

	merged, err := MergeInventory(base, &Inventory{
		Resources: []ResourceInventory{
			{
				ResourceID: "default",
				Provider:   "ollama",
				Attempted:  true,
				Models: []DiscoveredModel{
					{Name: "qwen3:4b"},
					{Name: "gpt-oss:20b", Family: "gpt-oss", Families: []string{"gpt-oss"}, ParameterSize: "20B", Quantization: "Q4_K_M"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("MergeInventory() error = %v", err)
	}

	id, err := merged.ResolveModelRef("gpt-oss:20b")
	if err != nil {
		t.Fatalf("ResolveModelRef(gpt-oss:20b) error = %v", err)
	}
	if id != "default/gpt-oss:20b" {
		t.Fatalf("ResolveModelRef(gpt-oss:20b) = %q, want %q", id, "default/gpt-oss:20b")
	}

	dep, ok := merged.byID["default/gpt-oss:20b"]
	if !ok {
		t.Fatal("merged catalog missing discovered deployment")
	}
	if dep.Source != DeploymentSourceDiscovered {
		t.Fatalf("Source = %q, want %q", dep.Source, DeploymentSourceDiscovered)
	}
	if dep.Routable {
		t.Fatal("Routable = true, want false for discovered deployment")
	}
	if !dep.SupportsTools || !dep.ProviderSupportsTools || !dep.SupportsStreaming {
		t.Fatalf("discovered deployment capabilities = %+v, want tools/provider_tools/streaming", dep)
	}
	if dep.SupportsImages {
		t.Fatalf("discovered deployment capabilities = %+v, want image support=false for gpt-oss", dep)
	}
	if dep.Family != "gpt-oss" {
		t.Fatalf("Family = %q, want %q", dep.Family, "gpt-oss")
	}

	routerCfg := merged.RouterConfig(100)
	found := false
	for _, model := range routerCfg.Models {
		if model.Name == "default/gpt-oss:20b" {
			t.Fatal("discovered deployment should not be included in router config yet")
		}
		if model.Name == "qwen3:4b" {
			found = true
			if !model.SupportsStreaming || !model.ProviderSupportsTools {
				t.Fatalf("router model capabilities = %+v, want provider-backed streaming/tools", model)
			}
			if model.SupportsImages {
				t.Fatalf("router model capabilities = %+v, want image support=false for qwen3", model)
			}
		}
	}
	if !found {
		t.Fatal("router config missing configured qwen3:4b deployment")
	}
}

func TestMergeInventory_PreservesStableConfiguredIDWhenDuplicateAppears(t *testing.T) {
	cfg := &config.Config{}
	cfg.Models.Resources = map[string]config.ModelServerConfig{
		"default": {URL: "http://localhost:11434", Provider: "ollama"},
		"edge":    {URL: "http://edge:11434", Provider: "ollama"},
	}
	cfg.Models.Default = "qwen3:4b"
	cfg.Models.Available = []config.ModelConfig{
		{Name: "qwen3:4b", Resource: "default", SupportsTools: true, ContextWindow: 32768, Speed: 7, Quality: 6, CostTier: 0},
	}

	base, err := BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("BuildCatalog() error = %v", err)
	}
	if base.DefaultModel != "qwen3:4b" {
		t.Fatalf("base.DefaultModel = %q, want %q", base.DefaultModel, "qwen3:4b")
	}

	merged, err := MergeInventory(base, &Inventory{
		Resources: []ResourceInventory{
			{
				ResourceID: "edge",
				Provider:   "ollama",
				Attempted:  true,
				Models:     []DiscoveredModel{{Name: "qwen3:4b"}},
			},
		},
	})
	if err != nil {
		t.Fatalf("MergeInventory() error = %v", err)
	}

	if merged.DefaultModel != "qwen3:4b" {
		t.Fatalf("merged.DefaultModel = %q, want %q", merged.DefaultModel, "qwen3:4b")
	}
	id, err := merged.ResolveModelRef("qwen3:4b")
	if err != nil {
		t.Fatalf("ResolveModelRef(qwen3:4b) error = %v", err)
	}
	if id != "qwen3:4b" {
		t.Fatalf("ResolveModelRef(qwen3:4b) = %q, want %q", id, "qwen3:4b")
	}
	if _, ok := merged.byID["edge/qwen3:4b"]; !ok {
		t.Fatal("merged catalog missing discovered qualified deployment")
	}
}

func TestBuildCatalog_SingleLMStudioResourceCanBeInferred(t *testing.T) {
	cfg := &config.Config{}
	cfg.Models.Resources = map[string]config.ModelServerConfig{
		"deepslate": {URL: "http://deepslate:1234", Provider: "lmstudio", APIKey: "secret-token"},
	}
	cfg.Models.Default = "qwen3:8b"
	cfg.Models.Available = []config.ModelConfig{
		{Name: "qwen3:8b", Provider: "lmstudio", SupportsTools: true, ContextWindow: 32768, Speed: 8, Quality: 6, CostTier: 0},
	}

	cat, err := BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("BuildCatalog() error = %v", err)
	}

	if got := cat.DefaultModel; got != "qwen3:8b" {
		t.Fatalf("DefaultModel = %q, want %q", got, "qwen3:8b")
	}
	dep, ok := cat.byID["qwen3:8b"]
	if !ok {
		t.Fatal("catalog missing inferred LM Studio deployment")
	}
	if dep.Provider != "lmstudio" {
		t.Fatalf("Provider = %q, want %q", dep.Provider, "lmstudio")
	}
	if dep.ResourceID != "deepslate" {
		t.Fatalf("ResourceID = %q, want %q", dep.ResourceID, "deepslate")
	}
	if !dep.ProviderSupportsTools || !dep.SupportsStreaming {
		t.Fatalf("LM Studio deployment capabilities = %+v, want provider tools/streaming", dep)
	}
	if dep.SupportsImages {
		t.Fatalf("LM Studio deployment capabilities = %+v, want image support=false for qwen3", dep)
	}
}

func TestCatalogRouterConfig_ExcludesInactiveDeploymentsAndRetargetsDefault(t *testing.T) {
	cat := &Catalog{
		DefaultModel:  "spark/gpt-oss:20b",
		RecoveryModel: "mirror/gpt-oss:20b",
		LocalFirst:    true,
		Deployments: []Deployment{
			{
				ID:            "spark/gpt-oss:20b",
				ModelName:     "gpt-oss:20b",
				Provider:      "ollama",
				ResourceID:    "spark",
				Routable:      true,
				SupportsTools: true,
				ContextWindow: 8192,
				Speed:         6,
				Quality:       6,
				PolicyState:   DeploymentPolicyStateInactive,
			},
			{
				ID:            "mirror/gpt-oss:20b",
				ModelName:     "gpt-oss:20b",
				Provider:      "ollama",
				ResourceID:    "mirror",
				Routable:      true,
				SupportsTools: true,
				ContextWindow: 8192,
				Speed:         6,
				Quality:       6,
				PolicyState:   DeploymentPolicyStateActive,
			},
			{
				ID:            "spark/qwen3:8b",
				ModelName:     "qwen3:8b",
				Provider:      "ollama",
				ResourceID:    "spark",
				Routable:      true,
				SupportsTools: true,
				ContextWindow: 32768,
				Speed:         8,
				Quality:       5,
				PolicyState:   DeploymentPolicyStateFlagged,
			},
		},
	}

	cfg := cat.RouterConfig(100)

	if cfg.DefaultModel != "mirror/gpt-oss:20b" {
		t.Fatalf("DefaultModel = %q, want %q", cfg.DefaultModel, "mirror/gpt-oss:20b")
	}
	if len(cfg.Models) != 2 {
		t.Fatalf("len(Models) = %d, want 2", len(cfg.Models))
	}
	for _, model := range cfg.Models {
		if model.Name == "spark/gpt-oss:20b" {
			t.Fatal("inactive deployment should not be included in automatic router config")
		}
	}
	if cfg.Models[0].Name != "mirror/gpt-oss:20b" && cfg.Models[1].Name != "mirror/gpt-oss:20b" {
		t.Fatal("active recovery deployment missing from automatic router config")
	}
	if cfg.Models[0].Name != "spark/qwen3:8b" && cfg.Models[1].Name != "spark/qwen3:8b" {
		t.Fatal("flagged deployment should remain available for automatic routing in this slice")
	}

	rtr := router.NewRouter(slog.Default(), cfg)
	got, _ := rtr.Route(t.Context(), router.Request{
		Query:      "search for the latest battery event",
		NeedsTools: true,
		ToolCount:  2,
		Priority:   router.PriorityBackground,
	})
	if got == "spark/gpt-oss:20b" {
		t.Fatal("router selected inactive deployment")
	}
}
