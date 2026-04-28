package tools

import (
	"context"
	"slices"
	"testing"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

func TestConversationIDFromContext(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"default when unset", context.Background(), "default"},
		{"round trip", WithConversationID(context.Background(), "conv-123"), "conv-123"},
		{"empty string returns default", WithConversationID(context.Background(), ""), "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConversationIDFromContext(tt.ctx)
			if got != tt.want {
				t.Errorf("ConversationIDFromContext() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionIDFromContext(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"empty when unset", context.Background(), ""},
		{"round trip", WithSessionID(context.Background(), "sess-abc"), "sess-abc"},
		{"empty string returns empty", WithSessionID(context.Background(), ""), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SessionIDFromContext(tt.ctx)
			if got != tt.want {
				t.Errorf("SessionIDFromContext() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolCallIDFromContext(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"empty when unset", context.Background(), ""},
		{"round trip", WithToolCallID(context.Background(), "call_xyz"), "call_xyz"},
		{"empty string returns empty", WithToolCallID(context.Background(), ""), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToolCallIDFromContext(tt.ctx)
			if got != tt.want {
				t.Errorf("ToolCallIDFromContext() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHintsFromContext(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want map[string]string
	}{
		{"nil when unset", context.Background(), nil},
		{"nil hints returns original context", WithHints(context.Background(), nil), nil},
		{"round trip", WithHints(context.Background(), map[string]string{"key": "val"}), map[string]string{"key": "val"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HintsFromContext(tt.ctx)
			if tt.want == nil {
				if got != nil {
					t.Errorf("HintsFromContext() = %v, want nil", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Errorf("HintsFromContext() len = %d, want %d", len(got), len(tt.want))
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("HintsFromContext()[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestIterationIndexFromContext(t *testing.T) {
	tests := []struct {
		name    string
		ctx     context.Context
		want    int
		wantSet bool
	}{
		{"unset returns -1/false", context.Background(), -1, false},
		{"round trip zero", WithIterationIndex(context.Background(), 0), 0, true},
		{"round trip positive", WithIterationIndex(context.Background(), 5), 5, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := IterationIndexFromContext(tt.ctx)
			if got != tt.want || ok != tt.wantSet {
				t.Errorf("IterationIndexFromContext() = (%d, %v), want (%d, %v)", got, ok, tt.want, tt.wantSet)
			}
		})
	}
}

func TestContextKeysIndependent(t *testing.T) {
	// Verify that setting one key doesn't interfere with another.
	ctx := context.Background()
	ctx = WithConversationID(ctx, "conv-1")
	ctx = WithSessionID(ctx, "sess-1")
	ctx = WithToolCallID(ctx, "call-1")
	ctx = WithChannelBinding(ctx, &memory.ChannelBinding{Channel: "signal", Address: "+15551234567"})

	if got := ConversationIDFromContext(ctx); got != "conv-1" {
		t.Errorf("ConversationIDFromContext() = %q, want %q", got, "conv-1")
	}
	if got := SessionIDFromContext(ctx); got != "sess-1" {
		t.Errorf("SessionIDFromContext() = %q, want %q", got, "sess-1")
	}
	if got := ToolCallIDFromContext(ctx); got != "call-1" {
		t.Errorf("ToolCallIDFromContext() = %q, want %q", got, "call-1")
	}
	if got := ChannelBindingFromContext(ctx); got == nil || got.Channel != "signal" || got.Address != "+15551234567" {
		t.Errorf("ChannelBindingFromContext() = %#v", got)
	}
}

func TestChannelBindingFromContext(t *testing.T) {
	binding := &memory.ChannelBinding{
		Channel:     "signal",
		Address:     "+15551234567",
		ContactID:   "contact-1",
		ContactName: "Alice Smith",
		TrustZone:   "known",
	}
	ctx := WithChannelBinding(context.Background(), binding)
	got := ChannelBindingFromContext(ctx)
	if got == nil {
		t.Fatal("ChannelBindingFromContext() = nil, want binding")
	}
	if got.ContactName != "Alice Smith" || got.ContactID != "contact-1" {
		t.Fatalf("ChannelBindingFromContext() = %#v", got)
	}
	got.ContactName = "changed"
	if binding.ContactName != "Alice Smith" {
		t.Fatalf("binding mutated = %#v", binding)
	}
}

func TestInheritableCapabilityTagsFromContext(t *testing.T) {
	tags := []string{"ha", "kb:article/example"}
	ctx := WithInheritableCapabilityTags(context.Background(), tags)
	tags[0] = "mutated"

	got := InheritableCapabilityTagsFromContext(ctx)
	if !slices.Equal(got, []string{"ha", "kb:article/example"}) {
		t.Fatalf("InheritableCapabilityTagsFromContext() = %#v", got)
	}

	got[0] = "changed"
	got = InheritableCapabilityTagsFromContext(ctx)
	if !slices.Equal(got, []string{"ha", "kb:article/example"}) {
		t.Fatalf("stored inheritable tags mutated = %#v", got)
	}

	ctx = WithInheritableCapabilityTags(ctx, nil)
	if got := InheritableCapabilityTagsFromContext(ctx); got != nil {
		t.Fatalf("cleared inheritable tags = %#v, want nil", got)
	}
}

func TestLoopCompletionTargetFromContext(t *testing.T) {
	t.Run("signal context returns channel target", func(t *testing.T) {
		ctx := WithConversationID(context.Background(), "signal-15551234567")
		ctx = WithHints(ctx, map[string]string{
			"source": "signal",
			"sender": "+15551234567",
		})

		mode, conversationID, target := LoopCompletionTargetFromContext(ctx)
		if mode != looppkg.CompletionChannel {
			t.Fatalf("mode = %q, want channel", mode)
		}
		if conversationID != "signal-15551234567" {
			t.Fatalf("conversationID = %q, want signal-15551234567", conversationID)
		}
		if target == nil || target.Channel != "signal" || target.Recipient != "+15551234567" || target.ConversationID != "signal-15551234567" {
			t.Fatalf("target = %#v", target)
		}
	})

	t.Run("signal context without sender falls back to conversation", func(t *testing.T) {
		ctx := WithConversationID(context.Background(), "signal-15551234567")
		ctx = WithHints(ctx, map[string]string{
			"source": "signal",
		})

		mode, conversationID, target := LoopCompletionTargetFromContext(ctx)
		if mode != looppkg.CompletionConversation {
			t.Fatalf("mode = %q, want conversation", mode)
		}
		if conversationID != "signal-15551234567" {
			t.Fatalf("conversationID = %q, want signal-15551234567", conversationID)
		}
		if target != nil {
			t.Fatalf("target = %#v, want nil", target)
		}
	})

	t.Run("channel binding can drive signal target without hints", func(t *testing.T) {
		ctx := WithConversationID(context.Background(), "signal-15551234567")
		ctx = WithChannelBinding(ctx, &memory.ChannelBinding{
			Channel: "signal",
			Address: "+15551234567",
		})

		mode, conversationID, target := LoopCompletionTargetFromContext(ctx)
		if mode != looppkg.CompletionChannel {
			t.Fatalf("mode = %q, want channel", mode)
		}
		if conversationID != "signal-15551234567" {
			t.Fatalf("conversationID = %q, want signal-15551234567", conversationID)
		}
		if target == nil || target.Channel != "signal" || target.Recipient != "+15551234567" {
			t.Fatalf("target = %#v", target)
		}
	})

	t.Run("owu conversation returns owu channel target", func(t *testing.T) {
		ctx := WithConversationID(context.Background(), "owu-abc123")

		mode, conversationID, target := LoopCompletionTargetFromContext(ctx)
		if mode != looppkg.CompletionChannel {
			t.Fatalf("mode = %q, want channel", mode)
		}
		if conversationID != "owu-abc123" {
			t.Fatalf("conversationID = %q, want owu-abc123", conversationID)
		}
		if target == nil || target.Channel != "owu" || target.ConversationID != "owu-abc123" {
			t.Fatalf("target = %#v", target)
		}
	})

	t.Run("owu source hint returns owu channel target", func(t *testing.T) {
		ctx := WithConversationID(context.Background(), "conv-owu-1")
		ctx = WithHints(ctx, map[string]string{
			"source": "owu",
		})

		mode, conversationID, target := LoopCompletionTargetFromContext(ctx)
		if mode != looppkg.CompletionChannel {
			t.Fatalf("mode = %q, want channel", mode)
		}
		if conversationID != "conv-owu-1" {
			t.Fatalf("conversationID = %q, want conv-owu-1", conversationID)
		}
		if target == nil || target.Channel != "owu" || target.ConversationID != "conv-owu-1" {
			t.Fatalf("target = %#v", target)
		}
	})

	t.Run("other context falls back to conversation", func(t *testing.T) {
		ctx := WithConversationID(context.Background(), "conv-123")

		mode, conversationID, target := LoopCompletionTargetFromContext(ctx)
		if mode != looppkg.CompletionConversation {
			t.Fatalf("mode = %q, want conversation", mode)
		}
		if conversationID != "conv-123" {
			t.Fatalf("conversationID = %q, want conv-123", conversationID)
		}
		if target != nil {
			t.Fatalf("target = %#v, want nil", target)
		}
	})
}

func TestSuppressAlwaysContext(t *testing.T) {
	t.Run("default false on bare context", func(t *testing.T) {
		if SuppressAlwaysContextFromContext(context.Background()) {
			t.Fatal("default should be false")
		}
	})

	t.Run("suppress=true sets the flag", func(t *testing.T) {
		ctx := WithSuppressAlwaysContext(context.Background(), true)
		if !SuppressAlwaysContextFromContext(ctx) {
			t.Fatal("expected suppression to be set")
		}
	})

	t.Run("suppress=false clears an inherited true", func(t *testing.T) {
		parent := WithSuppressAlwaysContext(context.Background(), true)
		child := WithSuppressAlwaysContext(parent, false)
		if SuppressAlwaysContextFromContext(child) {
			t.Fatal("WithSuppressAlwaysContext(ctx, false) must override an inherited true")
		}
	})
}
