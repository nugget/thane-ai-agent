package tools

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ConversationModelPin is the live record of a model override requested
// from inside one conversation. The agent runtime owns the set of pins
// and honors them at turn time; this package defines the shape because
// the pin tool renders it to the model and the API renders it to the
// console.
//
// A pin is deliberately not durable. It lives in process memory, so it
// holds across turns and sessions of its conversation and clears when
// Thane restarts. A poor choice is always one restart from undone, and
// nothing about it has to be found and deleted from storage.
type ConversationModelPin struct {
	ConversationID string `json:"conversation_id"`
	// Model is the resolved deployment ID the conversation is held to.
	Model    string `json:"model"`
	Provider string `json:"provider,omitempty"`
	Resource string `json:"resource,omitempty"`
	// Capability facts the turn-time preflight judges the pin on. A
	// turn the deployment cannot serve routes normally instead of
	// failing, so the model can see in advance which turns those are.
	SupportsTools     bool `json:"supports_tools"`
	SupportsImages    bool `json:"supports_images"`
	SupportsStreaming bool `json:"supports_streaming"`
	ContextWindow     int  `json:"context_window,omitempty"`
	// Reason is the free-text justification recorded with the pin.
	Reason   string    `json:"reason,omitempty"`
	PinnedAt time.Time `json:"pinned_at"`
	// LastFallback records the most recent turn the runtime could not
	// honor the pin and routed instead. Nil until that happens.
	LastFallback *ConversationModelPinFallback `json:"last_fallback,omitempty"`
}

// ConversationModelPinFallback describes one turn that a pinned
// deployment could not serve.
type ConversationModelPinFallback struct {
	At time.Time `json:"at"`
	// Model is the deployment the router chose for that turn instead.
	Model string `json:"model"`
	// Reason is the preflight verdict, in the same words the runtime
	// uses for an explicit request model.
	Reason string `json:"reason"`
}

// ConversationModelPinner manages conversation model pins. Implemented by
// agent.Loop, which resolves references against the live model catalog
// and honors pins when selecting a turn's model.
type ConversationModelPinner interface {
	// PinConversationModel resolves model (a deployment ID or unique
	// model name) and holds conversationID to it from the next turn.
	// Replaces any existing pin for that conversation.
	PinConversationModel(conversationID, model, reason string) (ConversationModelPin, error)
	// ClearConversationModelPin removes the pin for conversationID and
	// returns it. The boolean is false when nothing was pinned.
	ClearConversationModelPin(conversationID string) (ConversationModelPin, bool)
	// ConversationModelPin reports the current pin for conversationID.
	ConversationModelPin(conversationID string) (ConversationModelPin, bool)
}

// conversationModelPinClearValues are the model arguments that mean
// "return this conversation to the router". Models asked to unpin reach
// for these at least as often as for the clear flag.
var conversationModelPinClearValues = map[string]bool{
	"auto":   true,
	"router": true,
	"thane":  true,
	"none":   true,
	"clear":  true,
}

// SetConversationModelPinner adds the conversation_model_pin tool to the
// registry.
func (r *Registry) SetConversationModelPinner(pinner ConversationModelPinner) {
	r.Register(&Tool{
		Name: "conversation_model_pin",
		Description: "Hold the current conversation to one model deployment, bypassing the router on every later turn, or clear that hold. " +
			"Use when the user asks to talk to a specific model (\"switch this chat to opus\", \"use the local model for now\", \"go back to automatic\"). " +
			"model accepts a deployment id or unique model name as listed by model_registry_list; pass clear=true (or model \"auto\") to return the conversation to router selection. " +
			"The pin outranks the channel's configured model and any client-selected model, takes effect from the next turn, holds across turns and sessions of this conversation, and is deliberately dropped when Thane restarts. " +
			"A turn the pinned deployment cannot serve (an image arrives and it lacks vision, the prompt outgrows its window) routes normally for that one turn and the Context line reports the skipped pin. " +
			"This is not fleet policy: to disable or promote a deployment for everyone use model_deployment_set_policy; to pin a loop definition use loop_definition_update.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"model": map[string]any{
					"type":        "string",
					"description": "Deployment id or unique model name to pin (from model_registry_list). \"auto\" clears the pin.",
				},
				"clear": map[string]any{
					"type":        "boolean",
					"description": "When true, remove the pin and return the conversation to the router. model is ignored.",
				},
				"reason": map[string]any{
					"type":        "string",
					"description": "Why this conversation is being pinned, in the user's words where possible. Recorded with the pin and shown to whoever inspects it.",
				},
			},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			// Turns without an ID share the runtime's "default"
			// conversation, and the helper says so; a pin set from one
			// of them holds that shared conversation, which is exactly
			// what its memory and session state already do.
			convID := ConversationIDFromContext(ctx)

			model := strings.TrimSpace(stringArg(args, "model"))
			clear, _ := args["clear"].(bool)
			if model != "" && conversationModelPinClearValues[strings.ToLower(model)] {
				clear = true
			}
			if clear {
				prev, had := pinner.ClearConversationModelPin(convID)
				if !had {
					return mrMarshalToolJSON(map[string]any{
						"status":          "not_pinned",
						"conversation_id": convID,
						"model_selection": "router",
					})
				}
				return mrMarshalToolJSON(map[string]any{
					"status":          "cleared",
					"conversation_id": convID,
					"previous_model":  prev.Model,
					"model_selection": "router",
					"applies":         "next turn",
				})
			}
			if model == "" {
				return "", fmt.Errorf("model is required: pass a deployment id or model name from model_registry_list, or clear=true to return to the router")
			}

			pin, err := pinner.PinConversationModel(convID, model, strings.TrimSpace(stringArg(args, "reason")))
			if err != nil {
				return "", err
			}
			return mrMarshalToolJSON(map[string]any{
				"status":     "pinned",
				"pin":        pin,
				"applies":    "next turn",
				"cleared_by": "conversation_model_pin with clear=true, or a Thane restart",
			})
		},
	})
}
