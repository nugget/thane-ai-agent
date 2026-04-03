package models

import (
	"context"
	"testing"
)

func TestDiscoverInventorySkipsUnsupportedProviders(t *testing.T) {
	t.Parallel()

	base := &Catalog{
		Resources: []Resource{
			{ID: "cloud", Provider: "anthropic", URL: "https://api.anthropic.com"},
		},
	}
	if err := base.reindex(base.DefaultModel, base.RecoveryModel); err != nil {
		t.Fatalf("reindex base: %v", err)
	}

	inv := DiscoverInventory(context.Background(), base, &ClientBundle{})
	if inv == nil {
		t.Fatal("DiscoverInventory returned nil")
	}
	if len(inv.Resources) != 0 {
		t.Fatalf("len(Resources) = %d, want 0 for unsupported providers", len(inv.Resources))
	}
}
