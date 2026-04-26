package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// setupCapabilityLoop builds a Loop with capability tags configured and the
// activate_capability / deactivate_capability / reset_capabilities tools
// registered. This mirrors the
// production wiring in cmd/thane/main.go.
func setupCapabilityLoop(mock *mockLLM, extraNames []string, capTags map[string]config.CapabilityTagConfig) *Loop {
	loop := buildTestLoop(mock, extraNames)
	loop.SetCapabilityTags(capTags, nil)

	// Build and register capability management tools, matching the
	// production wiring: loop.Tools().SetCapabilityTools(loop, manifest).
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

	return loop
}

// TestCapabilityActivation_MidLoop verifies that activate_capability
// activates a tag mid-loop and the newly-available tools can be called
// on the next iteration. This was broken when effectiveTools was
// snapshotted once before the loop (issue #507).
func TestCapabilityActivation_MidLoop(t *testing.T) {
	forgeToolCalled := false

	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// Iter 0: model requests the "forge" capability.
			{
				Model: "test-model",
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID: "call-req-cap",
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{
							Name:      "activate_capability",
							Arguments: map[string]any{"tag": "forge"},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 20,
			},
			// Iter 1: model calls forge_tool (should now be available).
			{
				Model: "test-model",
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID: "call-forge",
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{
							Name:      "forge_tool",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  200,
				OutputTokens: 20,
			},
			// Iter 2: text response.
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Done."},
				InputTokens:  300,
				OutputTokens: 10,
			},
		},
	}

	capTags := map[string]config.CapabilityTagConfig{
		"forge": {
			Description: "Forge tools",
			Tools:       []string{"forge_tool"},
		},
		"base": {
			Description:  "Base tools",
			Tools:        []string{"base_tool"},
			AlwaysActive: true,
		},
	}

	loop := setupCapabilityLoop(mock, []string{"forge_tool", "base_tool"}, capTags)

	// Override forge_tool handler to track invocation.
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
		Messages: []Message{{Role: "user", Content: "create an issue"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// forge_tool should have been executed successfully.
	if !forgeToolCalled {
		t.Error("forge_tool was not called; activate_capability did not activate the tag mid-loop")
	}

	// The response should not contain illegal-tool errors.
	if strings.Contains(resp.Content, "not available") {
		t.Errorf("response contains tool-unavailable error: %s", resp.Content)
	}

	// Verify forge_tool was NOT in iter 0 tool defs (before activation)
	// but WAS in iter 1 tool defs (after activation).
	if len(mock.calls) < 2 {
		t.Fatalf("expected at least 2 LLM calls, got %d", len(mock.calls))
	}
	iter0Tools := toolNames(mock.calls[0].Tools)
	if hasName(iter0Tools, "forge_tool") {
		t.Error("forge_tool should NOT be in iter 0 tool definitions (not yet activated)")
	}
	iter1Tools := toolNames(mock.calls[1].Tools)
	if !hasName(iter1Tools, "forge_tool") {
		t.Error("forge_tool should be in iter 1 tool definitions (activated by activate_capability)")
	}
}

func TestRunResponseSurfacesEffectiveToolsAndLoadedCapabilities(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "All set."},
				InputTokens:  100,
				OutputTokens: 12,
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

	loop := setupCapabilityLoop(mock, []string{"get_state"}, capTags)
	loop.UseCapabilitySurface(tools.BuildCapabilityManifest(
		map[string][]string{"ha": {"get_state"}},
		map[string]string{"ha": "Home Assistant tools"},
		map[string]bool{"ha": true},
		nil,
	))

	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "what can you do here?"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if !slices.Contains(resp.EffectiveTools, "get_state") {
		t.Fatalf("EffectiveTools = %#v, want get_state", resp.EffectiveTools)
	}
	for _, toolName := range []string{"activate_capability", "deactivate_capability", "reset_capabilities", "list_loaded_capabilities"} {
		if !slices.Contains(resp.EffectiveTools, toolName) {
			t.Fatalf("EffectiveTools = %#v, missing %q", resp.EffectiveTools, toolName)
		}
	}
	if !slices.Equal(resp.ActiveTags, []string{"ha"}) {
		t.Fatalf("ActiveTags = %#v, want [ha]", resp.ActiveTags)
	}
	if len(resp.LoadedCapabilities) != 1 || resp.LoadedCapabilities[0].Tag != "ha" {
		t.Fatalf("LoadedCapabilities = %#v, want ha entry", resp.LoadedCapabilities)
	}
}

func TestCapabilityActivation_DoesNotBleedActiveStateAcrossConversations(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model: "test-model",
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID: "call-req-cap",
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{
							Name:      "activate_capability",
							Arguments: map[string]any{"tag": "forge"},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 10,
			},
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Forge ready."},
				InputTokens:  120,
				OutputTokens: 10,
			},
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Fresh conversation."},
				InputTokens:  100,
				OutputTokens: 10,
			},
		},
	}

	capTags := map[string]config.CapabilityTagConfig{
		"forge": {
			Description: "Forge tools",
			Tools:       []string{"forge_tool"},
		},
	}

	loop := setupCapabilityLoop(mock, []string{"forge_tool"}, capTags)
	loop.UseCapabilitySurface(tools.BuildCapabilityManifest(
		map[string][]string{"forge": {"forge_tool"}},
		map[string]string{"forge": "Forge tools"},
		nil,
		nil,
	))
	loop.SetCapabilityTagStore(newTestCapStore(t))

	resp1, err := loop.Run(context.Background(), &Request{
		ConversationID: "conv-1",
		Messages:       []Message{{Role: "user", Content: "activate forge"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run conv-1 error: %v", err)
	}
	if !slices.Equal(resp1.ActiveTags, []string{"forge"}) {
		t.Fatalf("conv-1 ActiveTags = %#v, want [forge]", resp1.ActiveTags)
	}

	resp2, err := loop.Run(context.Background(), &Request{
		ConversationID: "conv-2",
		Messages:       []Message{{Role: "user", Content: "what can you do here?"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run conv-2 error: %v", err)
	}
	if len(resp2.ActiveTags) != 0 {
		t.Fatalf("conv-2 ActiveTags = %#v, want none", resp2.ActiveTags)
	}
	if len(resp2.LoadedCapabilities) != 0 {
		t.Fatalf("conv-2 LoadedCapabilities = %#v, want none", resp2.LoadedCapabilities)
	}
}

func TestRuntimeTags_PinForRunWithoutPersistence(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Reacted."},
				InputTokens:  100,
				OutputTokens: 10,
			},
		},
	}

	capTags := map[string]config.CapabilityTagConfig{
		"message_channel": {
			Description: "Current message channel",
			Tools:       []string{"send_reaction"},
			Protected:   true,
		},
	}

	loop := setupCapabilityLoop(mock, []string{"send_reaction"}, capTags)
	store := newTestCapStore(t)
	loop.SetCapabilityTagStore(store)

	resp, err := loop.Run(context.Background(), &Request{
		ConversationID: "signal-15551234567",
		RuntimeTags:    []string{"message_channel"},
		Messages:       []Message{{Role: "user", Content: "Thanks"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !slices.Equal(resp.ActiveTags, []string{"message_channel"}) {
		t.Fatalf("ActiveTags = %#v, want [message_channel]", resp.ActiveTags)
	}
	if names := toolNames(mock.calls[0].Tools); !hasName(names, "send_reaction") {
		t.Fatalf("send_reaction should be available via runtime tag: %v", names)
	}

	saved, err := store.LoadTags("signal-15551234567")
	if err != nil {
		t.Fatalf("LoadTags() error: %v", err)
	}
	if len(saved) != 0 {
		t.Fatalf("runtime tags should not persist, got %#v", saved)
	}
}

func TestCapabilityScope_InheritableTagsOnlyElective(t *testing.T) {
	scope := newCapabilityScope(map[string]config.CapabilityTagConfig{
		"core": {
			Description:  "Core tools",
			Tools:        []string{"remember_fact"},
			AlwaysActive: true,
		},
		"ha": {
			Description: "Home Assistant",
			Tools:       []string{"get_state"},
		},
		"message_channel": {
			Description: "Current message channel",
			Tools:       []string{"send_reaction"},
		},
	}, []string{"ops_lens"})

	if err := scope.Request("ha"); err != nil {
		t.Fatalf("Request(ha) error: %v", err)
	}
	scope.PinChannelTags([]string{"message_channel"})

	if got := scope.InheritableTags(); !slices.Equal(got, []string{"ha"}) {
		t.Fatalf("InheritableTags() = %#v, want [ha]", got)
	}
}

func TestCloseSession_NextRunStartsAtChannelBaseline(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model: "test-model",
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID: "call-req-cap",
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{
							Name:      "activate_capability",
							Arguments: map[string]any{"tag": "forge"},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 10,
			},
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Forge ready."},
				InputTokens:  120,
				OutputTokens: 10,
			},
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Fresh session."},
				InputTokens:  100,
				OutputTokens: 10,
			},
		},
	}

	capTags := map[string]config.CapabilityTagConfig{
		"forge": {
			Description: "Forge tools",
			Tools:       []string{"forge_tool"},
		},
		"web": {
			Description: "Web tools",
			Tools:       []string{"web_fetch", "web_search"},
		},
	}

	loop := setupCapabilityLoop(mock, []string{"forge_tool", "web_fetch", "web_search"}, capTags)
	loop.SetCapabilityTagStore(newTestCapStore(t))
	loop.SetChannelTags(map[string][]string{
		"signal": {"web"},
	})
	loop.memory = newMockMemWithCompaction()
	loop.archiver = &mockArchiver{activeID: "old-session"}

	resp1, err := loop.Run(context.Background(), &Request{
		ConversationID: "conv-1",
		Hints:          map[string]string{"source": "signal"},
		Messages:       []Message{{Role: "user", Content: "activate forge"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run conv-1 error: %v", err)
	}
	if !slices.Equal(resp1.ActiveTags, []string{"forge", "web"}) {
		t.Fatalf("conv-1 ActiveTags = %#v, want [forge web]", resp1.ActiveTags)
	}

	if err := loop.CloseSession("conv-1", "idle", "Carry this forward."); err != nil {
		t.Fatalf("CloseSession() error: %v", err)
	}

	resp2, err := loop.Run(context.Background(), &Request{
		ConversationID: "conv-1",
		Hints:          map[string]string{"source": "signal"},
		Messages:       []Message{{Role: "user", Content: "fresh session"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run conv-1 fresh-session error: %v", err)
	}
	if !slices.Equal(resp2.ActiveTags, []string{"web"}) {
		t.Fatalf("fresh session ActiveTags = %#v, want [web]", resp2.ActiveTags)
	}
}

// TestIllegalStrikes_NotResetByMetaTool verifies that the illegal strike
// counter is not reset by capability meta-tools (activate_capability,
// deactivate_capability), preventing infinite activate→blocked→activate loops.
func TestIllegalStrikes_NotResetByMetaTool(t *testing.T) {
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// Iter 0: model calls unavailable tool → strike 1.
			{
				Model: "test-model",
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID: "call-secret",
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{
							Name:      "secret_tool",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  100,
				OutputTokens: 20,
			},
			// Iter 1: model calls activate_capability (success, but meta-only
			// batch — should NOT reset strikes).
			{
				Model: "test-model",
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID: "call-req",
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{
							Name:      "activate_capability",
							Arguments: map[string]any{"tag": "base"},
						},
					}},
				},
				InputTokens:  200,
				OutputTokens: 20,
			},
			// Iter 2: model calls unavailable tool again → strike 2 → break.
			{
				Model: "test-model",
				Message: llm.Message{
					Role: "assistant",
					ToolCalls: []llm.ToolCall{{
						ID: "call-secret-2",
						Function: struct {
							Name      string         `json:"name"`
							Arguments map[string]any `json:"arguments"`
						}{
							Name:      "secret_tool",
							Arguments: map[string]any{},
						},
					}},
				},
				InputTokens:  300,
				OutputTokens: 20,
			},
			// Forced text recovery after illegal_tool break.
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "I cannot do that."},
				InputTokens:  400,
				OutputTokens: 10,
			},
		},
	}

	capTags := map[string]config.CapabilityTagConfig{
		"base": {
			Description:  "Base tools",
			Tools:        []string{"base_tool"},
			AlwaysActive: true,
		},
	}

	loop := setupCapabilityLoop(mock, []string{"base_tool"}, capTags)

	resp, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "use the secret tool"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// The loop should have broken after 3 tool iterations (not looped
	// indefinitely). With the old bug, strikes reset on
	// activate_capability success, allowing infinite loops.
	if len(mock.calls) > 4 {
		t.Errorf("expected at most 4 LLM calls (3 tool iters + 1 forced text), got %d", len(mock.calls))
	}

	// Verify we got the forced text recovery.
	if resp.Content != "I cannot do that." {
		t.Errorf("Content = %q, want forced recovery text", resp.Content)
	}
}
