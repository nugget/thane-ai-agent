package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func TestRun_RequestSizingUsesVisibleSchemas(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		mode := "automatic"
		if explicit {
			mode = "explicit"
		}
		for _, surface := range []string{"all", "allowlist", "exclusion", "orchestrator", "inactive tag", "active tag", "runtime tool"} {
			t.Run(mode+"/"+surface, func(t *testing.T) {
				mock := &mockLLM{responses: []*llm.ChatResponse{{Message: llm.Message{Role: "assistant", Content: "done"}}}}
				loop := buildTestLoop(mock, nil)
				loop.tools = tools.NewEmptyRegistry()
				loop.tools.Register(&tools.Tool{Name: "thane_now", Description: "Read the time.", Parameters: map[string]any{"type": "object"}})
				bulky := &tools.Tool{
					Name: "bulky", Description: strings.Repeat("A detailed resource schema. ", 2000),
					Parameters: map[string]any{"type": "object"},
				}
				if surface != "runtime tool" {
					loop.tools.Register(bulky)
				}
				req := &Request{Messages: []Message{{Role: "user", Content: "Check the status."}}, SystemPrompt: "Help with the request."}
				visibleBulky := surface == "all" || surface == "active tag" || surface == "runtime tool"
				switch surface {
				case "allowlist":
					req.AllowedTools = []string{"thane_now"}
				case "exclusion":
					req.ExcludeTools = []string{"bulky"}
				case "orchestrator":
					loop.orchestratorTools = []string{"thane_now"}
				case "inactive tag", "active tag":
					loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
						"core":  {Core: true, Tools: []string{"thane_now"}},
						"extra": {Tools: []string{"bulky"}},
					}, nil)
					if surface == "active tag" {
						req.InitialTags = []string{"extra"}
					}
				case "runtime tool":
					req.RuntimeTools = []*tools.Tool{bulky}
				}
				cfg := &config.Config{Models: config.ModelsConfig{
					Default: "small", LocalFirst: true,
					Resources: map[string]config.ModelServerConfig{"local": {URL: "http://localhost:11434", Provider: "ollama"}},
					Available: []config.ModelConfig{
						{Name: "small", Resource: "local", SupportsTools: true, ContextWindow: 2048, Speed: 10, Quality: 8},
						{Name: "large", Resource: "local", SupportsTools: true, ContextWindow: 100000, Speed: 3, Quality: 8},
					},
				}}
				registry := testModelRegistryFromConfig(t, cfg)
				loop.UseModelRegistry(registry)
				loop.router = router.NewRouter(loop.logger, registry.Catalog().RouterConfig(32))
				if explicit {
					req.Model = "small"
				}
				_, err := loop.Run(context.Background(), req, nil)
				if explicit && visibleBulky {
					var incompatible *IncompatibleModelError
					if !errors.As(err, &incompatible) || len(mock.calls) != 0 {
						t.Fatalf("oversized explicit request: error=%v call count=%d", err, len(mock.calls))
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				wantModel := "small"
				if visibleBulky {
					wantModel = "large"
				}
				if len(mock.calls) != 1 {
					t.Fatalf("call count=%d, want one call to %s", len(mock.calls), wantModel)
				}
				if mock.calls[0].Model != wantModel {
					t.Fatalf("provider model=%s, want %s", mock.calls[0].Model, wantModel)
				}
				if got := hasName(toolNames(mock.calls[0].Tools), "bulky"); got != visibleBulky {
					t.Fatalf("provider bulky schema visibility=%t, want %t", got, visibleBulky)
				}
			})
		}
	}
}

func TestRun_RoutedPreflightIncludesSchemasAfterPromptGrowth(t *testing.T) {
	mock := &mockLLM{responses: []*llm.ChatResponse{{Message: llm.Message{Role: "assistant", Content: "done"}}}}
	loop := buildTestLoop(mock, nil)
	loop.tools = tools.NewEmptyRegistry()
	loop.tools.Register(&tools.Tool{
		Name: "inspect", Description: strings.Repeat("Inspect source evidence. ", 500),
		Parameters: map[string]any{"type": "object"},
	})
	userMessage := "what is the status"
	messages := []Message{{Role: "user", Content: userMessage}}
	defaultPrompt, defaultSections := loop.buildSystemPromptWithProfileSections(context.Background(), userMessage, llm.DefaultModelInteractionProfile())
	localPrompt, localSections := loop.buildSystemPromptWithProfileSections(context.Background(), userMessage, loop.modelInteractionProfileForModel("qwen3:8b"))
	initial := buildInitialLLMMessages(defaultPrompt, defaultSections, nil, messages, "default", time.Time{})
	rebuilt := buildInitialLLMMessages(localPrompt, localSections, nil, messages, "default", time.Time{})
	initialSize := estimateRequestContextTokens(initial, loop.tools.List())
	rebuiltSize := estimateRequestContextTokens(rebuilt, loop.tools.List())
	window := rebuiltSize - 1
	if initialSize >= window || estimateLLMMessagesContextTokens(rebuilt) >= window {
		t.Fatalf("invalid setup: initial=%d rebuilt=%d messages-only=%d window=%d", initialSize, rebuiltSize, estimateLLMMessagesContextTokens(rebuilt), window)
	}
	registry := testModelRegistryFromConfig(t, &config.Config{Models: config.ModelsConfig{
		Default: "qwen3:8b", LocalFirst: true,
		Resources: map[string]config.ModelServerConfig{
			"local": {URL: "http://localhost:11434", Provider: "ollama"},
			"cloud": {URL: "https://api.anthropic.com", Provider: "anthropic"},
		},
		Available: []config.ModelConfig{
			{Name: "qwen3:8b", Resource: "local", SupportsTools: true, ContextWindow: window, Speed: 10, Quality: 7},
			{Name: "claude-sonnet-4-20250514", Resource: "cloud", SupportsTools: true, ContextWindow: 100000, Speed: 4, Quality: 9, CostTier: 3},
		},
	}})
	loop.UseModelRegistry(registry)
	loop.router = router.NewRouter(loop.logger, registry.Catalog().RouterConfig(32))
	if _, err := loop.Run(context.Background(), &Request{Messages: messages}, nil); err != nil {
		t.Fatal(err)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("provider call count=%d, want one", len(mock.calls))
	}
	if mock.calls[0].Model != "claude-sonnet-4-20250514" {
		t.Fatalf("provider model=%s, want the larger model after prompt growth", mock.calls[0].Model)
	}
}
