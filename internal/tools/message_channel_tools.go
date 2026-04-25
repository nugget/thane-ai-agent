package tools

import (
	"context"
	"fmt"
	"strings"
)

// ChannelReactionRequest is the channel-neutral request shape for an
// in-conversation reaction. Channel providers translate it into their
// native API details.
type ChannelReactionRequest struct {
	Channel        string
	Recipient      string
	ConversationID string
	Emoji          string
	Target         string
}

// ChannelReactionFunc handles a normalized in-channel reaction.
type ChannelReactionFunc func(context.Context, ChannelReactionRequest) (string, error)

// RegisterChannelReactionHandler registers a reaction handler for one
// message-channel provider and exposes the normalized send_reaction
// tool.
func (r *Registry) RegisterChannelReactionHandler(channel string, handler ChannelReactionFunc) {
	channel = strings.TrimSpace(channel)
	if channel == "" || handler == nil {
		return
	}
	if r.channelReactionHandlers == nil {
		r.channelReactionHandlers = make(map[string]ChannelReactionFunc)
	}
	r.channelReactionHandlers[channel] = handler
	r.registerMessageChannelTools()
}

func (r *Registry) registerMessageChannelTools() {
	r.Register(&Tool{
		Name:        "send_reaction",
		Description: "React inside the current message-app conversation. Use this for lightweight acknowledgement or emphasis; final reply text is sent by the active channel automatically.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"emoji": map[string]any{
					"type":        "string",
					"description": "Reaction emoji to send.",
				},
				"target": map[string]any{
					"type":        "string",
					"description": "Message target. Use \"latest\" for the most recent inbound message, or a channel-native target token from the conversation such as [ts:1700000000000]. Defaults to latest.",
				},
			},
			"required": []string{"emoji"},
		},
		Handler: r.handleSendReaction,
	})
}

func (r *Registry) handleSendReaction(ctx context.Context, args map[string]any) (string, error) {
	emoji := strings.TrimSpace(messageStringArg(args, "emoji"))
	if emoji == "" {
		return "", fmt.Errorf("emoji is required")
	}
	target := strings.TrimSpace(messageStringArg(args, "target"))
	if target == "" {
		target = "latest"
	}

	hints := HintsFromContext(ctx)
	source := strings.TrimSpace(hints["source"])
	recipient := strings.TrimSpace(hints["sender"])
	binding := ChannelBindingFromContext(ctx)
	if binding != nil {
		if source == "" {
			source = strings.TrimSpace(binding.Channel)
		}
		if recipient == "" {
			recipient = strings.TrimSpace(binding.Address)
		}
	}
	if source == "" {
		return "", fmt.Errorf("current message channel is unknown")
	}

	handler := r.channelReactionHandlers[source]
	if handler == nil {
		return "", ErrUnavailable{
			Tool:   "send_reaction",
			Reason: fmt.Sprintf("no reaction provider registered for channel %q", source),
		}
	}

	return handler(ctx, ChannelReactionRequest{
		Channel:        source,
		Recipient:      recipient,
		ConversationID: strings.TrimSpace(ConversationIDFromContext(ctx)),
		Emoji:          emoji,
		Target:         target,
	})
}

func messageStringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	switch v := args[key].(type) {
	case string:
		return v
	default:
		return ""
	}
}
