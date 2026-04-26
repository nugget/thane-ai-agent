package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

type sessionOriginContextView struct {
	Origin      SessionOrigin              `json:"origin"`
	Applied     []SessionOriginAppliedRule `json:"applied_rules,omitempty"`
	Tags        []string                   `json:"tags,omitempty"`
	ContextRefs []string                   `json:"context_refs,omitempty"`
}

func parseSessionOriginContext(t *testing.T, prompt string) sessionOriginContextView {
	t.Helper()
	sectionStart := strings.Index(prompt, "## Session Origin Context")
	if sectionStart < 0 {
		t.Fatalf("prompt missing Session Origin Context:\n%s", prompt)
	}
	blockStart := strings.Index(prompt[sectionStart:], "```json\n")
	if blockStart < 0 {
		t.Fatalf("session origin context missing JSON block:\n%s", prompt[sectionStart:])
	}
	blockStart = sectionStart + blockStart + len("```json\n")
	blockEnd := strings.Index(prompt[blockStart:], "\n```")
	if blockEnd < 0 {
		t.Fatalf("session origin context has unterminated JSON block:\n%s", prompt[blockStart:])
	}

	var payload sessionOriginContextView
	if err := json.Unmarshal([]byte(prompt[blockStart:blockStart+blockEnd]), &payload); err != nil {
		t.Fatalf("parse session origin context: %v\n%s", err, prompt[blockStart:blockStart+blockEnd])
	}
	return payload
}

func appliedRuleBySource(rules []SessionOriginAppliedRule, source string) (SessionOriginAppliedRule, bool) {
	for _, rule := range rules {
		if rule.Source == source {
			return rule, true
		}
	}
	return SessionOriginAppliedRule{}, false
}

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
	// Channel-pinned tags cannot be dropped via capabilityScope.Drop.
	capTags := map[string]config.CapabilityTagConfig{
		"signal": {Description: "Signal messaging", Tools: []string{"signal_tool"}},
		"extra":  {Description: "Extra tools", Tools: []string{"extra_tool"}},
	}
	scope := newCapabilityScope(capTags, nil)
	scope.PinChannelTags([]string{"signal"})
	_ = scope.Request("extra") // agent-requested, not pinned

	ctx := withCapabilityScope(context.Background(), scope)

	// Dropping the channel-pinned tag should fail.
	loop := buildTestLoop(&mockLLM{}, nil)
	loop.SetCapabilityTags(capTags, nil)
	err := loop.DropCapability(ctx, "signal")
	if err == nil {
		t.Fatal("expected error when dropping channel-pinned tag")
	}
	if !strings.Contains(err.Error(), "channel-pinned") {
		t.Errorf("error should mention channel-pinned: %v", err)
	}

	// The tag should still be active.
	active := scope.Snapshot()
	if !active["signal"] {
		t.Error("signal tag should still be active after rejected drop")
	}
}

func TestChannelTags_DropNonPinnedTagAllowed(t *testing.T) {
	// Tags that are active but not channel-pinned can still be dropped.
	capTags := map[string]config.CapabilityTagConfig{
		"signal": {Description: "Signal messaging", Tools: []string{"signal_tool"}},
		"extra":  {Description: "Extra tools", Tools: []string{"extra_tool"}},
	}
	scope := newCapabilityScope(capTags, nil)
	scope.PinChannelTags([]string{"signal"})
	_ = scope.Request("extra") // agent-requested, not pinned

	ctx := withCapabilityScope(context.Background(), scope)

	// Dropping the non-pinned tag should succeed.
	loop := buildTestLoop(&mockLLM{}, nil)
	loop.SetCapabilityTags(capTags, nil)
	err := loop.DropCapability(ctx, "extra")
	if err != nil {
		t.Fatalf("unexpected error dropping non-pinned tag: %v", err)
	}

	active := scope.Snapshot()
	if active["extra"] {
		t.Error("extra tag should no longer be active after drop")
	}
	if !active["signal"] {
		t.Error("signal tag should still be active (channel-pinned)")
	}
}

func TestProtectedTags_CannotBeActivatedManually(t *testing.T) {
	scope := newCapabilityScope(map[string]config.CapabilityTagConfig{
		"owner": {
			Description: "Owner-scoped privileged tools.",
			Protected:   true,
		},
		"message_channel": {
			Description: "Current message channel.",
			Protected:   true,
		},
	}, nil)

	for _, tag := range []string{"owner", "message_channel"} {
		err := scope.Request(tag)
		if err == nil {
			t.Fatalf("expected protected tag %q activation to fail", tag)
		}
		if !strings.Contains(err.Error(), "protected tag") {
			t.Fatalf("error = %v, want protected-tag message", err)
		}
	}
}

func TestProtectedOwnerTag_ActivatedByChannelBinding(t *testing.T) {
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

	loop := buildTestLoop(mock, []string{"owner_tool", "base_tool"})
	loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"owner": {
			Description: "Owner-scoped privileged tools.",
			Tools:       []string{"owner_tool"},
			Protected:   true,
		},
		"base": {
			Description:  "Base tools",
			Tools:        []string{"base_tool"},
			AlwaysActive: true,
		},
	}, nil)

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "owner request"}},
		ChannelBinding: &memory.ChannelBinding{
			Channel: "owu",
			IsOwner: true,
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(mock.calls) < 1 {
		t.Fatal("expected at least 1 LLM call")
	}
	names := toolNames(mock.calls[0].Tools)
	if !hasName(names, "owner_tool") {
		t.Fatalf("owner_tool should be available for owner binding: %v", names)
	}
}

func TestContactOrigin_ActivatedBySignalContact(t *testing.T) {
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

	loop := buildTestLoop(mock, []string{"signal_tool", "project_tool"})
	loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"signal": {
			Description: "Signal messaging",
			Tools:       []string{"signal_tool"},
		},
		"projects": {
			Description: "Project context",
			Tools:       []string{"project_tool"},
		},
	}, nil)
	loop.UseContactLookup(&mockContactLookup{
		byID: map[string]*ContactContext{
			"contact-1": {
				ID:        "contact-1",
				Name:      "David McNett",
				TrustZone: "admin",
				TrustPolicy: &TrustPolicyView{
					FrontierModel:     true,
					ProactiveOutreach: "full",
					ToolAccess:        "unrestricted",
					SendGating:        "allowed",
				},
				Summary: "Prefers contact identity from the structured directory.",
			},
		},
		policies: map[string]*ContactOriginPolicy{
			"contact-1": {
				Tags: []string{"signal", "projects"},
			},
		},
	})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "what needs attention?"}},
		Hints:    map[string]string{"source": "signal"},
		ChannelBinding: &memory.ChannelBinding{
			Channel:     "signal",
			Address:     "+15551234567",
			ContactID:   "contact-1",
			ContactName: "David McNett",
			TrustZone:   "trusted",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	names := toolNames(mock.calls[0].Tools)
	for _, want := range []string{"signal_tool", "project_tool"} {
		if !hasName(names, want) {
			t.Fatalf("%s should be available via session origin policy: %v", want, names)
		}
	}
	systemPrompt := mock.calls[0].Messages[0].Content
	for _, want := range []string{"Session Origin Context", "contact_origin", "Prefers contact identity from the structured directory."} {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("system prompt should include origin/contact context %q, got:\n%s", want, systemPrompt)
		}
	}
	if strings.Contains(systemPrompt, "kb:people") {
		t.Fatalf("people identity should come from contact context, got:\n%s", systemPrompt)
	}
}

func TestContactOrigin_MissingIdentityFallsBackToChannelTags(t *testing.T) {
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

	loop := buildTestLoop(mock, []string{"signal_tool", "project_tool"})
	loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"signal": {
			Description: "Signal messaging",
			Tools:       []string{"signal_tool"},
		},
		"projects": {
			Description: "Project context",
			Tools:       []string{"project_tool"},
		},
	}, nil)
	loop.SetChannelTags(map[string][]string{
		"signal": {"signal"},
	})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "check signal updates"}},
		Hints:    map[string]string{"source": "signal"},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	names := toolNames(mock.calls[0].Tools)
	if !hasName(names, "signal_tool") {
		t.Fatalf("signal_tool should remain available through channel_tags fallback: %v", names)
	}
	if hasName(names, "project_tool") {
		t.Fatalf("project_tool should not be available without matching contact identity: %v", names)
	}
}

func TestContactOrigin_ProtectedTagsRequireTrustedBinding(t *testing.T) {
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

	loop := buildTestLoop(mock, []string{"owner_tool", "signal_tool"})
	loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"owner": {
			Description: "Owner-scoped privileged tools.",
			Tools:       []string{"owner_tool"},
			Protected:   true,
		},
		"signal": {
			Description: "Signal messaging",
			Tools:       []string{"signal_tool"},
		},
	}, nil)
	loop.SetChannelTags(map[string][]string{
		"signal": {"owner", "signal"},
	})
	loop.UseContactLookup(&mockContactLookup{
		policies: map[string]*ContactOriginPolicy{
			"David": {
				Tags: []string{"owner"},
			},
		},
	})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "check privileged tools"}},
		Hints:    map[string]string{"source": "signal"},
		ChannelBinding: &memory.ChannelBinding{
			Channel:     "signal",
			ContactName: "David",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	names := toolNames(mock.calls[0].Tools)
	if hasName(names, "owner_tool") {
		t.Fatalf("owner_tool should not be available from contact policy without owner binding: %v", names)
	}
	if !hasName(names, "signal_tool") {
		t.Fatalf("signal_tool should still be available: %v", names)
	}
}

func TestSessionOrigin_RuntimeOnlyTagsRequireTrustedRuntimeSource(t *testing.T) {
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

	loop := buildTestLoop(mock, []string{"owner_tool", "send_reaction", "signal_tool", "project_tool", "protected_tool"})
	loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"owner": {
			Description: "Owner-scoped privileged tools.",
			Tools:       []string{"owner_tool"},
			Protected:   true,
		},
		"message_channel": {
			Description: "Current message channel.",
			Tools:       []string{"send_reaction"},
			Protected:   true,
		},
		"signal": {
			Description: "Signal messaging.",
			Tools:       []string{"signal_tool"},
		},
		"projects": {
			Description: "Project context.",
			Tools:       []string{"project_tool"},
		},
		"protected_custom": {
			Description: "Future protected runtime surface.",
			Tools:       []string{"protected_tool"},
			Protected:   true,
		},
	}, nil)
	loop.SetChannelTags(map[string][]string{
		"signal": {"signal", "message_channel", "owner", "protected_custom"},
	})
	loop.UseContactLookup(&mockContactLookup{
		policies: map[string]*ContactOriginPolicy{
			"David": {
				Tags: []string{"projects", "message_channel", "owner", "protected_custom"},
			},
		},
	})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "check available surfaces"}},
		Hints:    map[string]string{"source": "signal"},
		ChannelBinding: &memory.ChannelBinding{
			Channel:     "signal",
			ContactName: "David",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	names := toolNames(mock.calls[0].Tools)
	for _, want := range []string{"signal_tool", "project_tool"} {
		if !hasName(names, want) {
			t.Fatalf("%s should be available from source/contact policy: %v", want, names)
		}
	}
	for _, unwanted := range []string{"send_reaction", "owner_tool", "protected_tool"} {
		if hasName(names, unwanted) {
			t.Fatalf("%s should not be available from source/contact policy: %v", unwanted, names)
		}
	}

	originCtx := parseSessionOriginContext(t, mock.calls[0].Messages[0].Content)
	for _, unwanted := range []string{"message_channel", "owner", "protected_custom"} {
		if containsString(originCtx.Tags, unwanted) {
			t.Fatalf("origin tags = %#v, should not include runtime-only %s", originCtx.Tags, unwanted)
		}
	}
	if rule, ok := appliedRuleBySource(originCtx.Applied, "channel_tags"); !ok {
		t.Fatalf("origin applied rules missing channel_tags: %#v", originCtx.Applied)
	} else if !containsString(rule.Tags, "signal") || containsString(rule.Tags, "message_channel") || containsString(rule.Tags, "owner") || containsString(rule.Tags, "protected_custom") {
		t.Fatalf("channel_tags rule = %#v, want only broad source tags", rule)
	}
	if rule, ok := appliedRuleBySource(originCtx.Applied, "contacts"); !ok {
		t.Fatalf("origin applied rules missing contacts: %#v", originCtx.Applied)
	} else if !containsString(rule.Tags, "projects") || containsString(rule.Tags, "message_channel") || containsString(rule.Tags, "owner") || containsString(rule.Tags, "protected_custom") {
		t.Fatalf("contacts rule = %#v, want only contact-origin optional tags", rule)
	}
}

func TestSessionOrigin_CombinesRuntimeContactSourceAndOwnerBinding(t *testing.T) {
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

	loop := buildTestLoop(mock, []string{"owner_tool", "send_reaction", "signal_tool", "project_tool"})
	loop.SetCapabilityTags(map[string]config.CapabilityTagConfig{
		"owner": {
			Description: "Owner-scoped privileged tools.",
			Tools:       []string{"owner_tool"},
			Protected:   true,
		},
		"message_channel": {
			Description: "Current message channel.",
			Tools:       []string{"send_reaction"},
			Protected:   true,
		},
		"signal": {
			Description: "Signal messaging.",
			Tools:       []string{"signal_tool"},
		},
		"projects": {
			Description: "Project context.",
			Tools:       []string{"project_tool"},
		},
	}, nil)
	loop.SetChannelTags(map[string][]string{
		"signal": {"signal", "message_channel"},
	})
	loop.UseContactLookup(&mockContactLookup{
		byID: map[string]*ContactContext{
			"contact-1": {
				ID:        "contact-1",
				Name:      "David",
				TrustZone: "admin",
			},
		},
		policies: map[string]*ContactOriginPolicy{
			"contact-1": {
				Tags:        []string{"projects", "message_channel", "owner"},
				ContextRefs: []string{"kb:projects/current.md"},
			},
		},
	})

	_, err := loop.Run(context.Background(), &Request{
		Messages:    []Message{{Role: "user", Content: "check all origin layers"}},
		Hints:       map[string]string{"source": "signal"},
		RuntimeTags: []string{"message_channel"},
		ChannelBinding: &memory.ChannelBinding{
			Channel:     "signal",
			ContactID:   "contact-1",
			ContactName: "David",
			IsOwner:     true,
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	names := toolNames(mock.calls[0].Tools)
	for _, want := range []string{"owner_tool", "send_reaction", "signal_tool", "project_tool"} {
		if !hasName(names, want) {
			t.Fatalf("%s should be available from the combined production origin shape: %v", want, names)
		}
	}

	originCtx := parseSessionOriginContext(t, mock.calls[0].Messages[0].Content)
	if !containsString(originCtx.Tags, "message_channel") || !containsString(originCtx.Tags, "owner") ||
		!containsString(originCtx.Tags, "signal") || !containsString(originCtx.Tags, "projects") {
		t.Fatalf("origin tags = %#v, want runtime, owner binding, source, and contact tags", originCtx.Tags)
	}
	if !containsString(originCtx.ContextRefs, "kb:projects/current.md") {
		t.Fatalf("origin context refs = %#v, want exact contact ref", originCtx.ContextRefs)
	}
	if rule, ok := appliedRuleBySource(originCtx.Applied, "runtime"); !ok || !containsString(rule.Tags, "message_channel") {
		t.Fatalf("runtime rule = %#v, ok=%v, want message_channel", rule, ok)
	}
	if rule, ok := appliedRuleBySource(originCtx.Applied, "channel_tags"); !ok {
		t.Fatalf("origin applied rules missing channel_tags: %#v", originCtx.Applied)
	} else if !containsString(rule.Tags, "signal") || containsString(rule.Tags, "message_channel") {
		t.Fatalf("channel_tags rule = %#v, want signal without runtime-only tags", rule)
	}
	if rule, ok := appliedRuleBySource(originCtx.Applied, "contacts"); !ok {
		t.Fatalf("origin applied rules missing contacts: %#v", originCtx.Applied)
	} else if !containsString(rule.Tags, "projects") || containsString(rule.Tags, "message_channel") || containsString(rule.Tags, "owner") {
		t.Fatalf("contacts rule = %#v, want projects without runtime-only tags", rule)
	} else if !containsString(rule.ContextRefs, "kb:projects/current.md") {
		t.Fatalf("contacts rule = %#v, want exact context ref", rule)
	}
	if rule, ok := appliedRuleBySource(originCtx.Applied, "channel_binding"); !ok || !containsString(rule.Tags, "owner") {
		t.Fatalf("owner binding rule = %#v, ok=%v, want owner", rule, ok)
	}
}

func TestContactOrigin_InjectsExactContextRefs(t *testing.T) {
	kbDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(kbDir, "projects"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(kbDir, "projects", "current.md"),
		[]byte("---\ntags: [private]\n---\n# Current Projects\nBring project context forward."), 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

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

	loop := buildTestLoop(mock, nil)
	loop.SetTagContextAssembler(NewTagContextAssembler(TagContextAssemblerConfig{
		KBDir: kbDir,
	}))
	loop.UseContactLookup(&mockContactLookup{
		policies: map[string]*ContactOriginPolicy{
			"David": {
				ContextRefs: []string{"kb:projects/current.md"},
			},
		},
	})

	_, err := loop.Run(context.Background(), &Request{
		Messages: []Message{{Role: "user", Content: "what do you know?"}},
		Hints:    map[string]string{"source": "signal"},
		ChannelBinding: &memory.ChannelBinding{
			Channel:     "signal",
			ContactName: "David",
		},
	}, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	systemPrompt := mock.calls[0].Messages[0].Content
	for _, want := range []string{"Session Origin Context", "kb:projects/current.md", "Current Projects", "Bring project context forward."} {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, systemPrompt)
		}
	}
	if strings.Contains(systemPrompt, "tags: [private]") {
		t.Fatalf("frontmatter should be stripped from context refs:\n%s", systemPrompt)
	}
}

func TestResetCapabilities_DropsOnlyVoluntaryTags(t *testing.T) {
	capTags := map[string]config.CapabilityTagConfig{
		"base": {
			Description:  "Base tools",
			AlwaysActive: true,
		},
		"signal": {Description: "Signal channel"},
		"owner": {
			Description: "Owner-scoped privileged tools.",
			Protected:   true,
		},
		"extra": {Description: "Voluntary extra tools"},
	}

	scope := newCapabilityScope(capTags, nil)
	scope.PinChannelTags([]string{"signal", "owner"})
	if err := scope.Request("extra"); err != nil {
		t.Fatalf("Request(extra) error: %v", err)
	}

	ctx := withCapabilityScope(context.Background(), scope)
	loop := buildTestLoop(&mockLLM{}, nil)
	loop.SetCapabilityTags(capTags, nil)

	dropped, err := loop.ResetCapabilities(ctx)
	if err != nil {
		t.Fatalf("ResetCapabilities() error: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != "extra" {
		t.Fatalf("dropped = %#v, want [extra]", dropped)
	}

	active := scope.Snapshot()
	if active["extra"] {
		t.Fatalf("active = %#v, want extra dropped", active)
	}
	for _, tag := range []string{"base", "signal", "owner"} {
		if !active[tag] {
			t.Fatalf("active = %#v, want %s preserved", active, tag)
		}
	}
}
