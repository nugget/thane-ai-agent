package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

type rawTextMockLLM struct {
	mu          sync.Mutex
	responses   []*llm.ChatResponse
	callIndex   int
	calls       []mockLLMCall
	textProfile llm.ToolCallTextProfile
}

func (m *rawTextMockLLM) Chat(ctx context.Context, model string, msgs []llm.Message, td []map[string]any) (*llm.ChatResponse, error) {
	return m.ChatStream(ctx, model, msgs, td, nil)
}

func (m *rawTextMockLLM) ChatStream(_ context.Context, model string, msgs []llm.Message, td []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.calls = append(m.calls, mockLLMCall{Model: model, Messages: msgs, Tools: td})
	if m.callIndex >= len(m.responses) {
		return nil, fmt.Errorf("rawTextMockLLM: no more responses (call %d)", m.callIndex)
	}

	resp := *m.responses[m.callIndex]
	m.callIndex++
	llm.ApplyTextToolCallFallback(&resp, llm.ExtractToolNames(td), m.textProfile)
	return &resp, nil
}

func (m *rawTextMockLLM) Ping(_ context.Context) error { return nil }

func buildTestLoopWithClient(client llm.Client, extraNames []string, model string) *Loop {
	reg := tools.NewRegistry(nil, nil)
	for _, name := range extraNames {
		n := name
		reg.Register(&tools.Tool{
			Name:        n,
			Description: "test tool " + n,
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			Handler: func(_ context.Context, _ map[string]any) (string, error) {
				return "ok", nil
			},
		})
	}

	return &Loop{
		logger: slog.Default(),
		memory: newMockMem(),
		llm:    client,
		tools:  reg,
		model:  model,
	}
}

func setupCapabilityLoopWithClient(client llm.Client, extraNames []string, capTags map[string]config.CapabilityTagConfig, model string) *Loop {
	loop := buildTestLoopWithClient(client, extraNames, model)
	loop.SetCapabilityTags(capTags, nil)

	tagTools := make(map[string][]string, len(capTags))
	descriptions := make(map[string]string, len(capTags))
	alwaysActive := make(map[string]bool, len(capTags))
	protected := make(map[string]bool, len(capTags))
	for tag, cfg := range capTags {
		tagTools[tag] = cfg.Tools
		descriptions[tag] = cfg.Description
		alwaysActive[tag] = cfg.AlwaysActive
		protected[tag] = cfg.Protected
	}
	manifest := tools.BuildCapabilityManifest(tagTools, descriptions, alwaysActive, protected)
	loop.Tools().SetCapabilityTools(loop, manifest)
	loop.UseCapabilitySurface(manifest)
	return loop
}

func testProviderBackedGptOSSCatalog(t *testing.T) *fleet.Catalog {
	t.Helper()

	cfg := &config.Config{}
	cfg.Models.Resources = map[string]config.ModelServerConfig{
		"spark": {
			Provider: "ollama",
			URL:      "http://localhost:11434",
		},
	}
	cfg.Models.Default = "gpt-oss:20b"
	cfg.Models.Available = []config.ModelConfig{
		{
			Name:          "gpt-oss:20b",
			Resource:      "spark",
			SupportsTools: true,
			ContextWindow: 8192,
			Speed:         7,
			Quality:       7,
			CostTier:      0,
		},
	}

	cat, err := fleet.BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("BuildCatalog() error: %v", err)
	}
	return cat
}

func TestGptOSSProviderProfile_RecoversCapabilityActivationFromRawText(t *testing.T) {
	forgeToolCalled := false

	mock := &rawTextMockLLM{
		textProfile: llm.DefaultToolCallTextProfile(),
		responses: []*llm.ChatResponse{
			{
				Model: "gpt-oss:20b",
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"name":"forge_capability","arguments":{}}`,
				},
			},
			{
				Model: "gpt-oss:20b",
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"name":"forge_tool","arguments":{}}`,
				},
			},
			{
				Model:   "gpt-oss:20b",
				Message: llm.Message{Role: "assistant", Content: "Done."},
			},
		},
	}

	capTags := map[string]config.CapabilityTagConfig{
		"forge": {
			Description: "Forge tools",
			Tools:       []string{"forge_tool"},
		},
	}

	loop := setupCapabilityLoopWithClient(mock, []string{"forge_tool"}, capTags, "gpt-oss:20b")
	loop.usageCatalog = testProviderBackedGptOSSCatalog(t)
	loop.Tools().Register(&tools.Tool{
		Name:        "forge_tool",
		Description: "test forge tool",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Handler: func(_ context.Context, _ map[string]any) (string, error) {
			forgeToolCalled = true
			return "forge result", nil
		},
	})

	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "Activate forge and then use it."}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !forgeToolCalled {
		t.Fatal("forge_tool was not called after recovering raw-text capability activation")
	}
	if resp == nil || resp.Content == "" {
		t.Fatal("expected non-empty final response")
	}
	if len(mock.calls) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(mock.calls))
	}

	systemPrompt := ""
	for _, msg := range mock.calls[0].Messages {
		if msg.Role == "system" {
			systemPrompt = msg.Content
			break
		}
	}
	if systemPrompt == "" {
		t.Fatal("expected first call to include a system prompt")
	}
	if strings.Contains(systemPrompt, "## Tool Calling Contract") {
		t.Fatal("gpt-oss provider-backed profile should not inject the raw-text tool calling contract")
	}

	foundActivationResult := false
	for _, msg := range mock.calls[1].Messages {
		if msg.Role == "tool" && strings.Contains(msg.Content, "Capability **forge** activated.") {
			foundActivationResult = true
			break
		}
	}
	if !foundActivationResult {
		t.Fatalf("recovery call messages = %#v, want activate_capability tool result", mock.calls[1].Messages)
	}
}

func TestGptOSSProviderProfile_RecoversListLoadedCapabilitiesAliasFromRawText(t *testing.T) {
	mock := &rawTextMockLLM{
		textProfile: llm.DefaultToolCallTextProfile(),
		responses: []*llm.ChatResponse{
			{
				Model: "gpt-oss:20b",
				Message: llm.Message{
					Role:    "assistant",
					Content: `{"name":"list_capabilities","arguments":{}}`,
				},
			},
			{
				Model:   "gpt-oss:20b",
				Message: llm.Message{Role: "assistant", Content: "Loaded: ha."},
			},
		},
	}

	capTags := map[string]config.CapabilityTagConfig{
		"ha": {
			Description:  "Home Assistant tools",
			Tools:        []string{"get_state"},
			AlwaysActive: true,
		},
	}

	loop := setupCapabilityLoopWithClient(mock, []string{"get_state"}, capTags, "gpt-oss:20b")
	loop.usageCatalog = testProviderBackedGptOSSCatalog(t)

	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "What capability tags are loaded?"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if resp == nil || resp.Content == "" {
		t.Fatal("expected non-empty final response")
	}
	if len(mock.calls) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(mock.calls))
	}

	foundListResult := false
	for _, msg := range mock.calls[1].Messages {
		if msg.Role == "tool" && strings.Contains(msg.Content, `"loaded_capabilities"`) && strings.Contains(msg.Content, `"tag":"ha"`) {
			foundListResult = true
			break
		}
	}
	if !foundListResult {
		t.Fatalf("messages = %#v, want list_loaded_capabilities tool result", mock.calls[1].Messages)
	}
}
