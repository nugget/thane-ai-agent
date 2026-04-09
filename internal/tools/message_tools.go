package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/messages"
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
	r.Register(&Tool{
		Name: "signal_loop",
		Description: "Send a one-shot signal envelope to a live timer-driven loop. " +
			"Use this to tap a sleeping watcher with fresh context or force the next iteration " +
			"to run in supervisor mode. If the loop is currently sleeping, it wakes immediately; " +
			"if it is already processing, the signal is queued for the next iteration.",
		Parameters: map[string]any{
			"type": "object",
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
		},
		Handler: r.handleSignalLoop,
	})
}

func (r *Registry) handleSignalLoop(ctx context.Context, args map[string]any) (string, error) {
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

	payload := messages.LoopSignalPayload{
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
