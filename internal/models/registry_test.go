package models

import (
	"errors"
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
	if !snap.Resources[0].SupportsStreaming || !snap.Resources[0].SupportsTools || !snap.Resources[0].SupportsImages || !snap.Resources[0].SupportsInventory {
		t.Fatalf("mirror capabilities = %+v, want streaming/tools/images/inventory", snap.Resources[0])
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
	qwen, ok := findPolicySnapshot(snap, "spark/qwen3:8b")
	if !ok {
		t.Fatal("missing spark/qwen3:8b snapshot")
	}
	if !qwen.SupportsTools || !qwen.ProviderSupportsTools || !qwen.SupportsStreaming {
		t.Fatalf("spark/qwen3:8b capabilities = %+v, want provider-driven tools/streaming", qwen)
	}
	if qwen.SupportsImages {
		t.Fatalf("spark/qwen3:8b capabilities = %+v, want image support=false", qwen)
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
	if !snap.Resources[0].SupportsTools || !snap.Resources[0].SupportsStreaming || !snap.Resources[0].SupportsImages {
		t.Fatalf("anthropic resource capabilities = %+v, want chat/streaming/tools/images metadata", snap.Resources[0])
	}
	if snap.Resources[0].SupportsInventory {
		t.Fatalf("anthropic SupportsInventory = true, want false")
	}
	if snap.Resources[0].LastRefresh != "" {
		t.Fatalf("LastRefresh = %q, want empty for unattempted resource", snap.Resources[0].LastRefresh)
	}
	if snap.Resources[0].DiscoveredModels != 0 {
		t.Fatalf("DiscoveredModels = %d, want 0", snap.Resources[0].DiscoveredModels)
	}
}

func TestRegistryDeploymentPolicyLifecycle(t *testing.T) {
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
	if err := reg.ApplyInventory(&Inventory{
		Resources: []ResourceInventory{
			{
				ResourceID: "mirror",
				Provider:   "ollama",
				Attempted:  true,
				Models: []DiscoveredModel{
					{Name: "qwen3:8b"},
				},
			},
		},
	}, time.Date(2026, 4, 3, 20, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("ApplyInventory: %v", err)
	}

	snap := reg.Snapshot()
	if snap == nil {
		t.Fatal("Snapshot returned nil")
	}

	dep, ok := findPolicySnapshot(snap, "spark/gpt-oss:20b")
	if !ok {
		t.Fatal("missing config deployment snapshot")
	}
	if dep.PolicyState != DeploymentPolicyStateActive {
		t.Fatalf("default policy state = %q, want %q", dep.PolicyState, DeploymentPolicyStateActive)
	}
	if dep.PolicySource != DeploymentPolicySourceDefault {
		t.Fatalf("default policy source = %q, want %q", dep.PolicySource, DeploymentPolicySourceDefault)
	}

	updatedAt := time.Date(2026, 4, 3, 20, 5, 0, 0, time.UTC)
	if err := reg.ApplyDeploymentPolicy("mirror/qwen3:8b", DeploymentPolicy{
		State:  DeploymentPolicyStateFlagged,
		Reason: "manual review",
	}, updatedAt); err != nil {
		t.Fatalf("ApplyDeploymentPolicy: %v", err)
	}

	snap = reg.Snapshot()
	dep, ok = findPolicySnapshot(snap, "mirror/qwen3:8b")
	if !ok {
		t.Fatal("missing discovered deployment snapshot")
	}
	if dep.PolicyState != DeploymentPolicyStateFlagged {
		t.Fatalf("policy state = %q, want %q", dep.PolicyState, DeploymentPolicyStateFlagged)
	}
	if dep.PolicySource != DeploymentPolicySourceOverlay {
		t.Fatalf("policy source = %q, want %q", dep.PolicySource, DeploymentPolicySourceOverlay)
	}
	if dep.PolicyReason != "manual review" {
		t.Fatalf("policy reason = %q, want %q", dep.PolicyReason, "manual review")
	}
	if dep.PolicyUpdated != updatedAt.Format(time.RFC3339) {
		t.Fatalf("policy updated = %q, want %q", dep.PolicyUpdated, updatedAt.Format(time.RFC3339))
	}

	// A later inventory refresh should preserve the explicit policy.
	if err := reg.ApplyInventory(&Inventory{
		Resources: []ResourceInventory{
			{
				ResourceID: "mirror",
				Provider:   "ollama",
				Attempted:  true,
				Models: []DiscoveredModel{
					{Name: "qwen3:8b"},
					{Name: "llama3.2:latest"},
				},
			},
		},
	}, time.Date(2026, 4, 3, 20, 10, 0, 0, time.UTC)); err != nil {
		t.Fatalf("ApplyInventory second refresh: %v", err)
	}

	snap = reg.Snapshot()
	dep, ok = findPolicySnapshot(snap, "mirror/qwen3:8b")
	if !ok {
		t.Fatal("missing discovered deployment snapshot after refresh")
	}
	if dep.PolicyState != DeploymentPolicyStateFlagged {
		t.Fatalf("post-refresh policy state = %q, want %q", dep.PolicyState, DeploymentPolicyStateFlagged)
	}

	if err := reg.ClearDeploymentPolicy("mirror/qwen3:8b", time.Date(2026, 4, 3, 20, 12, 0, 0, time.UTC)); err != nil {
		t.Fatalf("ClearDeploymentPolicy: %v", err)
	}

	snap = reg.Snapshot()
	dep, ok = findPolicySnapshot(snap, "mirror/qwen3:8b")
	if !ok {
		t.Fatal("missing deployment snapshot after clear")
	}
	if dep.PolicyState != DeploymentPolicyStateActive {
		t.Fatalf("cleared policy state = %q, want %q", dep.PolicyState, DeploymentPolicyStateActive)
	}
	if dep.PolicySource != DeploymentPolicySourceDefault {
		t.Fatalf("cleared policy source = %q, want %q", dep.PolicySource, DeploymentPolicySourceDefault)
	}
	if dep.PolicyReason != "" {
		t.Fatalf("cleared policy reason = %q, want empty", dep.PolicyReason)
	}
	if dep.PolicyUpdated != "" {
		t.Fatalf("cleared policy updated = %q, want empty", dep.PolicyUpdated)
	}
}

func TestRegistryDeploymentPolicyCanPromoteDiscoveredDeploymentIntoRouting(t *testing.T) {
	t.Parallel()

	base := &Catalog{
		DefaultModel: "spark/gpt-oss:20b",
		LocalFirst:   true,
		Resources: []Resource{
			{ID: "deepslate", Provider: "lmstudio", URL: "http://deepslate.example"},
			{ID: "spark", Provider: "ollama", URL: "http://spark.example"},
		},
		Deployments: []Deployment{
			{
				ID:                    "spark/gpt-oss:20b",
				ModelName:             "gpt-oss:20b",
				Provider:              "ollama",
				ResourceID:            "spark",
				Server:                "spark",
				SupportsTools:         true,
				ProviderSupportsTools: true,
				SupportsStreaming:     true,
				ContextWindow:         8192,
				Speed:                 6,
				Quality:               6,
				CostTier:              0,
				Source:                DeploymentSourceConfig,
				Routable:              true,
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
	if err := reg.ApplyInventory(&Inventory{
		Resources: []ResourceInventory{
			{
				ResourceID: "deepslate",
				Provider:   "lmstudio",
				Attempted:  true,
				Models: []DiscoveredModel{
					{
						Name:              "google/gemma-3-4b",
						SupportsTools:     true,
						SupportsStreaming: true,
						SupportsImages:    true,
					},
				},
			},
		},
	}, time.Date(2026, 4, 3, 21, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("ApplyInventory: %v", err)
	}

	snap := reg.Snapshot()
	dep, ok := findPolicySnapshot(snap, "deepslate/google/gemma-3-4b")
	if !ok {
		t.Fatal("missing discovered deployment snapshot")
	}
	if dep.Routable {
		t.Fatalf("default discovered Routable = true, want false")
	}
	if dep.RoutableSource != DeploymentPolicySourceDefault {
		t.Fatalf("default discovered RoutableSource = %q, want %q", dep.RoutableSource, DeploymentPolicySourceDefault)
	}

	routable := true
	updatedAt := time.Date(2026, 4, 3, 21, 5, 0, 0, time.UTC)
	if err := reg.ApplyDeploymentPolicy("deepslate/google/gemma-3-4b", DeploymentPolicy{
		State:    DeploymentPolicyStateActive,
		Routable: &routable,
		Reason:   "promote vision route",
	}, updatedAt); err != nil {
		t.Fatalf("ApplyDeploymentPolicy: %v", err)
	}

	snap = reg.Snapshot()
	dep, ok = findPolicySnapshot(snap, "deepslate/google/gemma-3-4b")
	if !ok {
		t.Fatal("missing promoted deployment snapshot")
	}
	if !dep.Routable {
		t.Fatalf("promoted Routable = false, want true")
	}
	if dep.RoutableSource != DeploymentPolicySourceOverlay {
		t.Fatalf("promoted RoutableSource = %q, want %q", dep.RoutableSource, DeploymentPolicySourceOverlay)
	}

	routerCfg := reg.Catalog().RouterConfig(100)
	found := false
	for _, model := range routerCfg.Models {
		if model.Name == "deepslate/google/gemma-3-4b" {
			found = true
			if !model.SupportsImages {
				t.Fatalf("promoted router model = %+v, want image support", model)
			}
		}
	}
	if !found {
		t.Fatal("promoted discovered deployment missing from router config")
	}

	if err := reg.ClearDeploymentPolicy("deepslate/google/gemma-3-4b", time.Date(2026, 4, 3, 21, 10, 0, 0, time.UTC)); err != nil {
		t.Fatalf("ClearDeploymentPolicy: %v", err)
	}

	snap = reg.Snapshot()
	dep, ok = findPolicySnapshot(snap, "deepslate/google/gemma-3-4b")
	if !ok {
		t.Fatal("missing deployment snapshot after clear")
	}
	if dep.Routable {
		t.Fatalf("cleared Routable = true, want false")
	}
	if dep.RoutableSource != DeploymentPolicySourceDefault {
		t.Fatalf("cleared RoutableSource = %q, want %q", dep.RoutableSource, DeploymentPolicySourceDefault)
	}
}

func findPolicySnapshot(snapshot *RegistrySnapshot, id string) (RegistryDeploymentSnapshot, bool) {
	for _, dep := range snapshot.Deployments {
		if dep.ID == id {
			return dep, true
		}
	}
	return RegistryDeploymentSnapshot{}, false
}

func TestRegistryApplyDeploymentPolicy_UnknownDeployment(t *testing.T) {
	base := &Catalog{
		DefaultModel: "spark/gpt-oss:20b",
		Resources: []Resource{
			{ID: "spark", Provider: "ollama", URL: "http://spark.example"},
		},
		Deployments: []Deployment{
			{
				ID:         "spark/gpt-oss:20b",
				ModelName:  "gpt-oss:20b",
				Provider:   "ollama",
				ResourceID: "spark",
				Server:     "spark",
				Source:     DeploymentSourceConfig,
				Routable:   true,
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
	err = reg.ApplyDeploymentPolicy("missing/model", DeploymentPolicy{State: DeploymentPolicyStateFlagged}, time.Now())
	if err == nil {
		t.Fatal("ApplyDeploymentPolicy error = nil, want unknown deployment error")
	}
	var target *UnknownDeploymentError
	if !errors.As(err, &target) {
		t.Fatalf("ApplyDeploymentPolicy error = %T, want *UnknownDeploymentError", err)
	}
}

func TestRegistryReplaceDeploymentPolicies_ReappliesWhenDiscoveredDeploymentReturns(t *testing.T) {
	t.Parallel()

	base := &Catalog{
		DefaultModel: "spark/gpt-oss:20b",
		LocalFirst:   true,
		Resources: []Resource{
			{ID: "deepslate", Provider: "lmstudio", URL: "http://deepslate.example"},
			{ID: "spark", Provider: "ollama", URL: "http://spark.example"},
		},
		Deployments: []Deployment{
			{
				ID:                    "spark/gpt-oss:20b",
				ModelName:             "gpt-oss:20b",
				Provider:              "ollama",
				ResourceID:            "spark",
				Server:                "spark",
				SupportsTools:         true,
				ProviderSupportsTools: true,
				SupportsStreaming:     true,
				ContextWindow:         8192,
				Speed:                 6,
				Quality:               6,
				CostTier:              0,
				Source:                DeploymentSourceConfig,
				Routable:              true,
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

	routable := true
	policyTime := time.Date(2026, 4, 4, 1, 0, 0, 0, time.UTC)
	if err := reg.ReplaceDeploymentPolicies(map[string]DeploymentPolicy{
		"deepslate/google/gemma-3-4b": {
			State:     DeploymentPolicyStateFlagged,
			Routable:  &routable,
			Reason:    "remember this overlay while host is away",
			UpdatedAt: policyTime,
		},
	}, policyTime); err != nil {
		t.Fatalf("ReplaceDeploymentPolicies: %v", err)
	}

	snap := reg.Snapshot()
	if _, ok := findPolicySnapshot(snap, "deepslate/google/gemma-3-4b"); ok {
		t.Fatal("absent discovered deployment should not appear before inventory returns")
	}

	if err := reg.ApplyInventory(&Inventory{
		Resources: []ResourceInventory{
			{
				ResourceID: "deepslate",
				Provider:   "lmstudio",
				Attempted:  true,
				Models: []DiscoveredModel{
					{
						Name:              "google/gemma-3-4b",
						SupportsTools:     true,
						SupportsStreaming: true,
						SupportsImages:    true,
					},
				},
			},
		},
	}, time.Date(2026, 4, 4, 1, 5, 0, 0, time.UTC)); err != nil {
		t.Fatalf("ApplyInventory: %v", err)
	}

	snap = reg.Snapshot()
	dep, ok := findPolicySnapshot(snap, "deepslate/google/gemma-3-4b")
	if !ok {
		t.Fatal("missing discovered deployment snapshot after rediscovery")
	}
	if dep.PolicyState != DeploymentPolicyStateFlagged {
		t.Fatalf("PolicyState = %q, want %q", dep.PolicyState, DeploymentPolicyStateFlagged)
	}
	if dep.PolicySource != DeploymentPolicySourceOverlay {
		t.Fatalf("PolicySource = %q, want %q", dep.PolicySource, DeploymentPolicySourceOverlay)
	}
	if dep.PolicyReason != "remember this overlay while host is away" {
		t.Fatalf("PolicyReason = %q, want remembered reason", dep.PolicyReason)
	}
	if !dep.Routable {
		t.Fatal("Routable = false, want true from persisted overlay")
	}
	if dep.RoutableSource != DeploymentPolicySourceOverlay {
		t.Fatalf("RoutableSource = %q, want %q", dep.RoutableSource, DeploymentPolicySourceOverlay)
	}
}
