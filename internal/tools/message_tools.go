package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

// MessageToolDeps wires the shared envelope bus into the tool registry.
type MessageToolDeps struct {
	Bus *messages.Bus
}

// ConfigureMessageTools registers thin envelope-construction tools over the
// shared message bus.
func (r *Registry) ConfigureMessageTools(deps MessageToolDeps) {
	r.messageBus = deps.Bus
	r.registerMessageTools()
}

func (r *Registry) registerMessageTools() {
	if r.messageBus == nil {
		return
	}
	wakeParams := wakeToolParameters()
	r.Register(&Tool{
		Name: "thane_wake",
		Description: "Send a one-shot message envelope to a live timer-driven loop, waking it immediately if sleeping or queueing for the next iteration if processing. " +
			"Use this to tap a sleeping watcher with fresh context or force the next iteration to run in supervisor mode. " +
			"thane_wake is the family-shaped name for what notify_loop does; reach for thane_wake going forward.",
		Parameters: wakeParams,
		Handler:    r.handleNotifyLoop,
	})
	r.Register(&Tool{
		Name: "notify_loop",
		Description: "DEPRECATED: prefer thane_wake. notify_loop remains as a compatibility alias and will be removed in a future release. " +
			"Send a one-shot message envelope to a live timer-driven loop. " +
			"Use this to tap a sleeping watcher with fresh context or force the next iteration " +
			"to run in supervisor mode. If the loop is currently sleeping, it wakes immediately; " +
			"if it is already processing, the message is queued for the next iteration.",
		Parameters: wakeParams,
		Handler:    r.handleNotifyLoop,
	})
}

// wakeToolParameters returns the JSON schema shared by thane_wake and
// the deprecated notify_loop alias.
func wakeToolParameters() map[string]any {
	return map[string]any{
		"type": "object",
		"anyOf": []any{
			map[string]any{"required": []string{"loop_id"}},
			map[string]any{"required": []string{"name"}},
		},
		"properties": map[string]any{
			"loop_id": map[string]any{
				"type":        "string",
				"description": "Exact live loop ID to signal. Preferred when available from loop_status.",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "Exact live loop name to signal when loop_id is not known.",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "Optional one-shot context message delivered only to the next loop iteration.",
			},
			"force_supervisor": map[string]any{
				"type":        "boolean",
				"description": "When true, force the next triggered iteration to run in supervisor mode.",
			},
			"priority": map[string]any{
				"type":        "string",
				"enum":        []string{"low", "normal", "urgent"},
				"description": "Optional delivery priority recorded in the envelope audit trail.",
			},
		},
	}
}

func (r *Registry) handleNotifyLoop(ctx context.Context, args map[string]any) (string, error) {
	if r.messageBus == nil {
		return "", fmt.Errorf("message bus is not configured")
	}
	loopID := strings.TrimSpace(ldStringArg(args, "loop_id"))
	name := strings.TrimSpace(ldStringArg(args, "name"))
	if loopID == "" && name == "" {
		return "", fmt.Errorf("loop_id or name is required")
	}

	destination := messages.Destination{Kind: messages.DestinationLoop}
	switch {
	case loopID != "":
		destination.Target = loopID
		destination.Selector = messages.SelectorID
	default:
		destination.Target = name
		destination.Selector = messages.SelectorName
	}

	payload := messages.LoopNotifyPayload{
		Message:         strings.TrimSpace(ldStringArg(args, "message")),
		ForceSupervisor: boolArg(args, "force_supervisor"),
	}

	env := messages.Envelope{
		From:     senderIdentityFromContext(ctx),
		To:       destination,
		Type:     messages.TypeSignal,
		Payload:  payload,
		Priority: messagePriorityArg(args),
	}

	result, err := r.messageBus.Send(ctx, env)
	if err != nil {
		return "", err
	}
	return ldMarshalToolJSON(map[string]any{
		"status":   "ok",
		"delivery": result,
	})
}

func senderIdentityFromContext(ctx context.Context) messages.Identity {
	hints := HintsFromContext(ctx)
	source := strings.TrimSpace(hints["source"])
	loopID := strings.TrimSpace(LoopIDFromContext(ctx))
	loopName := strings.TrimSpace(hints["loop_name"])
	switch {
	case loopID != "":
		return messages.Identity{Kind: messages.IdentityLoop, ID: loopID, Name: loopName}
	case source == "delegate":
		return messages.Identity{Kind: messages.IdentityDelegate, Name: source}
	default:
		if source == "" {
			source = "conversation"
		}
		return messages.Identity{Kind: messages.IdentityCore, Name: source}
	}
}

func messagePriorityArg(args map[string]any) messages.Priority {
	switch strings.TrimSpace(ldStringArg(args, "priority")) {
	case "", "normal":
		return messages.PriorityNormal
	case "low":
		return messages.PriorityLow
	case "urgent":
		return messages.PriorityUrgent
	default:
		return messages.PriorityNormal
	}
}
