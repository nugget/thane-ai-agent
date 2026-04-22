package signal

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// ToolProvider is the [tools.Provider] for signal_send_message and
// signal_send_reaction. It is the canonical example of the
// async-binding pattern described in [tools.Provider]: the tools are
// DECLARED at init time so they land in the capability-tag snapshot,
// but their handlers return [tools.ErrUnavailable] until Bind is
// called with a connected signal-cli Client.
//
// Before the Provider pattern existed, these tools were registered
// from inside a deferWorker closure after signal-cli finished
// starting, and a deferredTools map exempted them from "unregistered
// tool" validation warnings. ToolProvider replaces that escape hatch:
// the tools exist in the registry from init onwards, visible to
// capability-tag resolution and the model-facing manifest, and the
// handler internally reports unavailability until signal-cli is live.
type ToolProvider struct {
	mu     sync.RWMutex
	client *Client
	bridge *Bridge
}

// NewToolProvider returns an unbound provider. Its tools are declared
// but invocations return [tools.ErrUnavailable] until [Bind] supplies
// a live client (and bridge for the reaction tool).
func NewToolProvider() *ToolProvider {
	return &ToolProvider{}
}

// Bind attaches a connected signal-cli client and bridge so tool
// invocations can succeed. Called by the deferWorker after
// Client.Start completes. Bind is safe to call multiple times; a
// subsequent call replaces the previous bindings.
func (p *ToolProvider) Bind(client *Client, bridge *Bridge) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.client = client
	p.bridge = bridge
}

// Name implements [tools.Provider].
func (p *ToolProvider) Name() string { return "signal" }

// Tools implements [tools.Provider].
func (p *ToolProvider) Tools() []*tools.Tool {
	return []*tools.Tool{
		{
			Name:        "signal_send_message",
			Description: "Send a Signal message to a phone number. Use this to reply to the user's Signal message or initiate a new Signal conversation.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"recipient": map[string]any{
						"type":        "string",
						"description": "Phone number including country code (e.g., +15551234567)",
					},
					"message": map[string]any{
						"type":        "string",
						"description": "Message text to send",
					},
				},
				"required": []string{"recipient", "message"},
			},
			Handler: p.handleSendMessage,
		},
		{
			Name:        "signal_send_reaction",
			Description: "React to a Signal message with an emoji. Use this to acknowledge messages or express reactions. The target_timestamp identifies which message to react to — use the [ts:...] value from the message, or \"latest\" to react to the most recent message from the recipient.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"recipient": map[string]any{
						"type":        "string",
						"description": "Phone number including country code (e.g., +15551234567)",
					},
					"emoji": map[string]any{
						"type":        "string",
						"description": "Reaction emoji (e.g., 👍, ❤️, 😂)",
					},
					"target_author": map[string]any{
						"type":        "string",
						"description": "Phone number of the message author to react to",
					},
					"target_timestamp": map[string]any{
						"type":        "string",
						"description": "Timestamp of the message to react to (from [ts:...] tag) as a numeric string, or \"latest\" for the most recent inbound message from the recipient",
					},
				},
				"required": []string{"recipient", "emoji", "target_author", "target_timestamp"},
			},
			Handler: p.handleSendReaction,
		},
	}
}

func (p *ToolProvider) handleSendMessage(ctx context.Context, args map[string]any) (string, error) {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()
	if client == nil {
		return "", tools.ErrUnavailable{
			Tool:   "signal_send_message",
			Reason: "signal-cli client not connected",
		}
	}

	recipient, _ := args["recipient"].(string)
	message, _ := args["message"].(string)
	if recipient == "" || message == "" {
		return "", fmt.Errorf("recipient and message are required")
	}
	if _, err := client.Send(ctx, recipient, message); err != nil {
		return "", err
	}
	return fmt.Sprintf("Message sent to %s", recipient), nil
}

func (p *ToolProvider) handleSendReaction(ctx context.Context, args map[string]any) (string, error) {
	p.mu.RLock()
	client := p.client
	bridge := p.bridge
	p.mu.RUnlock()
	if client == nil || bridge == nil {
		return "", tools.ErrUnavailable{
			Tool:   "signal_send_reaction",
			Reason: "signal-cli client or bridge not connected",
		}
	}

	recipient, _ := args["recipient"].(string)
	emoji, _ := args["emoji"].(string)
	targetAuthor, _ := args["target_author"].(string)

	if recipient == "" || emoji == "" || targetAuthor == "" {
		return "", fmt.Errorf("recipient, emoji, and target_author are required")
	}

	var targetTS int64
	switch v := args["target_timestamp"].(type) {
	case string:
		if v == "latest" {
			ts, ok := bridge.LastInboundTimestamp(recipient)
			if !ok {
				return "", fmt.Errorf("no recent inbound message from %s to react to", recipient)
			}
			targetTS = ts
		} else {
			// Accept numeric strings (LLMs often serialize large ints as strings).
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return "", fmt.Errorf("target_timestamp must be a numeric string or \"latest\", got %q", v)
			}
			targetTS = n
		}
	case float64:
		targetTS = int64(v)
	default:
		return "", fmt.Errorf("target_timestamp must be a string (numeric or \"latest\")")
	}

	if err := client.SendReaction(ctx, recipient, emoji, targetAuthor, targetTS, false); err != nil {
		return "", err
	}
	return fmt.Sprintf("Reacted with %s to message from %s", emoji, targetAuthor), nil
}
