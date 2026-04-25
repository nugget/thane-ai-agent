package app

import (
	"log/slog"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/models"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

func testModelPolicyRegistry(t *testing.T) *models.Registry {
	t.Helper()

	cfg := &config.Config{}
	cfg.Models.LocalFirst = true
	cfg.Models.Default = "gpt-oss:20b"
	cfg.Models.Resources = map[string]config.ModelServerConfig{
		"deepslate": {URL: "http://deepslate.example", Provider: "lmstudio"},
		"spark":     {URL: "http://spark.example", Provider: "ollama"},
	}
	cfg.Models.Available = []config.ModelConfig{
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

func TestModelPolicyStoreSaveAndLoadIntoRegistry(t *testing.T) {
	t.Parallel()

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	op, err := opstate.NewStore(db)
	if err != nil {
		t.Fatalf("opstate.NewStore: %v", err)
	}
	store := newModelPolicyStore(op)
	registry := testModelPolicyRegistry(t)

	routable := true
	updatedAt := time.Date(2026, 4, 4, 2, 0, 0, 0, time.UTC)
	if err := store.Save("deepslate/google/gemma-3-4b", models.DeploymentPolicy{
		State:     models.DeploymentPolicyStateFlagged,
		Routable:  &routable,
		Reason:    "night-only route",
		UpdatedAt: updatedAt,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := store.LoadInto(registry, slog.Default()); err != nil {
		t.Fatalf("LoadInto: %v", err)
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
	}, time.Date(2026, 4, 4, 2, 5, 0, 0, time.UTC)); err != nil {
		t.Fatalf("ApplyInventory: %v", err)
	}

	snap := registry.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot returned nil")
	}
	found := false
	for _, dep := range snap.Deployments {
		if dep.ID != "deepslate/google/gemma-3-4b" {
			continue
		}
		found = true
		if dep.PolicyState != models.DeploymentPolicyStateFlagged {
			t.Fatalf("PolicyState = %q, want %q", dep.PolicyState, models.DeploymentPolicyStateFlagged)
		}
		if dep.PolicySource != models.DeploymentPolicySourceOverlay {
			t.Fatalf("PolicySource = %q, want %q", dep.PolicySource, models.DeploymentPolicySourceOverlay)
		}
		if dep.PolicyReason != "night-only route" {
			t.Fatalf("PolicyReason = %q, want %q", dep.PolicyReason, "night-only route")
		}
		if !dep.Routable {
			t.Fatal("Routable = false, want true")
		}
	}
	if !found {
		t.Fatal("persisted discovered deployment policy did not reapply after inventory")
	}
}

func TestModelPolicyStoreLoadIntoSkipsInvalidEntries(t *testing.T) {
	t.Parallel()

	db, err := database.OpenMemory()
	if err != nil {
		t.Fatalf("database.OpenMemory: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	op, err := opstate.NewStore(db)
	if err != nil {
		t.Fatalf("opstate.NewStore: %v", err)
	}
	if err := op.Set(modelRegistryPolicyNamespace, "spark/gpt-oss:20b", "{not-json"); err != nil {
		t.Fatalf("op.Set: %v", err)
	}

	store := newModelPolicyStore(op)
	registry := testModelPolicyRegistry(t)
	if err := store.LoadInto(registry, slog.Default()); err != nil {
		t.Fatalf("LoadInto: %v", err)
	}

	snap := registry.Snapshot()
	for _, dep := range snap.Deployments {
		if dep.ID == "spark/gpt-oss:20b" && dep.PolicySource != models.DeploymentPolicySourceDefault {
			t.Fatalf("PolicySource = %q, want default when persisted entry is invalid", dep.PolicySource)
		}
	}
}
