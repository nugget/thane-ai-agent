package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	modelproviders "github.com/nugget/thane-ai-agent/internal/models/providers"
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

func TestDiscoverInventoryIncludesLMStudioResources(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("Authorization = %q, want Bearer token", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "google/gemma-3-4b"},
				{"id": "qwen3:8b"},
				{"id": "gpt-oss:20b"},
			},
		})
	}))
	defer srv.Close()

	base := &Catalog{
		Resources: []Resource{
			{ID: "deepslate", Provider: "lmstudio", URL: srv.URL},
		},
	}
	if err := base.reindex(base.DefaultModel, base.RecoveryModel); err != nil {
		t.Fatalf("reindex base: %v", err)
	}

	inv := DiscoverInventory(context.Background(), base, &ClientBundle{
		LMStudioClients: map[string]*modelproviders.LMStudioClient{
			"deepslate": modelproviders.NewLMStudioClient(srv.URL, "secret-token", nil),
		},
	})
	if inv == nil {
		t.Fatal("DiscoverInventory returned nil")
	}
	if len(inv.Resources) != 1 {
		t.Fatalf("len(Resources) = %d, want 1", len(inv.Resources))
	}
	if !inv.Resources[0].Attempted {
		t.Fatal("expected LM Studio resource discovery to be attempted")
	}
	if !inv.Resources[0].Capabilities.SupportsStreaming || !inv.Resources[0].Capabilities.SupportsTools || !inv.Resources[0].Capabilities.SupportsImages {
		t.Fatalf("LM Studio capabilities = %+v, want streaming/tools/images", inv.Resources[0].Capabilities)
	}
	if len(inv.Resources[0].Models) != 3 {
		t.Fatalf("len(Models) = %d, want 3", len(inv.Resources[0].Models))
	}
	if inv.Resources[0].Models[0].Name != "google/gemma-3-4b" || inv.Resources[0].Models[1].Name != "gpt-oss:20b" || inv.Resources[0].Models[2].Name != "qwen3:8b" {
		t.Fatalf("models = %+v", inv.Resources[0].Models)
	}
	if !inv.Resources[0].Models[0].SupportsStreaming || !inv.Resources[0].Models[0].SupportsTools || !inv.Resources[0].Models[0].SupportsImages {
		t.Fatalf("gemma model = %+v, want streaming/tools/images", inv.Resources[0].Models[0])
	}
	if inv.Resources[0].Models[1].SupportsImages {
		t.Fatalf("gpt-oss model = %+v, want image support=false", inv.Resources[0].Models[1])
	}
	if inv.Resources[0].Models[2].SupportsImages {
		t.Fatalf("qwen3 model = %+v, want image support=false", inv.Resources[0].Models[2])
	}
}
