package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	modelproviders "github.com/nugget/thane-ai-agent/internal/model/fleet/providers"
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
		if r.URL.Path != "/api/v1/models" {
			t.Fatalf("path = %q, want /api/v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("Authorization = %q, want Bearer token", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{
					"key":                "google/gemma-3-4b",
					"type":               "vlm",
					"architecture":       "gemma3",
					"publisher":          "google",
					"format":             "mlx",
					"quantization":       map[string]any{"name": "4bit"},
					"max_context_length": 131072,
					"capabilities":       map[string]any{"vision": true},
					"loaded_instances": []map[string]any{
						{"id": "google/gemma-3-4b:1", "config": map[string]any{"context_length": 2048}},
						{"id": "google/gemma-3-4b:2", "config": map[string]any{"context_length": 4096}},
					},
				},
				{
					"key":                "qwen3:8b",
					"type":               "llm",
					"architecture":       "qwen3",
					"format":             "gguf",
					"quantization":       map[string]any{"name": "Q4_K_M"},
					"max_context_length": 32768,
				},
				{
					"key":                "text-embedding-nomic-embed-text-v1.5",
					"type":               "embeddings",
					"architecture":       "nomic-bert",
					"format":             "gguf",
					"quantization":       map[string]any{"name": "Q4_K_M"},
					"max_context_length": 2048,
				},
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
	if inv.Resources[0].Models[0].Name != "google/gemma-3-4b" || inv.Resources[0].Models[1].Name != "qwen3:8b" || inv.Resources[0].Models[2].Name != "text-embedding-nomic-embed-text-v1.5" {
		t.Fatalf("models = %+v", inv.Resources[0].Models)
	}
	if !inv.Resources[0].Models[0].SupportsChat || !inv.Resources[0].Models[0].SupportsStreaming || !inv.Resources[0].Models[0].SupportsTools || !inv.Resources[0].Models[0].SupportsImages {
		t.Fatalf("gemma model = %+v, want streaming/tools/images", inv.Resources[0].Models[0])
	}
	if inv.Resources[0].Models[0].ContextWindow != 4096 || inv.Resources[0].Models[0].MaxContextWindow != 131072 || inv.Resources[0].Models[0].LoadedContextWindow != 4096 {
		t.Fatalf("gemma context metadata = %+v, want ctx=4096 max=131072 loaded=4096", inv.Resources[0].Models[0])
	}
	if inv.Resources[0].Models[1].SupportsImages {
		t.Fatalf("qwen3 model = %+v, want image support=false", inv.Resources[0].Models[1])
	}
	if !inv.Resources[0].Models[1].SupportsChat || inv.Resources[0].Models[1].ContextWindow != 32768 {
		t.Fatalf("qwen3 model = %+v, want chat ctx=32768", inv.Resources[0].Models[1])
	}
	if inv.Resources[0].Models[2].SupportsChat || inv.Resources[0].Models[2].SupportsTools || inv.Resources[0].Models[2].SupportsStreaming || inv.Resources[0].Models[2].SupportsImages {
		t.Fatalf("embedding model = %+v, want non-chat capabilities disabled", inv.Resources[0].Models[2])
	}
}
