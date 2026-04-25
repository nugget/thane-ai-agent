package fleet

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
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
