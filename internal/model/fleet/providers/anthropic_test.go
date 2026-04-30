package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

func TestConvertToAnthropic(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello!"},
		{Role: "assistant", Content: "Hi there!"},
		{Role: "user", Content: "Turn on the lights."},
	}

	result, system := convertToAnthropic(messages)

	if system != "You are a helpful assistant." {
		t.Errorf("expected system prompt extracted, got %q", system)
	}

	if len(result) != 3 {
		t.Fatalf("expected 3 messages (no system), got %d", len(result))
	}

	if result[0].Role != "user" {
		t.Errorf("expected first message to be user, got %s", result[0].Role)
	}
}

func TestConvertToAnthropicWithToolCalls(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "You are a home assistant."},
		{Role: "user", Content: "Turn on lights."},
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "toolu_abc123",
				Function: struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				}{
					Name:      "control_device",
					Arguments: map[string]any{"entity": "light.kitchen"},
				},
			}},
		},
		{Role: "tool", Content: "Done.", ToolCallID: "toolu_abc123"},
	}

	result, system := convertToAnthropic(messages)

	if system != "You are a home assistant." {
		t.Errorf("unexpected system: %q", system)
	}

	if len(result) != 3 { // user, assistant with tool_use, user with tool_result
		t.Fatalf("expected 3 messages, got %d", len(result))
	}

	// Check assistant message has tool_use blocks
	assistantContent, ok := result[1].Content.([]anthropicContent)
	if !ok {
		t.Fatal("expected assistant content to be []anthropicContent")
	}
	if len(assistantContent) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(assistantContent))
	}
	if assistantContent[0].Type != "tool_use" {
		t.Errorf("expected tool_use block, got %s", assistantContent[0].Type)
	}
	if assistantContent[0].ID != "toolu_abc123" {
		t.Errorf("expected tool_use ID toolu_abc123, got %s", assistantContent[0].ID)
	}

	// Check tool result
	toolResultContent, ok := result[2].Content.([]anthropicContent)
	if !ok {
		t.Fatal("expected tool result content to be []anthropicContent")
	}
	if toolResultContent[0].Type != "tool_result" {
		t.Errorf("expected tool_result, got %s", toolResultContent[0].Type)
	}
	if toolResultContent[0].ToolUseID != "toolu_abc123" {
		t.Errorf("expected tool_use_id toolu_abc123, got %s", toolResultContent[0].ToolUseID)
	}
}

func TestConvertToolsToAnthropic(t *testing.T) {
	tools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "get_state",
				"description": "Get entity state",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"entity_id": map[string]any{
							"type":        "string",
							"description": "The entity ID",
						},
					},
					"required": []string{"entity_id"},
				},
			},
		},
	}

	result := convertToolsToAnthropic(tools)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	if result[0].Name != "get_state" {
		t.Errorf("expected tool name get_state, got %s", result[0].Name)
	}
	if result[0].Description != "Get entity state" {
		t.Errorf("expected description, got %s", result[0].Description)
	}
	if result[0].CacheControl == nil || result[0].CacheControl.TTL != "1h" {
		t.Fatalf("expected last tool to carry 1h cache control, got %+v", result[0].CacheControl)
	}
}

func TestConvertToolsToAnthropic_StripsTopLevelCompositionKeywords(t *testing.T) {
	tools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "notify_loop",
				"description": "Notify a loop",
				"parameters": map[string]any{
					"type": "object",
					"anyOf": []any{
						map[string]any{"required": []any{"loop_id"}},
						map[string]any{"required": []any{"name"}},
					},
					"properties": map[string]any{
						"loop_id": map[string]any{"type": "string"},
						"name":    map[string]any{"type": "string"},
						"duration": map[string]any{
							"anyOf": []any{
								map[string]any{"type": "string"},
								map[string]any{"type": "number"},
							},
						},
					},
				},
			},
		},
	}

	result := convertToolsToAnthropic(tools)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	schema, ok := result[0].InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("InputSchema type = %T, want map[string]any", result[0].InputSchema)
	}
	if _, ok := schema["anyOf"]; ok {
		t.Fatalf("top-level anyOf should be removed for Anthropic: %#v", schema)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", schema["properties"])
	}
	if _, ok := props["loop_id"]; !ok {
		t.Fatalf("loop_id missing from sanitized schema: %#v", props)
	}
	duration, ok := props["duration"].(map[string]any)
	if !ok {
		t.Fatalf("duration type = %T, want map[string]any", props["duration"])
	}
	if _, ok := duration["anyOf"]; !ok {
		t.Fatalf("nested anyOf should be preserved: %#v", duration)
	}
}

func TestConvertFromAnthropic(t *testing.T) {
	resp := &anthropicResponse{
		Model: "claude-opus-4-20250514",
		Role:  "assistant",
		Content: []anthropicContent{
			{Type: "text", Text: "I'll check that for you."},
			{
				Type:  "tool_use",
				ID:    "toolu_xyz789",
				Name:  "get_state",
				Input: map[string]any{"entity_id": "sun.sun"},
			},
		},
		StopReason: "tool_use",
	}

	result := convertFromAnthropic(resp)

	if result.Message.Content != "I'll check that for you." {
		t.Errorf("unexpected content: %q", result.Message.Content)
	}
	if len(result.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.Message.ToolCalls))
	}
	if result.Message.ToolCalls[0].ID != "toolu_xyz789" {
		t.Errorf("expected tool call ID toolu_xyz789, got %s", result.Message.ToolCalls[0].ID)
	}
	if result.Message.ToolCalls[0].Function.Name != "get_state" {
		t.Errorf("expected get_state, got %s", result.Message.ToolCalls[0].Function.Name)
	}
}

func TestAnthropicClientImplementsInterface(t *testing.T) {
	// Compile-time check that AnthropicClient implements Client
	var _ llm.Client = (*AnthropicClient)(nil)
}

func TestOllamaClientImplementsInterface(t *testing.T) {
	// Compile-time check that OllamaClient implements Client
	var _ llm.Client = (*OllamaClient)(nil)
}

func TestAnthropicRequestSerialization(t *testing.T) {
	req := anthropicRequest{
		Model:     "claude-opus-4-20250514",
		Messages:  []anthropicMessage{{Role: "user", Content: "test"}},
		System:    "You are helpful.",
		MaxTokens: 4096,
		Tools: []anthropicTool{{
			Name:        "test_tool",
			Description: "A test tool",
			InputSchema: map[string]any{"type": "object"},
		}},
		CacheControl: &anthropicCacheControl{Type: "ephemeral"},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	// Verify it deserializes back
	var decoded anthropicRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Model != req.Model {
		t.Errorf("model mismatch: %s vs %s", decoded.Model, req.Model)
	}
	decodedSystem, ok := decoded.System.(string)
	if !ok {
		t.Fatalf("decoded.System type = %T, want string", decoded.System)
	}
	if decodedSystem != req.System {
		t.Errorf("system mismatch: %s vs %s", decodedSystem, req.System)
	}
	if decoded.CacheControl == nil || decoded.CacheControl.Type != "ephemeral" {
		t.Fatalf("cache_control = %+v, want ephemeral", decoded.CacheControl)
	}
}

func TestAnthropicSystemBlocks_ApplyCacheBreakpoints(t *testing.T) {
	sections := []llm.PromptSection{
		{Name: "PERSONA", Content: "PERSONA", CacheTTL: "1h"},
		{Name: "RUNTIME CONTRACT", Content: "RUNTIME", CacheTTL: "1h"},
		{Name: "TALENTS TAGGED", Content: "TAGGED", CacheTTL: "5m"},
		{Name: "CURRENT CONDITIONS", Content: "NOW"},
	}

	blocks := anthropicSystemBlocks(sections)
	if len(blocks) != 4 {
		t.Fatalf("len(blocks) = %d, want 4", len(blocks))
	}
	if blocks[0].CacheControl != nil {
		t.Fatalf("first 1h block should not carry the breakpoint: %+v", blocks[0].CacheControl)
	}
	if blocks[1].CacheControl == nil || blocks[1].CacheControl.TTL != "1h" {
		t.Fatalf("second block should close the 1h cache run: %+v", blocks[1].CacheControl)
	}
	if blocks[2].CacheControl == nil || blocks[2].CacheControl.TTL != "5m" {
		t.Fatalf("tagged block should close the 5m cache run: %+v", blocks[2].CacheControl)
	}
	if blocks[3].CacheControl != nil {
		t.Fatalf("uncached tail should not carry cache control: %+v", blocks[3].CacheControl)
	}
}

func TestAnthropicSystemPayload_UsesPromptSectionsWhenPresent(t *testing.T) {
	messages := []llm.Message{
		{
			Role:    "system",
			Content: "opaque fallback",
			Sections: []llm.PromptSection{
				{Name: "PERSONA", Content: "PERSONA", CacheTTL: "1h"},
				{Name: "CURRENT CONDITIONS", Content: "NOW"},
			},
		},
	}

	payload := anthropicSystemPayload(messages, "opaque fallback")
	blocks, ok := payload.([]anthropicContent)
	if !ok {
		t.Fatalf("payload type = %T, want []anthropicContent", payload)
	}
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(blocks))
	}
	if blocks[0].CacheControl == nil || blocks[0].CacheControl.TTL != "1h" {
		t.Fatalf("expected cached persona block, got %+v", blocks[0].CacheControl)
	}
}

func TestAnthropicPromptCacheControl_NotSuppressedByToolCacheBreakpoints(t *testing.T) {
	tools := convertToolsToAnthropic([]map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        "get_state",
				"description": "Get entity state",
				"parameters":  map[string]any{"type": "object"},
			},
		},
	})
	if len(tools) != 1 || tools[0].CacheControl == nil {
		t.Fatalf("expected tool cache control, got %+v", tools)
	}

	systemPayload := anthropicSystemPayload([]llm.Message{{Role: "system", Content: "You are a helpful assistant."}}, "You are a helpful assistant.")
	explicit := anthropicUsesExplicitPromptCaching(systemPayload)
	if explicit {
		t.Fatal("plain-string system prompt should not count as explicit prompt caching")
	}

	ctrl := anthropicPromptCacheControl("You are a helpful assistant.", []anthropicMessage{{Role: "user", Content: "check"}}, tools, explicit)
	if ctrl == nil || ctrl.Type != "ephemeral" {
		t.Fatalf("request-level cache_control = %+v, want ephemeral", ctrl)
	}
}

func TestAnthropicPromptCacheControl_SuppressedBySectionCacheBreakpoints(t *testing.T) {
	systemPayload := anthropicSystemPayload([]llm.Message{
		{
			Role:    "system",
			Content: "fallback",
			Sections: []llm.PromptSection{
				{Name: "PERSONA", Content: "PERSONA", CacheTTL: "1h"},
			},
		},
	}, "fallback")

	explicit := anthropicUsesExplicitPromptCaching(systemPayload)
	if !explicit {
		t.Fatal("sectioned system prompt should count as explicit prompt caching")
	}

	ctrl := anthropicPromptCacheControl("fallback", []anthropicMessage{{Role: "user", Content: "check"}}, nil, explicit)
	if ctrl != nil {
		t.Fatalf("request-level cache_control = %+v, want nil when explicit system block caching is present", ctrl)
	}
}

func TestMinCacheablePrefixTokens(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"claude-sonnet-4-5", 1024},
		{"claude-sonnet-4-20250514", 1024},
		{"claude-opus-4-7", 4096},
		{"claude-haiku-4-5", 4096},
		{"unknown-model", 4096},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := minCacheablePrefixTokens(tt.model); got != tt.want {
				t.Errorf("minCacheablePrefixTokens(%q) = %d, want %d", tt.model, got, tt.want)
			}
		})
	}
}

// textBlock is a tiny helper for readable cache-guard test cases.
func textBlock(text string, ttl string) anthropicContent {
	b := anthropicContent{Type: "text", Text: text}
	if ttl != "" {
		b.CacheControl = &anthropicCacheControl{Type: "ephemeral", TTL: ttl}
	}
	return b
}

func TestApplyCacheBreakpointGuards_UnderMinimumRunsAreDropped(t *testing.T) {
	// Sonnet minimum is 1024 tokens (≈4096 chars). Build two blocks:
	// the first is way under; the second pushes the prefix over.
	blocks := []anthropicContent{
		textBlock(strings.Repeat("a", 400), "1h"),  // prefix ≈ 100 tokens — below min
		textBlock(strings.Repeat("b", 4000), "1h"), // prefix ≈ 1100 tokens — above min
	}
	applyCacheBreakpointGuards(blocks, nil, "claude-sonnet-4-5", slog.Default())

	if blocks[0].CacheControl != nil {
		t.Error("under-minimum run should have cache_control stripped")
	}
	if blocks[1].CacheControl == nil {
		t.Error("run above minimum should keep cache_control")
	}
}

func TestApplyCacheBreakpointGuards_CapsAtFourBreakpointsDroppingTool(t *testing.T) {
	// Four system breakpoints each above minimum, plus a tool cache.
	// Total would be 5; the tool cache should drop.
	body := strings.Repeat("x", 4100)
	blocks := []anthropicContent{
		textBlock(body, "1h"),
		textBlock(body, "1h"),
		textBlock(body, "1h"),
		textBlock(body, "1h"),
	}
	tools := []anthropicTool{
		{Name: "t", CacheControl: &anthropicCacheControl{Type: "ephemeral", TTL: "1h"}},
	}

	applyCacheBreakpointGuards(blocks, tools, "claude-sonnet-4-5", slog.Default())

	if tools[0].CacheControl != nil {
		t.Error("tool cache should be dropped when system fills the 4-breakpoint budget")
	}
	surviving := 0
	for _, b := range blocks {
		if b.CacheControl != nil {
			surviving++
		}
	}
	if surviving != 4 {
		t.Errorf("surviving system breakpoints = %d, want 4", surviving)
	}
}

func TestApplyCacheBreakpointGuards_DropsTrailingSystemWhenAboveCap(t *testing.T) {
	// Five system breakpoints all above minimum and no tools. The
	// trailing breakpoint should drop to fit the cap.
	body := strings.Repeat("x", 4100)
	blocks := []anthropicContent{
		textBlock(body, "1h"),
		textBlock(body, "1h"),
		textBlock(body, "1h"),
		textBlock(body, "1h"),
		textBlock(body, "1h"),
	}

	applyCacheBreakpointGuards(blocks, nil, "claude-sonnet-4-5", slog.Default())

	if blocks[4].CacheControl != nil {
		t.Error("trailing system breakpoint should drop when budget exhausted")
	}
	for i := 0; i < 4; i++ {
		if blocks[i].CacheControl == nil {
			t.Errorf("leading system breakpoint %d should survive", i)
		}
	}
}

func TestApplyCacheBreakpointGuards_AllowsTypicalThreeBreakpointConfig(t *testing.T) {
	// Two system runs + one tool is the current policy shape — well
	// under the cap, all above minimum. The guard should be a no-op.
	body := strings.Repeat("x", 5000)
	blocks := []anthropicContent{
		textBlock(body, "1h"),
		textBlock(body, "5m"),
	}
	tools := []anthropicTool{
		{Name: "t", CacheControl: &anthropicCacheControl{Type: "ephemeral", TTL: "1h"}},
	}

	applyCacheBreakpointGuards(blocks, tools, "claude-sonnet-4-5", slog.Default())

	for i, b := range blocks {
		if b.CacheControl == nil {
			t.Errorf("system block %d cache_control should remain", i)
		}
	}
	if tools[0].CacheControl == nil {
		t.Error("tool cache_control should remain when under the 4-breakpoint cap")
	}
}

func TestShouldUseAnthropicPromptCaching(t *testing.T) {
	tests := []struct {
		name   string
		system string
		msgs   []anthropicMessage
		tools  []anthropicTool
		want   bool
	}{
		{
			name:   "tool loop enables caching",
			system: "You are a tool-using assistant.",
			msgs:   []anthropicMessage{{Role: "user", Content: "check the state"}},
			tools:  []anthropicTool{{Name: "get_state", InputSchema: map[string]any{"type": "object"}}},
			want:   true,
		},
		{
			name:   "multi turn conversation enables caching",
			system: "You are a helpful assistant.",
			msgs: []anthropicMessage{
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "hi"},
				{Role: "user", Content: "what did I say?"},
			},
			want: true,
		},
		{
			name:   "large system prompt enables caching",
			system: strings.Repeat("x", 4096),
			msgs:   []anthropicMessage{{Role: "user", Content: "summarize this"}},
			want:   true,
		},
		{
			name:   "simple one shot request stays uncached",
			system: "You are a helpful assistant.",
			msgs:   []anthropicMessage{{Role: "user", Content: "hello"}},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldUseAnthropicPromptCaching(tt.system, tt.msgs, tt.tools)
			if got != tt.want {
				t.Fatalf("shouldUseAnthropicPromptCaching() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAnthropicClient_SetLogger_ReboundLoggerEmitsDebug(t *testing.T) {
	bootBuf := &bytes.Buffer{}
	bootLogger := slog.New(slog.NewJSONHandler(bootBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	c := NewAnthropicClient("k", bootLogger)

	if c.logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("bootstrap logger unexpectedly enabled at Debug")
	}
	c.logger.Debug("preflight", "stage", "boot")
	if bootBuf.Len() != 0 {
		t.Fatalf("bootstrap Info-level logger should drop Debug, got: %s", bootBuf.String())
	}

	prodBuf := &bytes.Buffer{}
	prodLogger := slog.New(slog.NewJSONHandler(prodBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	c.SetLogger(prodLogger)

	if !c.logger.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("rebound logger should be Debug-enabled")
	}
	c.logger.Debug("after rebind", "stage", "prod")
	if !strings.Contains(prodBuf.String(), `"msg":"after rebind"`) {
		t.Fatalf("expected Debug line after rebind, got: %s", prodBuf.String())
	}
	if !strings.Contains(prodBuf.String(), `"provider":"anthropic"`) {
		t.Fatalf("rebound logger lost provider attribute, got: %s", prodBuf.String())
	}
}

func TestAnthropicClient_SetLogger_NilGuards(t *testing.T) {
	var c *AnthropicClient
	c.SetLogger(slog.Default()) // must not panic on nil receiver

	c = NewAnthropicClient("k", slog.Default())
	before := c.logger
	c.SetLogger(nil)
	if c.logger != before {
		t.Fatal("nil logger argument must not replace existing logger")
	}
}

func TestConvertFromAnthropic_PopulatesStopReason(t *testing.T) {
	cases := []string{"end_turn", "tool_use", "max_tokens", "stop_sequence", "pause_turn"}
	for _, want := range cases {
		t.Run(want, func(t *testing.T) {
			resp := &anthropicResponse{
				Model:      "claude-opus-4-20250514",
				Role:       "assistant",
				Content:    []anthropicContent{{Type: "text", Text: "hi"}},
				StopReason: want,
			}
			out := convertFromAnthropic(resp)
			if out.StopReason != want {
				t.Errorf("StopReason = %q, want %q", out.StopReason, want)
			}
		})
	}
}

func TestAnthropicClient_HandleNonStreamingCapturesUpstreamRequestID(t *testing.T) {
	body := strings.NewReader(`{
		"id":"msg_01",
		"type":"message",
		"role":"assistant",
		"content":[{"type":"text","text":"hi"}],
		"model":"claude-opus-4-20250514",
		"stop_reason":"end_turn",
		"usage":{"input_tokens":10,"output_tokens":5}
	}`)
	c := NewAnthropicClient("k", slog.New(slog.NewTextHandler(io.Discard, nil)))

	resp, err := c.handleNonStreaming(context.Background(), body, "req_xyz789")
	if err != nil {
		t.Fatalf("handleNonStreaming: %v", err)
	}
	if resp.UpstreamRequestID != "req_xyz789" {
		t.Fatalf("UpstreamRequestID = %q, want %q", resp.UpstreamRequestID, "req_xyz789")
	}
}

func TestAnthropicClient_HandleStreamingCapturesUpstreamRequestID(t *testing.T) {
	// Minimal SSE stream: message_start with usage, then message_stop.
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_x","model":"claude-opus-4-20250514","usage":{"input_tokens":10,"output_tokens":0}}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
		``,
	}, "\n")

	c := NewAnthropicClient("k", slog.New(slog.NewTextHandler(io.Discard, nil)))
	resp, err := c.handleStreaming(context.Background(), strings.NewReader(stream), nil, "req_streamed_999")
	if err != nil {
		t.Fatalf("handleStreaming: %v", err)
	}
	if resp.UpstreamRequestID != "req_streamed_999" {
		t.Fatalf("UpstreamRequestID = %q, want %q", resp.UpstreamRequestID, "req_streamed_999")
	}
}

func TestAnthropicClient_HandleStreamingPropagatesStopReason(t *testing.T) {
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_x","model":"claude-opus-4-20250514","usage":{"input_tokens":10,"output_tokens":0}}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":5}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	c := NewAnthropicClient("k", slog.New(slog.NewTextHandler(io.Discard, nil)))
	resp, err := c.handleStreaming(context.Background(), strings.NewReader(stream), nil, "")
	if err != nil {
		t.Fatalf("handleStreaming: %v", err)
	}
	if resp.StopReason != "pause_turn" {
		t.Fatalf("StopReason = %q, want pause_turn", resp.StopReason)
	}
}

func TestAnthropicClient_HandleStreamingPreservesStopReasonAcrossMessageDeltas(t *testing.T) {
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_x","model":"claude-opus-4-20250514","usage":{"input_tokens":10,"output_tokens":0}}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":5}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{},"usage":{"output_tokens":9}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	c := NewAnthropicClient("k", slog.New(slog.NewTextHandler(io.Discard, nil)))
	resp, err := c.handleStreaming(context.Background(), strings.NewReader(stream), nil, "")
	if err != nil {
		t.Fatalf("handleStreaming: %v", err)
	}
	if resp.StopReason != "pause_turn" {
		t.Fatalf("StopReason = %q, want pause_turn", resp.StopReason)
	}
}

// TestAnthropicClient_HandleStreamingAggregatesUsageAcrossSSEEvents
// regression-locks how the streaming handler combines usage data from
// the message_start event (input/cache tokens) with the message_delta
// event (output_tokens). Anthropic's SSE schema reports input and
// cache numbers up front and the final output_tokens only at the end;
// any future refactor that drops one of those merges would silently
// undercount tokens in usage_records and skew cost reports.
func TestAnthropicClient_HandleStreamingAggregatesUsageAcrossSSEEvents(t *testing.T) {
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-20250514","usage":{"input_tokens":1234,"cache_creation_input_tokens":4096,"cache_read_input_tokens":8000,"cache_creation":{"ephemeral_5m_input_tokens":1000,"ephemeral_1h_input_tokens":3096}}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	c := NewAnthropicClient("k", slog.New(slog.NewTextHandler(io.Discard, nil)))
	resp, err := c.handleStreaming(context.Background(), strings.NewReader(stream), nil, "")
	if err != nil {
		t.Fatalf("handleStreaming: %v", err)
	}

	checks := []struct {
		field string
		got   int
		want  int
	}{
		{"InputTokens", resp.InputTokens, 1234},
		{"OutputTokens", resp.OutputTokens, 42},
		{"CacheCreationInputTokens", resp.CacheCreationInputTokens, 4096},
		{"CacheReadInputTokens", resp.CacheReadInputTokens, 8000},
		{"CacheCreation5mInputTokens", resp.CacheCreation5mInputTokens, 1000},
		{"CacheCreation1hInputTokens", resp.CacheCreation1hInputTokens, 3096},
	}
	for _, chk := range checks {
		if chk.got != chk.want {
			t.Errorf("%s = %d, want %d", chk.field, chk.got, chk.want)
		}
	}
	if resp.Message.Content != "hi" {
		t.Errorf("Content = %q, want %q", resp.Message.Content, "hi")
	}
}

// TestAnthropicClient_HandleStreamingMessageDeltaWithoutUsage protects
// against a hypothetical regression where message_delta arriving
// without a usage object overwrites the input/cache totals captured
// from message_start. Anthropic always sends the final output_tokens
// in message_delta, but the surrounding usage struct is optional and
// may be absent even in valid streams; the handler must keep the
// message_start numbers regardless.
func TestAnthropicClient_HandleStreamingMessageDeltaWithoutUsage(t *testing.T) {
	stream := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-20250514","usage":{"input_tokens":500,"cache_read_input_tokens":2000}}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")

	c := NewAnthropicClient("k", slog.New(slog.NewTextHandler(io.Discard, nil)))
	resp, err := c.handleStreaming(context.Background(), strings.NewReader(stream), nil, "")
	if err != nil {
		t.Fatalf("handleStreaming: %v", err)
	}
	if resp.InputTokens != 500 {
		t.Errorf("InputTokens = %d, want 500 (must not be reset by message_delta with no usage)", resp.InputTokens)
	}
	if resp.CacheReadInputTokens != 2000 {
		t.Errorf("CacheReadInputTokens = %d, want 2000", resp.CacheReadInputTokens)
	}
}

func TestApplyCacheBreakpointGuards_ReturnsUnderMinimumDrops(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Two short blocks with breakpoints; opus minimum is 4096 tokens
	// (~16384 chars). Both runs sit well below the threshold.
	blocks := []anthropicContent{
		{Type: "text", Text: strings.Repeat("a", 1000), CacheControl: &anthropicCacheControl{Type: "ephemeral", TTL: "1h"}},
		{Type: "text", Text: strings.Repeat("b", 500), CacheControl: &anthropicCacheControl{Type: "ephemeral", TTL: "5m"}},
	}
	tools := []anthropicTool{}

	drops := applyCacheBreakpointGuards(blocks, tools, "claude-opus-4-20250514", logger)

	if len(drops) != 2 {
		t.Fatalf("drops = %d, want 2", len(drops))
	}
	for _, d := range drops {
		if d.Reason != "under_minimum_prefix" {
			t.Errorf("drop reason = %q, want under_minimum_prefix", d.Reason)
		}
		if d.Scope != "system" {
			t.Errorf("drop scope = %q, want system", d.Scope)
		}
	}
	if blocks[0].CacheControl != nil || blocks[1].CacheControl != nil {
		t.Fatalf("expected breakpoints stripped from both blocks: %+v", blocks)
	}
}

func TestApplyCacheBreakpointGuards_ReturnsToolCapDrop(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Five system breakpoints plus a tool breakpoint = 6 total. Under
	// the cap policy the tool drop fires first; an additional system
	// drop brings us to 4. Use opus-sized blocks (>16k chars each) so
	// the under-minimum guard doesn't pre-drop them.
	long := strings.Repeat("x", 20000)
	blocks := []anthropicContent{
		{Type: "text", Text: long, CacheControl: &anthropicCacheControl{Type: "ephemeral", TTL: "1h"}},
		{Type: "text", Text: long, CacheControl: &anthropicCacheControl{Type: "ephemeral", TTL: "1h"}},
		{Type: "text", Text: long, CacheControl: &anthropicCacheControl{Type: "ephemeral", TTL: "1h"}},
		{Type: "text", Text: long, CacheControl: &anthropicCacheControl{Type: "ephemeral", TTL: "1h"}},
		{Type: "text", Text: long, CacheControl: &anthropicCacheControl{Type: "ephemeral", TTL: "5m"}},
	}
	tools := []anthropicTool{
		{Name: "first"},
		{Name: "last", CacheControl: &anthropicCacheControl{Type: "ephemeral", TTL: "1h"}},
	}

	drops := applyCacheBreakpointGuards(blocks, tools, "claude-opus-4-20250514", logger)

	if len(drops) < 1 {
		t.Fatalf("drops = %d, want at least 1", len(drops))
	}
	// First drop should be the tool breakpoint per the policy.
	if drops[0].Reason != "over_cap_tool_breakpoint" || drops[0].Scope != "tools" {
		t.Errorf("first drop = %+v, want over_cap_tool_breakpoint/tools", drops[0])
	}
	if tools[1].CacheControl != nil {
		t.Errorf("tool breakpoint should be stripped: %+v", tools[1].CacheControl)
	}
	// Trailing system drops (if any) should follow with the right reason.
	for _, d := range drops[1:] {
		if d.Reason != "over_cap_trailing_system" {
			t.Errorf("subsequent drop reason = %q, want over_cap_trailing_system", d.Reason)
		}
	}
}

func TestParseRateLimitHeaders_AllFieldsPresent(t *testing.T) {
	h := http.Header{}
	h.Set("anthropic-ratelimit-requests-limit", "5000")
	h.Set("anthropic-ratelimit-requests-remaining", "4999")
	h.Set("anthropic-ratelimit-requests-reset", "2026-04-30T08:00:00Z")
	h.Set("anthropic-ratelimit-tokens-limit", "1000000")
	h.Set("anthropic-ratelimit-tokens-remaining", "950000")
	h.Set("anthropic-ratelimit-input-tokens-limit", "800000")
	h.Set("anthropic-ratelimit-input-tokens-remaining", "750000")
	h.Set("anthropic-ratelimit-output-tokens-limit", "200000")
	h.Set("anthropic-ratelimit-output-tokens-remaining", "199000")
	h.Set("retry-after", "30")

	now := time.Date(2026, 4, 30, 7, 30, 0, 0, time.UTC)
	snap := parseRateLimitHeaders(h, "req_xyz", now)

	if snap == nil {
		t.Fatal("parseRateLimitHeaders returned nil with headers present")
	}
	if snap.UpstreamRequestID != "req_xyz" {
		t.Errorf("UpstreamRequestID = %q, want req_xyz", snap.UpstreamRequestID)
	}
	if snap.RequestsRemaining != 4999 {
		t.Errorf("RequestsRemaining = %d, want 4999", snap.RequestsRemaining)
	}
	if snap.TokensLimit != 1000000 {
		t.Errorf("TokensLimit = %d, want 1000000", snap.TokensLimit)
	}
	if snap.InputTokensRemaining != 750000 {
		t.Errorf("InputTokensRemaining = %d, want 750000", snap.InputTokensRemaining)
	}
	if snap.OutputTokensRemaining != 199000 {
		t.Errorf("OutputTokensRemaining = %d, want 199000", snap.OutputTokensRemaining)
	}
	if snap.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", snap.RetryAfter)
	}
	if !snap.CapturedAt.Equal(now) {
		t.Errorf("CapturedAt = %v, want %v", snap.CapturedAt, now)
	}
}

func TestParseRateLimitHeaders_NoneMeansNil(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	if snap := parseRateLimitHeaders(h, "req_xyz", time.Now()); snap != nil {
		t.Fatalf("expected nil when no rate-limit headers, got %+v", snap)
	}
}

func TestAnthropicClient_RateLimitSnapshot_StoreReturnsCopy(t *testing.T) {
	c := NewAnthropicClient("k", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if got := c.RateLimitSnapshot(); got != nil {
		t.Fatalf("expected nil before any response captured, got %+v", got)
	}

	original := &RateLimitSnapshot{
		CapturedAt:        time.Now(),
		UpstreamRequestID: "req_a",
		RequestsRemaining: 4000,
	}
	c.storeRateLimitSnapshot(original)

	snap := c.RateLimitSnapshot()
	if snap == nil {
		t.Fatal("RateLimitSnapshot returned nil after store")
	}
	if snap.RequestsRemaining != 4000 {
		t.Errorf("RequestsRemaining = %d, want 4000", snap.RequestsRemaining)
	}
	// Verify we got a copy, not the original pointer — a downstream
	// mutation must not race with future stores.
	snap.RequestsRemaining = 0
	if c.RateLimitSnapshot().RequestsRemaining != 4000 {
		t.Fatal("RateLimitSnapshot returned shared pointer; expected defensive copy")
	}
}

func TestAnthropicClient_PingCapturesRateLimitSnapshot(t *testing.T) {
	c := NewAnthropicClient("k", slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		h := http.Header{}
		h.Set("x-request-id", "req_ping")
		h.Set("anthropic-ratelimit-requests-remaining", "123")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     h,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    req,
		}, nil
	})}

	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}
	snap := c.RateLimitSnapshot()
	if snap == nil {
		t.Fatal("RateLimitSnapshot returned nil after Ping response")
	}
	if snap.UpstreamRequestID != "req_ping" {
		t.Errorf("UpstreamRequestID = %q, want req_ping", snap.UpstreamRequestID)
	}
	if snap.RequestsRemaining != 123 {
		t.Errorf("RequestsRemaining = %d, want 123", snap.RequestsRemaining)
	}
}

func TestLogRateLimitSnapshot_OmitsMissingResetFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logRateLimitSnapshot(logger, &RateLimitSnapshot{
		UpstreamRequestID: "req_missing_resets",
		RequestsLimit:     5000,
		RequestsRemaining: 4999,
	})

	got := buf.String()
	if strings.Contains(got, "0001-01-01T00:00:00Z") {
		t.Fatalf("zero time leaked into rate-limit log: %s", got)
	}
	for _, absent := range []string{
		"requests_reset",
		"tokens_reset",
		"input_tokens_reset",
		"output_tokens_reset",
		"retry_after",
	} {
		if strings.Contains(got, absent) {
			t.Errorf("log should omit %q when missing/zero: %s", absent, got)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
