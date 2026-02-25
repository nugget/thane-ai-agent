package tools

import (
	"context"
	"testing"
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

func TestContextKeysIndependent(t *testing.T) {
	// Verify that setting one key doesn't interfere with another.
	ctx := context.Background()
	ctx = WithConversationID(ctx, "conv-1")
	ctx = WithSessionID(ctx, "sess-1")
	ctx = WithToolCallID(ctx, "call-1")

	if got := ConversationIDFromContext(ctx); got != "conv-1" {
		t.Errorf("ConversationIDFromContext() = %q, want %q", got, "conv-1")
	}
	if got := SessionIDFromContext(ctx); got != "sess-1" {
		t.Errorf("SessionIDFromContext() = %q, want %q", got, "sess-1")
	}
	if got := ToolCallIDFromContext(ctx); got != "call-1" {
		t.Errorf("ToolCallIDFromContext() = %q, want %q", got, "call-1")
	}
}
