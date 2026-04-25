package tools

import (
	"context"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/memory"
)

func TestSendReaction_DispatchesToCurrentChannel(t *testing.T) {
	reg := NewEmptyRegistry()
	var got ChannelReactionRequest
	reg.RegisterChannelReactionHandler("signal", func(_ context.Context, req ChannelReactionRequest) (string, error) {
		got = req
		return "reacted", nil
	})

	ctx := context.Background()
	ctx = WithConversationID(ctx, "signal-15551234567")
	ctx = WithChannelBinding(ctx, &memory.ChannelBinding{
		Channel: "signal",
		Address: "+15551234567",
	})

	result, err := reg.Execute(ctx, "send_reaction", `{"emoji":"ok"}`)
	if err != nil {
		t.Fatalf("Execute(send_reaction) error: %v", err)
	}
	if result != "reacted" {
		t.Fatalf("result = %q, want reacted", result)
	}
	if got.Channel != "signal" || got.Recipient != "+15551234567" || got.ConversationID != "signal-15551234567" {
		t.Fatalf("request target = %#v", got)
	}
	if got.Emoji != "ok" || got.Target != "latest" {
		t.Fatalf("request reaction = %#v", got)
	}
}

func TestSendReaction_UsesExplicitTarget(t *testing.T) {
	reg := NewEmptyRegistry()
	var got ChannelReactionRequest
	reg.RegisterChannelReactionHandler("signal", func(_ context.Context, req ChannelReactionRequest) (string, error) {
		got = req
		return "reacted", nil
	})

	ctx := WithHints(context.Background(), map[string]string{
		"source": "signal",
		"sender": "+15551234567",
	})

	_, err := reg.Execute(ctx, "send_reaction", `{"emoji":"ok","target":"[ts:1700000000000]"}`)
	if err != nil {
		t.Fatalf("Execute(send_reaction) error: %v", err)
	}
	if got.Target != "[ts:1700000000000]" {
		t.Fatalf("Target = %q", got.Target)
	}
}

func TestSendReaction_UnknownChannel(t *testing.T) {
	reg := NewEmptyRegistry()
	reg.RegisterChannelReactionHandler("signal", func(_ context.Context, _ ChannelReactionRequest) (string, error) {
		return "reacted", nil
	})

	_, err := reg.Execute(context.Background(), "send_reaction", `{"emoji":"ok"}`)
	if err == nil {
		t.Fatal("expected error")
	}
}
