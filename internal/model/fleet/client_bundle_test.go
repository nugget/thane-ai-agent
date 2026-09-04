package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
)

type testBundleClient struct {
	lastModel string
}

func (c *testBundleClient) Chat(_ context.Context, model string, _ []llm.Message, _ []map[string]any) (*llm.ChatResponse, error) {
	c.lastModel = model
	return &llm.ChatResponse{
		Model:   model,
		Message: llm.Message{Role: "assistant", Content: "ok"},
		Done:    true,
	}, nil
}

func (c *testBundleClient) ChatStream(_ context.Context, model string, _ []llm.Message, _ []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	c.lastModel = model
	return &llm.ChatResponse{
		Model:   model,
		Message: llm.Message{Role: "assistant", Content: "ok"},
		Done:    true,
	}, nil
}

func (c *testBundleClient) Ping(context.Context) error { return nil }

func TestClientBundleBuildRoutedClient_SelectsDeterministicFallback(t *testing.T) {
	cat := &Catalog{
		Resources: []Resource{
			{ID: "mirror", Provider: "ollama", URL: "http://127.0.0.1:11434"},
			{ID: "spark", Provider: "ollama", URL: "http://127.0.0.1:11434"},
		},
	}
	if err := cat.reindex("", ""); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	mirror := &testBundleClient{}
	spark := &testBundleClient{}
	bundle := &ClientBundle{
		ResourceClients: map[string]llm.Client{
			"spark":  spark,
			"mirror": mirror,
		},
	}

	client, err := bundle.BuildRoutedClient(cat)
	if err != nil {
		t.Fatalf("BuildRoutedClient: %v", err)
	}
	resp, err := client.Chat(context.Background(), "unknown-model", nil, nil)
	if err != nil {
		t.Fatalf("Chat fallback: %v", err)
	}
	if resp.Model != "unknown-model" {
		t.Fatalf("resp.Model = %q, want unknown-model", resp.Model)
	}
	if mirror.lastModel != "unknown-model" {
		t.Fatalf("mirror fallback model = %q, want unknown-model", mirror.lastModel)
	}
	if spark.lastModel != "" {
		t.Fatalf("spark should not be used for fallback, got %q", spark.lastModel)
	}
}

func TestClientBundleBuildRoutedClient_UsesLMStudioLoadedInstanceForUpstream(t *testing.T) {
	cat := &Catalog{
		Resources: []Resource{
			{ID: "deepslate", Provider: "lmstudio", URL: "http://127.0.0.1:1234"},
		},
		Deployments: []Deployment{{
			ID:               "deepslate/google/gemma-3-4b",
			ModelName:        "google/gemma-3-4b",
			Provider:         "lmstudio",
			ResourceID:       "deepslate",
			LoadedInstanceID: "google/gemma-3-4b:7",
		}},
	}
	if err := cat.reindex("deepslate/google/gemma-3-4b", ""); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	clientImpl := &testBundleClient{}
	bundle := &ClientBundle{
		ResourceClients: map[string]llm.Client{
			"deepslate": clientImpl,
		},
	}

	client, err := bundle.BuildRoutedClient(cat)
	if err != nil {
		t.Fatalf("BuildRoutedClient: %v", err)
	}
	resp, err := client.Chat(context.Background(), "deepslate/google/gemma-3-4b", nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if clientImpl.lastModel != "google/gemma-3-4b:7" {
		t.Fatalf("upstream model = %q, want loaded instance id", clientImpl.lastModel)
	}
	if resp.Model != "deepslate/google/gemma-3-4b" {
		t.Fatalf("resp.Model = %q, want stable deployment id", resp.Model)
	}
}

// TestBuildClientsAppliesChatTemplateKwargs pins the whole path from the
// config key to the request body. The per-client test proves the field
// can be sent; this one proves an operator who writes it in config
// actually gets it sent, which is the part that decides whether a model
// with thinking on by default is usable for a turn with a bounded
// output budget.
func TestBuildClientsAppliesChatTemplateKwargs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		kwargs   map[string]any
		provider string
		want     map[string]any
	}{
		{
			name:     "openai_compat resource sends its configured kwargs",
			provider: "openai_compat",
			kwargs:   map[string]any{"enable_thinking": false},
			want:     map[string]any{"enable_thinking": false},
		},
		{
			// LM Studio speaks the same protocol through the embedded
			// client, so the policy has to reach it by the same path.
			name:     "lmstudio resource sends them too",
			provider: "lmstudio",
			kwargs:   map[string]any{"enable_thinking": false},
			want:     map[string]any{"enable_thinking": false},
		},
		{
			name:     "a resource that configured none sends none",
			provider: "openai_compat",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var body map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/chat/completions") {
					_ = json.NewDecoder(r.Body).Decode(&body)
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[]}`))
			}))
			defer srv.Close()

			cfg := &config.Config{
				Models: config.ModelsConfig{
					Resources: map[string]config.ModelServerConfig{
						"spark": {URL: srv.URL, Provider: tt.provider, ChatTemplateKwargs: tt.kwargs},
					},
					Available: []config.ModelConfig{
						{Name: "m", Resource: "spark", Provider: tt.provider},
					},
				},
			}
			cat, err := BuildCatalog(cfg)
			if err != nil {
				t.Fatalf("BuildCatalog: %v", err)
			}
			bundle, err := BuildClients(cat, cfg, nil)
			if err != nil {
				t.Fatalf("BuildClients: %v", err)
			}

			client, ok := bundle.ResourceClients["spark"]
			if !ok {
				t.Fatalf("no client built for resource spark")
			}
			if _, err := client.Chat(context.Background(), "m", []llm.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
				t.Fatalf("Chat: %v", err)
			}

			got, present := body["chat_template_kwargs"]
			if tt.want == nil {
				if present {
					t.Errorf("chat_template_kwargs sent unconfigured: %#v", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("chat_template_kwargs = %#v, want %#v", got, tt.want)
			}
		})
	}
}
