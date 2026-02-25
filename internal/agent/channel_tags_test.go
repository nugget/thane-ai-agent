package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/llm"
)

func TestChannelTags_ActivatedBySource(t *testing.T) {
	// When a request has hints["source"]="signal" and channel_tags maps
	// signal → [signal], the signal-tagged tools should be available.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Done."},
				InputTokens:  100,
				OutputTokens: 10,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"signal_send_reaction", "base_tool"})
	loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"signal": {
			Description: "Signal messaging",
			Tools:       []string{"signal_send_reaction"},
		},
		"base": {
			Description:  "Base tools",
			Tools:        []string{"base_tool"},
			AlwaysActive: true,
		},
	}, nil)
	loop.SetChannelTags(map[string][]string{
		"signal": {"signal"},
	})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "react to this"}},
		Hints:    map[string]string{"source": "signal"},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(mock.calls) < 1 {
		t.Fatal("expected at least 1 LLM call")
	}
	names := toolNames(mock.calls[0].Tools)
	if !hasName(names, "signal_send_reaction") {
		t.Errorf("signal_send_reaction should be available via channel tags: %v", names)
	}
	if !hasName(names, "base_tool") {
		t.Errorf("base_tool (always-active) should still be available: %v", names)
	}
}

func TestChannelTags_NotActivatedWithoutSource(t *testing.T) {
	// When no source hint is set, channel-specific tools should not appear.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Done."},
				InputTokens:  100,
				OutputTokens: 10,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"signal_send_reaction", "base_tool"})
	loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"signal": {
			Description: "Signal messaging",
			Tools:       []string{"signal_send_reaction"},
		},
		"base": {
			Description:  "Base tools",
			Tools:        []string{"base_tool"},
			AlwaysActive: true,
		},
	}, nil)
	loop.SetChannelTags(map[string][]string{
		"signal": {"signal"},
	})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "check the weather"}},
		// No source hint.
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(mock.calls) < 1 {
		t.Fatal("expected at least 1 LLM call")
	}
	names := toolNames(mock.calls[0].Tools)
	if hasName(names, "signal_send_reaction") {
		t.Errorf("signal_send_reaction should NOT appear without source hint: %v", names)
	}
	if !hasName(names, "base_tool") {
		t.Errorf("base_tool (always-active) should still be available: %v", names)
	}
}

func TestChannelTags_NoBleedBetweenRuns(t *testing.T) {
	// Channel tags activated in Run #1 (Signal) must not be visible
	// in Run #2 (no source).
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			// Run 1 response
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Signal reply."},
				InputTokens:  100,
				OutputTokens: 10,
			},
			// Run 2 response
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "API reply."},
				InputTokens:  100,
				OutputTokens: 10,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"signal_send_reaction", "base_tool"})
	loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"signal": {
			Description: "Signal messaging",
			Tools:       []string{"signal_send_reaction"},
		},
		"base": {
			Description:  "Base tools",
			Tools:        []string{"base_tool"},
			AlwaysActive: true,
		},
	}, nil)
	loop.SetChannelTags(map[string][]string{
		"signal": {"signal"},
	})

	// Run 1: Signal source
	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "signal message"}},
		Hints:    map[string]string{"source": "signal"},
	}, nil)
	if err != nil {
		t.Fatalf("Run 1 error: %v", err)
	}

	// Run 2: No source hint (API/web)
	_, err = loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "api message"}},
	}, nil)
	if err != nil {
		t.Fatalf("Run 2 error: %v", err)
	}

	if len(mock.calls) < 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(mock.calls))
	}

	// Run 1 should have signal tool.
	run1Names := toolNames(mock.calls[0].Tools)
	if !hasName(run1Names, "signal_send_reaction") {
		t.Errorf("Run 1: signal_send_reaction should be available: %v", run1Names)
	}

	// Run 2 should NOT have signal tool.
	run2Names := toolNames(mock.calls[1].Tools)
	if hasName(run2Names, "signal_send_reaction") {
		t.Errorf("Run 2: signal_send_reaction should NOT bleed over: %v", run2Names)
	}
}

func TestChannelTags_AdditiveWithAlwaysActive(t *testing.T) {
	// Channel-pinned tags are additive to always-active tags. Both sets
	// of tools should appear together.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Done."},
				InputTokens:  100,
				OutputTokens: 10,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"signal_tool", "ha_tool"})
	loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"signal": {
			Description: "Signal messaging",
			Tools:       []string{"signal_tool"},
		},
		"ha": {
			Description:  "Home Assistant",
			Tools:        []string{"ha_tool"},
			AlwaysActive: true,
		},
	}, nil)
	loop.SetChannelTags(map[string][]string{
		"signal": {"signal"},
	})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "test"}},
		Hints:    map[string]string{"source": "signal"},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(mock.calls) < 1 {
		t.Fatal("expected at least 1 LLM call")
	}
	names := toolNames(mock.calls[0].Tools)
	if !hasName(names, "signal_tool") {
		t.Errorf("signal_tool should be available via channel tag: %v", names)
	}
	if !hasName(names, "ha_tool") {
		t.Errorf("ha_tool should be available via always-active: %v", names)
	}
}

func TestChannelTags_UnknownChannel(t *testing.T) {
	// An unknown channel source should not cause errors — just no
	// additional tags activated.
	mock := &mockLLM{
		responses: []*llm.ChatResponse{
			{
				Model:        "test-model",
				Message:      llm.Message{Role: "assistant", Content: "Done."},
				InputTokens:  100,
				OutputTokens: 10,
			},
		},
	}

	loop := buildTestLoop(mock, []string{"signal_tool", "base_tool"})
	loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"signal": {
			Description: "Signal messaging",
			Tools:       []string{"signal_tool"},
		},
		"base": {
			Description:  "Base tools",
			Tools:        []string{"base_tool"},
			AlwaysActive: true,
		},
	}, nil)
	loop.SetChannelTags(map[string][]string{
		"signal": {"signal"},
	})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "test"}},
		Hints:    map[string]string{"source": "matrix"}, // unknown channel
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(mock.calls) < 1 {
		t.Fatal("expected at least 1 LLM call")
	}
	names := toolNames(mock.calls[0].Tools)
	if hasName(names, "signal_tool") {
		t.Errorf("signal_tool should NOT appear for unknown channel 'matrix': %v", names)
	}
	if !hasName(names, "base_tool") {
		t.Errorf("base_tool (always-active) should still be available: %v", names)
	}
}

func TestChannelTags_DropPinnedTagRejected(t *testing.T) {
	// Channel-pinned tags cannot be dropped via DropCapability.
	// Simulate the state that Run() creates when a channel source is set.
	loop := buildTestLoop(&mockLLM{}, nil)
	loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"signal": {
			Description: "Signal messaging",
			Tools:       []string{"signal_tool"},
		},
		"extra": {
			Description: "Extra tools",
			Tools:       []string{"extra_tool"},
		},
	}, nil)
	loop.SetChannelTags(map[string][]string{
		"signal": {"signal"},
	})

	// Simulate what Run() does: activate the channel tag and ref-count it.
	loop.tagMu.Lock()
	loop.activeTags["signal"] = true
	loop.channelPinnedTags["signal"] = 1
	// Also activate "extra" as an agent-requested tag (not channel-pinned).
	loop.activeTags["extra"] = true
	loop.tagMu.Unlock()

	// Dropping the channel-pinned tag should fail.
	err := loop.DropCapability("signal")
	if err == nil {
		t.Fatal("expected error when dropping channel-pinned tag")
	}
	if !strings.Contains(err.Error(), "channel-pinned") {
		t.Errorf("error should mention channel-pinned: %v", err)
	}

	// The tag should still be active.
	active := loop.ActiveTags()
	if !active["signal"] {
		t.Error("signal tag should still be active after rejected drop")
	}
}

func TestChannelTags_DropNonPinnedTagAllowed(t *testing.T) {
	// Tags that are active but not channel-pinned can still be dropped.
	loop := buildTestLoop(&mockLLM{}, nil)
	loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"signal": {
			Description: "Signal messaging",
			Tools:       []string{"signal_tool"},
		},
		"extra": {
			Description: "Extra tools",
			Tools:       []string{"extra_tool"},
		},
	}, nil)
	loop.SetChannelTags(map[string][]string{
		"signal": {"signal"},
	})

	// Simulate: "signal" is channel-pinned, "extra" is agent-requested.
	loop.tagMu.Lock()
	loop.activeTags["signal"] = true
	loop.channelPinnedTags["signal"] = 1
	loop.activeTags["extra"] = true
	loop.tagMu.Unlock()

	// Dropping the non-pinned tag should succeed.
	err := loop.DropCapability("extra")
	if err != nil {
		t.Fatalf("unexpected error dropping non-pinned tag: %v", err)
	}

	active := loop.ActiveTags()
	if active["extra"] {
		t.Error("extra tag should no longer be active after drop")
	}
	if !active["signal"] {
		t.Error("signal tag should still be active (channel-pinned)")
	}
}
