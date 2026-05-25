package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
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
		Name: "request_core_attention",
		Description: "Ask the designated core/owner loop to review a concern. Use this from delegate, service, or subsystem loops when a human-facing alert, message, or strategic decision may be needed. " +
			"This call forces the core loop's next iteration into a supervisor turn — costlier than a normal wake — so reserve it for concerns that genuinely warrant the extra capacity, not as a routine notification channel. " +
			"Do not include recipients, phone numbers, delivery channels, or instructions to send immediately; the core loop decides whether to deliver, defer, or ignore the concern.",
		Parameters:      coreAttentionToolParameters(),
		Handler:         r.handleRequestCoreAttention,
		AlwaysAvailable: true,
	})
}

func coreAttentionToolParameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"concern": map[string]any{
				"type":        "string",
				"description": "The concern the core loop should review, stated as a decision or risk rather than a delivery command.",
			},
			"suggested_action": map[string]any{
				"type":        "string",
				"description": "Optional action the core loop should consider. The core loop may ignore or defer it.",
			},
			"context": map[string]any{
				"type":        "string",
				"description": "Optional compact context needed to judge timing, intent, and impact.",
			},
			"priority": map[string]any{
				"type":        "string",
				"enum":        []string{"low", "normal", "urgent"},
				"description": "How quickly the core loop should review the concern. This is not permission to message anyone directly.",
			},
		},
		"required": []string{"concern"},
	}
}

type coreAttentionTarget struct {
	LoopID     string     `json:"loop_id"`
	LoopName   string     `json:"loop_name"`
	Reason     string     `json:"reason"`
	LastActive *time.Time `json:"last_active,omitempty"`
}

func (r *Registry) handleRequestCoreAttention(ctx context.Context, args map[string]any) (string, error) {
	if r.messageBus == nil {
		return "", fmt.Errorf("message bus is not configured")
	}
	concern := strings.TrimSpace(ldStringArg(args, "concern"))
	if concern == "" {
		return "", fmt.Errorf("concern is required")
	}
	target, err := r.resolveCoreAttentionTarget()
	if err != nil {
		return "", err
	}

	payload := messages.LoopNotifyPayload{
		Kind:            "core_attention_request",
		Concern:         concern,
		SuggestedAction: strings.TrimSpace(ldStringArg(args, "suggested_action")),
		Context:         strings.TrimSpace(ldStringArg(args, "context")),
		ForceSupervisor: true,
	}

	env := messages.Envelope{
		From: senderIdentityFromContext(ctx),
		To: messages.Destination{
			Kind:     messages.DestinationLoop,
			Target:   target.LoopID,
			Selector: messages.SelectorID,
		},
		Type:     messages.TypeSignal,
		Payload:  payload,
		Priority: messagePriorityArg(args),
		Scope:    []string{"core_attention"},
	}

	result, err := r.messageBus.Send(ctx, env)
	if err != nil {
		return "", err
	}
	return ldMarshalToolJSON(map[string]any{
		"status":   "ok",
		"target":   target,
		"delivery": result,
	})
}

func (r *Registry) resolveCoreAttentionTarget() (coreAttentionTarget, error) {
	if r.liveLoopRegistry == nil {
		return coreAttentionTarget{}, fmt.Errorf("core attention target unavailable: loop registry is not configured")
	}
	statuses := r.liveLoopRegistry.Statuses()
	if len(statuses) == 0 {
		return coreAttentionTarget{}, fmt.Errorf("core attention target unavailable: no live loops are registered")
	}
	if target, ok := newestCoreAttentionTarget(statuses, func(st looppkg.Status) bool {
		return metadataFlag(st.Config.Metadata, "core_attention_target") ||
			metadataEquals(st.Config.Metadata, "attention_role", "core") ||
			metadataEquals(st.Config.Metadata, "role", "core")
	}, "metadata_core_attention_target"); ok {
		return target, nil
	}
	if target, ok := newestCoreAttentionTarget(statuses, func(st looppkg.Status) bool {
		return metadataEquals(st.Config.Metadata, "category", "channel") && metadataFlag(st.Config.Metadata, "is_owner")
	}, "recent_owner_channel"); ok {
		return target, nil
	}
	return coreAttentionTarget{}, fmt.Errorf("core attention target unavailable: no loop has metadata core_attention_target=true and no active owner channel loop was found")
}

func newestCoreAttentionTarget(statuses []looppkg.Status, accept func(looppkg.Status) bool, reason string) (coreAttentionTarget, bool) {
	matches := make([]looppkg.Status, 0, len(statuses))
	for _, st := range statuses {
		if st.ID == "" || st.Name == "" || !accept(st) {
			continue
		}
		matches = append(matches, st)
	}
	if len(matches) == 0 {
		return coreAttentionTarget{}, false
	}
	sort.SliceStable(matches, func(i, j int) bool {
		iActive := loopStatusLastActive(matches[i])
		jActive := loopStatusLastActive(matches[j])
		if iActive.Equal(jActive) {
			return matches[i].ID < matches[j].ID
		}
		return iActive.After(jActive)
	})
	st := matches[0]
	return coreAttentionTarget{
		LoopID:     st.ID,
		LoopName:   st.Name,
		Reason:     reason,
		LastActive: optionalTime(loopStatusLastActive(st)),
	}, true
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func loopStatusLastActive(st looppkg.Status) time.Time {
	if !st.LastWakeAt.IsZero() {
		return st.LastWakeAt
	}
	if !st.StartedAt.IsZero() {
		return st.StartedAt
	}
	return time.Time{}
}

func metadataFlag(meta map[string]string, key string) bool {
	switch strings.ToLower(strings.TrimSpace(meta[key])) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	default:
		return false
	}
}

func metadataEquals(meta map[string]string, key, want string) bool {
	return strings.EqualFold(strings.TrimSpace(meta[key]), want)
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
