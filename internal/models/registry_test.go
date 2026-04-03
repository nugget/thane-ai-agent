package models

import (
	"testing"
	"time"
)

func TestRegistryApplyInventoryBuildsEffectiveSnapshot(t *testing.T) {
	t.Parallel()

	base := &Catalog{
		DefaultModel: "spark/gpt-oss:20b",
		LocalFirst:   true,
		Resources: []Resource{
			{ID: "mirror", Provider: "ollama", URL: "http://mirror.example"},
			{ID: "spark", Provider: "ollama", URL: "http://spark.example"},
		},
		Deployments: []Deployment{
			{
				ID:            "spark/gpt-oss:20b",
				ModelName:     "gpt-oss:20b",
				Provider:      "ollama",
				ResourceID:    "spark",
				Server:        "spark",
				SupportsTools: true,
				ContextWindow: 8192,
				Speed:         6,
				Quality:       6,
				CostTier:      0,
				Source:        DeploymentSourceConfig,
				Routable:      true,
			},
		},
	}
	if err := base.reindex(base.DefaultModel, base.RecoveryModel); err != nil {
		t.Fatalf("reindex base: %v", err)
	}

	reg, err := NewRegistry(base)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	refreshedAt := time.Date(2026, 4, 3, 18, 45, 0, 0, time.UTC)
	err = reg.ApplyInventory(&Inventory{
		Resources: []ResourceInventory{
			{
				ResourceID: "mirror",
				Provider:   "ollama",
				Attempted:  true,
				Models: []DiscoveredModel{
					{Name: "qwen3:8b", Family: "qwen3", Families: []string{"qwen3"}, ParameterSize: "8B", Quantization: "Q4_K_M"},
				},
			},
			{
				ResourceID: "spark",
				Provider:   "ollama",
				Attempted:  true,
				Models: []DiscoveredModel{
					{Name: "gpt-oss:20b"},
					{Name: "qwen3:8b", Family: "qwen3", Families: []string{"qwen3"}, ParameterSize: "8B", Quantization: "Q4_K_M"},
				},
			},
		},
	}, refreshedAt)
	if err != nil {
		t.Fatalf("ApplyInventory: %v", err)
	}

	snap := reg.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot returned nil")
	}
	if snap.Generation != 1 {
		t.Fatalf("generation = %d, want 1", snap.Generation)
	}
	if snap.UpdatedAt != refreshedAt.Format(time.RFC3339) {
		t.Fatalf("updated_at = %q, want %q", snap.UpdatedAt, refreshedAt.Format(time.RFC3339))
	}
	if len(snap.Resources) != 2 {
		t.Fatalf("len(resources) = %d, want 2", len(snap.Resources))
	}
	if snap.Resources[0].ID != "mirror" || snap.Resources[0].DiscoveredModels != 1 {
		t.Fatalf("mirror resource snapshot = %+v, want discovered_models=1", snap.Resources[0])
	}
	if snap.Resources[1].ID != "spark" || snap.Resources[1].DiscoveredModels != 2 {
		t.Fatalf("spark resource snapshot = %+v, want discovered_models=2", snap.Resources[1])
	}

	cat := reg.Catalog()
	if cat == nil {
		t.Fatal("Catalog returned nil")
	}
	if _, err := cat.ResolveModelRef("mirror/qwen3:8b"); err != nil {
		t.Fatalf("ResolveModelRef(mirror/qwen3:8b): %v", err)
	}
	if _, err := cat.ResolveModelRef("spark/qwen3:8b"); err != nil {
		t.Fatalf("ResolveModelRef(spark/qwen3:8b): %v", err)
	}
	if _, err := cat.ResolveModelRef("qwen3:8b"); err == nil {
		t.Fatal("bare discovered duplicate should be ambiguous")
	}

	discovered := 0
	routableDiscovered := 0
	for _, dep := range snap.Deployments {
		if dep.Source == DeploymentSourceDiscovered {
			discovered++
			if dep.Routable {
				routableDiscovered++
			}
		}
	}
	if discovered != 2 {
		t.Fatalf("discovered deployments = %d, want 2", discovered)
	}
	if routableDiscovered != 0 {
		t.Fatalf("routable discovered deployments = %d, want 0", routableDiscovered)
	}
}

func TestRegistryApplyInventoryDoesNotMarkUnattemptedResourcesRefreshed(t *testing.T) {
	t.Parallel()

	base := &Catalog{
		Resources: []Resource{
			{ID: "cloud", Provider: "anthropic", URL: "https://api.anthropic.com"},
		},
	}
	if err := base.reindex(base.DefaultModel, base.RecoveryModel); err != nil {
		t.Fatalf("reindex base: %v", err)
	}

	reg, err := NewRegistry(base)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	refreshedAt := time.Date(2026, 4, 3, 19, 30, 0, 0, time.UTC)
	err = reg.ApplyInventory(&Inventory{
		Resources: []ResourceInventory{
			{
				ResourceID: "cloud",
				Provider:   "anthropic",
			},
		},
	}, refreshedAt)
	if err != nil {
		t.Fatalf("ApplyInventory: %v", err)
	}

	snap := reg.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot returned nil")
	}
	if len(snap.Resources) != 1 {
		t.Fatalf("len(resources) = %d, want 1", len(snap.Resources))
	}
	if snap.Resources[0].LastRefresh != "" {
		t.Fatalf("LastRefresh = %q, want empty for unattempted resource", snap.Resources[0].LastRefresh)
	}
	if snap.Resources[0].DiscoveredModels != 0 {
		t.Fatalf("DiscoveredModels = %d, want 0", snap.Resources[0].DiscoveredModels)
	}
}
