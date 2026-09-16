package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/fleet/providers"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/talents"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

type freshnessSystemBlock struct {
	Type         string `json:"type"`
	Text         string `json:"text"`
	CacheControl *struct {
		Type string `json:"type"`
		TTL  string `json:"ttl"`
	} `json:"cache_control"`
}

// freshnessPayloadLLM exercises the real Anthropic serializer and cache
// guards. An already-canceled context prevents any network request, while
// the provider's trace contains the exact JSON it prepared to send.
type freshnessPayloadLLM struct {
	*mockLLM
	payloads []struct {
		System []freshnessSystemBlock `json:"system"`
	}
	systemMessages []llm.Message
}

func (m *freshnessPayloadLLM) ChatStream(ctx context.Context, model string, msgs []llm.Message, toolDefs []map[string]any, stream llm.StreamCallback) (*llm.ChatResponse, error) {
	var trace bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&trace, &slog.HandlerOptions{Level: llm.LevelTrace}))
	provider := providers.NewAnthropicClient("unused-test-key", slog.New(slog.NewTextHandler(io.Discard, nil)))
	providerCtx, cancel := context.WithCancel(logging.WithLogger(ctx, logger))
	cancel()
	if _, err := provider.ChatStream(providerCtx, model, msgs, toolDefs, stream); !errors.Is(err, context.Canceled) {
		return nil, fmt.Errorf("serialize canceled Anthropic request: got %v, want context.Canceled", err)
	}

	decoder := json.NewDecoder(&trace)
	found := false
	for decoder.More() {
		var entry struct {
			Message string `json:"msg"`
			JSON    string `json:"json"`
		}
		if err := decoder.Decode(&entry); err != nil {
			return nil, fmt.Errorf("decode Anthropic trace: %w", err)
		}
		if entry.Message != "request payload" {
			continue
		}
		var payload struct {
			System []freshnessSystemBlock `json:"system"`
		}
		if err := json.Unmarshal([]byte(entry.JSON), &payload); err != nil {
			return nil, fmt.Errorf("decode Anthropic payload: %w", err)
		}
		m.payloads = append(m.payloads, payload)
		found = true
	}
	if !found {
		return nil, errors.New("Anthropic trace did not contain a request payload")
	}
	msg := msgs[0]
	msg.Sections = append([]llm.PromptSection(nil), msg.Sections...)
	m.systemMessages = append(m.systemMessages, msg)
	return m.mockLLM.ChatStream(ctx, model, msgs, toolDefs, stream)
}

func TestPromptFreshness_AnthropicPayload(t *testing.T) {
	for _, custom := range []bool{false, true} {
		name := "generated"
		if custom {
			name = "custom"
		}
		t.Run(name, func(t *testing.T) {
			mock := &mockLLM{responses: []*llm.ChatResponse{
				{Model: "claude-sonnet-4-5", Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
					{ID: "activate", Function: struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					}{Name: "tag_activate", Arguments: map[string]any{"tag": "forge"}}},
					{ID: "advance", Function: struct {
						Name      string         `json:"name"`
						Arguments map[string]any `json:"arguments"`
					}{Name: "advance_state", Arguments: map[string]any{}}},
				}}},
				{Model: "claude-sonnet-4-5", Message: llm.Message{Role: "assistant", Content: "Done."}},
			}}
			client := &freshnessPayloadLLM{mockLLM: mock}
			capTags := map[string]config.CapabilityTagConfig{
				"base":  {Core: true, Tools: []string{"advance_state"}},
				"forge": {Tools: []string{"forge_tool"}},
			}
			loop := setupCapabilityLoop(mock, []string{"advance_state", "forge_tool"}, capTags)
			loop.llm = client
			loop.model = "claude-sonnet-4-5"
			// Exceed the provider's minimum cacheable prefix so the payload
			// test verifies retained 1h/5m markers, not just their absence.
			loop.persona = strings.Repeat("Stable persona guidance. ", 300)
			loop.SetCapabilityTags(capTags, []talents.Talent{
				{Name: "always", Tags: []string{talents.TagAlways}, Content: "STABLE_GUIDANCE"},
				{Name: "forge", Tags: []string{"forge"}, Content: "FORGE_GUIDANCE"},
			})
			live := &mockTagProvider{content: `{"state":"INITIAL_STATE"}`, bucket: agentctx.ContextBucketLiveState}
			loop.RegisterAlwaysContextProvider(live)
			loop.RegisterTagContextProvider("forge", &mockTagProvider{
				content: `{"forge_context":"ACTIVE_FORGE_CONTEXT"}`,
				bucket:  agentctx.ContextBucketTaggedGuidance,
			})
			loop.Tools().Register(&tools.Tool{
				Name:       "advance_state",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
				Handler: func(context.Context, map[string]any) (string, error) {
					live.content = `{"state":"UPDATED_STATE"}`
					return "updated", nil
				},
			})
			req := &Request{Messages: []Message{{Role: "user", Content: "activate forge and refresh the state"}}, MaxIterations: 2}
			if custom {
				req.SystemPrompt = "CALLER_ASSEMBLED_PROMPT"
			}
			resp, err := loop.Run(context.Background(), req, nil)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if resp.Content != "Done." || len(client.payloads) != 2 {
				t.Fatalf("content = %q, payloads = %d; want Done. and two requests", resp.Content, len(client.payloads))
			}
			if custom {
				if !strings.Contains(client.systemMessages[0].Content, req.SystemPrompt) {
					t.Fatal("initial message omitted the caller's custom prompt")
				}
				for i, payload := range client.payloads {
					found := false
					for _, block := range payload.System {
						if block.Text == req.SystemPrompt {
							found = true
							if block.CacheControl != nil {
								t.Errorf("request %d assigned a cache marker to caller-supplied text", i)
							}
						}
					}
					if !found {
						t.Errorf("request %d omitted custom prompt from Anthropic payload", i)
					}
				}
				if !reflect.DeepEqual(client.systemMessages[0], client.systemMessages[1]) || !reflect.DeepEqual(client.payloads[0], client.payloads[1]) {
					t.Fatal("custom system prompt changed between iterations")
				}
				return
			}

			var texts [2]string
			var stablePrefixes [2]string
			for i, payload := range client.payloads {
				var prefix strings.Builder
				for _, block := range payload.System {
					texts[i] += block.Text
					prefix.WriteString(block.Text)
					if block.CacheControl != nil && block.CacheControl.TTL == "1h" {
						stablePrefixes[i] = prefix.String()
					}
					if (strings.Contains(block.Text, "_STATE") || strings.Contains(block.Text, "ACTIVE_FORGE_CONTEXT")) && block.CacheControl != nil {
						t.Fatalf("request %d cached volatile provider context", i)
					}
				}
			}
			if !strings.Contains(texts[0], "INITIAL_STATE") || strings.Contains(texts[0], "FORGE_GUIDANCE") || strings.Contains(texts[0], "ACTIVE_FORGE_CONTEXT") {
				t.Fatal("first Anthropic payload did not reflect the initial tag/provider state")
			}
			for _, marker := range []string{"UPDATED_STATE", "FORGE_GUIDANCE", "ACTIVE_FORGE_CONTEXT"} {
				if !strings.Contains(texts[1], marker) {
					t.Errorf("second Anthropic payload missing %s", marker)
				}
			}
			if strings.Contains(texts[1], "INITIAL_STATE") || strings.Contains(texts[1], "**Context:**") {
				t.Error("second Anthropic payload retained stale state or pre-run context usage")
			}
			if stablePrefixes[0] == "" || stablePrefixes[0] != stablePrefixes[1] {
				t.Error("stable 1h cache prefix changed during volatile context refresh")
			}
			foundTaggedCache := false
			for _, block := range client.payloads[1].System {
				if strings.Contains(block.Text, "FORGE_GUIDANCE") && block.CacheControl != nil && block.CacheControl.TTL == "5m" {
					foundTaggedCache = true
				}
			}
			if !foundTaggedCache {
				t.Error("new tagged guidance lost its 5m cache boundary")
			}
		})
	}
}
