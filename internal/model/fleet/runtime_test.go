package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	modelproviders "github.com/nugget/thane-ai-agent/internal/model/fleet/providers"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
)

func TestRuntimePrepareExplicitModel_LoadsLMStudioContext(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	loadedContext := 4096

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			mu.Lock()
			current := loadedContext
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(struct {
				Models []map[string]any `json:"models"`
			}{
				Models: []map[string]any{{
					"key":                "google/gemma-3-4b",
					"type":               "vlm",
					"architecture":       "gemma3",
					"format":             "mlx",
					"max_context_length": 131072,
					"capabilities": map[string]any{
						"vision": true,
					},
					"loaded_instances": []map[string]any{{
						"id": "google/gemma-3-4b",
						"config": map[string]any{
							"context_length": current,
						},
					}},
				}},
			})
		case "/api/v1/models/load":
			var req struct {
				Model         string `json:"model"`
				ContextLength int    `json:"context_length"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode load request: %v", err)
			}
			if req.Model != "google/gemma-3-4b" {
				t.Fatalf("load model = %q, want google/gemma-3-4b", req.Model)
			}
			if req.ContextLength != 6144 {
				t.Fatalf("context_length = %d, want 6144", req.ContextLength)
			}
			mu.Lock()
			loadedContext = req.ContextLength
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(modelproviders.LMStudioLoadResponse{
				Type:       "llm",
				InstanceID: "google/gemma-3-4b:2",
				Status:     "loaded",
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	cfg := &config.Config{
		Models: config.ModelsConfig{
			Resources: map[string]config.ModelServerConfig{
				"deepslate": {URL: srv.URL, Provider: "lmstudio"},
			},
		},
	}
	base, err := BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("BuildCatalog: %v", err)
	}
	runtime, err := NewRuntime(context.Background(), base, cfg, nil)
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}

	dep, err := runtime.Registry().Catalog().ResolveDeploymentRef("deepslate/google/gemma-3-4b")
	if err != nil {
		t.Fatalf("ResolveDeploymentRef before prepare: %v", err)
	}
	if dep.LoadedContextWindow != 4096 {
		t.Fatalf("initial LoadedContextWindow = %d, want 4096", dep.LoadedContextWindow)
	}

	prep, err := runtime.PrepareExplicitModel(context.Background(), "deepslate/google/gemma-3-4b", 6144)
	if err != nil {
		t.Fatalf("PrepareExplicitModel: %v", err)
	}
	if prep == nil || !prep.Changed {
		t.Fatal("PrepareExplicitModel changed = false, want true")
	}
	if prep.Instance != "google/gemma-3-4b:2" {
		t.Fatalf("prep.Instance = %q, want google/gemma-3-4b:2", prep.Instance)
	}
	if prep.Resolved != "deepslate/google/gemma-3-4b" {
		t.Fatalf("prep.Resolved = %q, want deepslate/google/gemma-3-4b", prep.Resolved)
	}

	dep, err = runtime.Registry().Catalog().ResolveDeploymentRef("deepslate/google/gemma-3-4b")
	if err != nil {
		t.Fatalf("ResolveDeploymentRef after prepare: %v", err)
	}
	if dep.LoadedContextWindow != 6144 {
		t.Fatalf("LoadedContextWindow = %d, want 6144", dep.LoadedContextWindow)
	}
	if dep.ContextWindow != 6144 {
		t.Fatalf("ContextWindow = %d, want 6144", dep.ContextWindow)
	}

	client := runtime.Client()
	if client == nil {
		t.Fatal("runtime.Client() = nil, want non-nil")
	}
	if _, ok := client.(*llm.DynamicClient); !ok {
		t.Fatalf("runtime.Client() = %T, want *llm.DynamicClient", client)
	}
}

func TestRuntime_SetLogger_RebindsAllProviderClients(t *testing.T) {
	t.Parallel()

	bootBuf := &bytes.Buffer{}
	bootLogger := slog.New(slog.NewJSONHandler(bootBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	bundle := &ClientBundle{
		OllamaClients: map[string]*modelproviders.OllamaClient{
			"hearth": modelproviders.NewOllamaClient("http://127.0.0.1:11434", bootLogger.With("resource", "hearth")),
		},
		LMStudioClients: map[string]*modelproviders.LMStudioClient{
			"deepslate": modelproviders.NewLMStudioClient("http://127.0.0.1:1234", "", bootLogger.With("resource", "deepslate")),
		},
		AnthropicClient: modelproviders.NewAnthropicClient("k", bootLogger),
	}
	rt := &Runtime{bundle: bundle}

	prodBuf := &bytes.Buffer{}
	prodLogger := slog.New(slog.NewJSONHandler(prodBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rt.SetLogger(prodLogger)

	// Each rebound logger should reach the prod buffer at Debug, with
	// the resource attribute restored for per-resource clients and the
	// provider attribute restored on every client.
	bundle.OllamaClients["hearth"].Logger().Debug("ollama probe")
	bundle.LMStudioClients["deepslate"].Logger().Debug("lmstudio probe")
	bundle.AnthropicClient.Logger().Debug("anthropic probe")

	out := prodBuf.String()
	for _, want := range []string{
		`"msg":"ollama probe"`, `"resource":"hearth"`, `"provider":"ollama"`,
		`"msg":"lmstudio probe"`, `"resource":"deepslate"`, `"provider":"lmstudio"`,
		`"msg":"anthropic probe"`, `"provider":"anthropic"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rebound output missing %q\n--- output ---\n%s", want, out)
		}
	}

	// Bootstrap logger must not have received any of the Debug lines —
	// SetLogger should fully replace the reference, not tee.
	if bootBuf.Len() != 0 {
		t.Fatalf("bootstrap logger still receiving messages after rebind: %s", bootBuf.String())
	}
}

func TestRuntime_SetLogger_NilGuards(t *testing.T) {
	t.Parallel()

	var rt *Runtime
	rt.SetLogger(slog.Default()) // nil receiver

	rt = &Runtime{}
	rt.SetLogger(slog.Default()) // nil bundle

	rt = &Runtime{bundle: &ClientBundle{}}
	rt.SetLogger(nil) // nil logger
}
