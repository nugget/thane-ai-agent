package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// TestDiscoverInventory_OpenAICompat pins the discovery branch added for
// the shared client. The gap it closes was that a provider can be fully
// declared — construction switch, capability table, config docs — and
// still be invisible to discovery, so /v1/models is never queried and no
// context ceiling ever reaches a deployment.
func TestDiscoverInventory_OpenAICompat(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// vLLM spells the ceiling max_model_len; a plain server sends none.
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"gpt-oss:120b","max_model_len":131072},
			{"id":"plain-model"}
		]}`))
	}))
	defer srv.Close()

	cat := &Catalog{Resources: []Resource{{ID: "spark", Provider: "openai_compat", URL: srv.URL}}}
	bundle := &ClientBundle{
		OpenAICompatClients: map[string]*modelproviders.OpenAICompatClient{
			"spark": modelproviders.NewOpenAICompatClient(srv.URL, "", "openai_compat", "res", nil, 0),
		},
	}

	inv := DiscoverInventory(context.Background(), cat, bundle)
	if len(inv.Resources) != 1 {
		t.Fatalf("resources = %d, want 1", len(inv.Resources))
	}
	ri := inv.Resources[0]
	if ri.Error != "" {
		t.Fatalf("discovery error: %s", ri.Error)
	}
	if len(ri.Models) != 2 {
		t.Fatalf("models = %d, want 2", len(ri.Models))
	}

	byName := map[string]DiscoveredModel{}
	for _, m := range ri.Models {
		byName[m.Name] = m
	}
	if got := byName["gpt-oss:120b"]; got.ContextWindow != 131072 || got.MaxContextWindow != 131072 {
		t.Errorf("gpt-oss context = %d/%d, want 131072", got.ContextWindow, got.MaxContextWindow)
	}
	// A server that reports no ceiling yields zero meaning "not
	// reported" — the deployment keeps its configured window rather than
	// inheriting a fabricated one.
	if got := byName["plain-model"]; got.ContextWindow != 0 {
		t.Errorf("unreported ceiling = %d, want 0", got.ContextWindow)
	}
	if !byName["gpt-oss:120b"].SupportsStreaming {
		t.Error("discovered model should inherit streaming support from the provider capabilities")
	}
}

// TestDiscoverInventory_SlowResourceDoesNotStarveOthers pins the two
// properties that make discovery survivable at boot: one unreachable
// resource is bounded rather than unbounded, and it does not delay the
// resources behind it. Sequential probing gave a runner that completes
// its handshake and then goes quiet the power to hold up startup
// entirely — the failure the stream-idle guard closes during
// generation, one loop earlier.
func TestDiscoverInventory_SlowResourceDoesNotStarveOthers(t *testing.T) {
	t.Parallel()

	// Defer order matters: httptest.Close waits for in-flight handlers,
	// so the handler must be released before the server is closed. LIFO
	// means close(release) has to be deferred last.
	release := make(chan struct{})
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer stalled.Close()
	defer close(release)

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"live-model"}]}`))
	}))
	defer healthy.Close()

	cat := &Catalog{Resources: []Resource{
		{ID: "stalled", Provider: "openai_compat", URL: stalled.URL},
		{ID: "healthy", Provider: "openai_compat", URL: healthy.URL},
	}}
	bundle := &ClientBundle{OpenAICompatClients: map[string]*modelproviders.OpenAICompatClient{
		"stalled": modelproviders.NewOpenAICompatClient(stalled.URL, "", "openai_compat", "stalled", nil, 0),
		"healthy": modelproviders.NewOpenAICompatClient(healthy.URL, "", "openai_compat", "healthy", nil, 0),
	}}

	// A deadline well under resourceProbeTimeout stands in for it, so the
	// test asserts the bound without waiting for the production one.
	// Generous enough that a loaded CI box cannot cancel the healthy
	// probe by accident: what is under test is that the stall is
	// bounded and does not starve its neighbor, not how quickly a
	// local httptest server answers.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan *Inventory, 1)
	go func() { done <- DiscoverInventory(ctx, cat, bundle) }()

	var inv *Inventory
	select {
	case inv = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("discovery never returned — a stalled resource blocked the pass")
	}

	if len(inv.Resources) != 2 {
		t.Fatalf("resources = %d, want both probed", len(inv.Resources))
	}
	// Catalog order is preserved regardless of completion order.
	if inv.Resources[0].ResourceID != "stalled" || inv.Resources[1].ResourceID != "healthy" {
		t.Errorf("resource order = %q, %q; want catalog order", inv.Resources[0].ResourceID, inv.Resources[1].ResourceID)
	}
	if inv.Resources[0].Error == "" {
		t.Error("stalled resource reported no error")
	}
	// The healthy resource is not punished for its neighbor.
	if len(inv.Resources[1].Models) != 1 || inv.Resources[1].Models[0].Name != "live-model" {
		t.Errorf("healthy resource models = %#v, want the one it serves", inv.Resources[1].Models)
	}
}
