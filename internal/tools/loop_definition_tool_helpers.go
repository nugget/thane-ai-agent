package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

func ldStringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func ldIntArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return 0
	}
}

func ldMarshalToolJSON(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeLoopSpecArg(args map[string]any, key string) (looppkg.Spec, error) {
	raw, ok := args[key]
	if !ok {
		return looppkg.Spec{}, fmt.Errorf("%s is required", key)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return looppkg.Spec{}, err
	}
	var spec looppkg.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return looppkg.Spec{}, err
	}
	return spec, nil
}

func decodeLoopLaunchArg(args map[string]any, key string) (looppkg.Launch, error) {
	raw, ok := args[key]
	if !ok {
		return looppkg.Launch{}, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return looppkg.Launch{}, err
	}
	var launch looppkg.Launch
	if err := json.Unmarshal(data, &launch); err != nil {
		return looppkg.Launch{}, err
	}
	if err := validateLoopLaunchOverrides(launch); err != nil {
		return looppkg.Launch{}, err
	}
	return launch, nil
}

// validateLoopLaunchOverrides catches common mistakes where a caller
// placed a routing override inside the opaque metadata map instead of
// the corresponding top-level Launch field. Metadata is informational
// tagging only; it does not influence routing, tool selection, or
// budgets. Returning an actionable error here is preferable to silently
// ignoring the override and letting the caller misinterpret the run.
func validateLoopLaunchOverrides(launch looppkg.Launch) error {
	if len(launch.Metadata) == 0 {
		return nil
	}
	if model, ok := launch.Metadata["model"]; ok && strings.TrimSpace(model) != "" && strings.TrimSpace(launch.Model) == "" {
		return fmt.Errorf(
			"launch.metadata.model=%q is opaque tagging and does not override the model; "+
				"use the top-level launch.model field (e.g. \"launch\": {\"model\": %q})",
			model, model)
	}
	return nil
}

func loopCompletionChannelTargetProperty() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Explicit detached channel delivery target when spec.completion=\"channel\". If omitted, the current origin context may infer one automatically.",
		"properties": map[string]any{
			"channel": map[string]any{
				"type":        "string",
				"description": "Channel integration name such as \"signal\" or \"owu\".",
			},
			"recipient": map[string]any{
				"type":        "string",
				"description": "Recipient or address for integrations that route by recipient (for example a Signal phone number).",
			},
			"conversation_id": map[string]any{
				"type":        "string",
				"description": "Channel-native conversation or thread ID for integrations that route by conversation.",
			},
		},
	}
}

func loopChannelBindingProperty() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Typed channel identity bound to the launched loop. If omitted, the current tool-call channel binding is inherited when available.",
		"properties": map[string]any{
			"channel": map[string]any{
				"type":        "string",
				"description": "Channel integration name (for example \"signal\").",
			},
			"address": map[string]any{
				"type":        "string",
				"description": "Channel address or sender identity bound to this loop.",
			},
			"contact_id": map[string]any{
				"type":        "string",
				"description": "Resolved internal contact ID for this channel identity, when known.",
			},
			"contact_name": map[string]any{
				"type":        "string",
				"description": "Resolved display name for the bound contact, when known.",
			},
			"trust_zone": map[string]any{
				"type":        "string",
				"description": "Trust classification attached to this channel identity.",
			},
			"link_source": map[string]any{
				"type":        "string",
				"description": "How this channel binding was established.",
			},
			"is_owner": map[string]any{
				"type":        "boolean",
				"description": "Whether this bound identity belongs to the owner/self side of the conversation.",
			},
		},
	}
}

// loopLaunchOverrideProperties returns the JSON-schema property set for
// the per-launch override fields accepted by both
// [Registry.handleLoopDefinitionLaunch] and [Registry.handleSpawnLoop].
// Exposing a typed schema lets the model see the real field names and
// their behavior rather than guessing at an opaque object — the
// disaster mode we most care about is putting a `model` override inside
// `metadata`, where it has no effect on routing.
func loopLaunchOverrideProperties() map[string]any {
	return map[string]any{
		"model": map[string]any{
			"type":        "string",
			"description": "Override the model for this launch (e.g. \"claude-sonnet-4-5\", \"gpt-oss:120b\"). This is the field the router reads. metadata.model is NOT read by the router and does not change the model.",
		},
		"task": map[string]any{
			"type":        "string",
			"description": "Override the task text for this launch. Applied only when the stored spec does not already supply a task.",
		},
		"parent_id": map[string]any{
			"type":        "string",
			"description": "Associate this launch with a parent loop ID for parent/child tracking.",
		},
		"allowed_tools": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "Restrict this launch's effective tool set to the listed tools (intersected with capability gating).",
		},
		"exclude_tools": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "Remove specific tools from this launch's effective tool set.",
		},
		"initial_tags": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "Preload these capability tags so the child loop starts with them active.",
		},
		"hints": map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": "string"},
			"description":          "Free-form string/string routing or context hints. Use named top-level fields (model, allowed_tools, etc.) for well-known behavior overrides; hints are for softer signals.",
		},
		"max_iterations": map[string]any{
			"type":        "integer",
			"description": "Cap the number of tool-call iterations for this launch.",
		},
		"max_output_tokens": map[string]any{
			"type":        "integer",
			"description": "Cap the model's output tokens per call for this launch.",
		},
		"system_prompt": map[string]any{
			"type":        "string",
			"description": "Override the system prompt for this launch.",
		},
		"prompt_mode": map[string]any{
			"type":        "string",
			"enum":        []string{"full", "task"},
			"description": "Select the system-prompt shape. full is the normal Thane prompt. task is a compact worker prompt that suppresses full identity and always-on continuity context.",
		},
		"skip_context": map[string]any{
			"type":        "boolean",
			"description": "Skip context providers (awareness, archive prewarm, etc.) for this launch.",
		},
		"skip_tag_filter": map[string]any{
			"type":        "boolean",
			"description": "Disable tag-based tool filtering for this launch. Use with care.",
		},
		"suppress_always_context": map[string]any{
			"type":        "boolean",
			"description": "Drop always-on ambient context (presence, episodic memory, notification history, etc.) from this launch's system prompt. Tagged context providers and KB articles still fire. Default is false; delegate executions set this to true so child loops aren't billed for the main loop's experiential context.",
		},
		"conversation_id": map[string]any{
			"type":        "string",
			"description": "Bind this launch to a specific conversation ID instead of deriving one.",
		},
		"channel_binding": loopChannelBindingProperty(),
		"run_timeout": map[string]any{
			"type":        "string",
			"description": "For request_reply launches, stop waiting after this wall-clock duration and return a timeout error. Use a Go duration string like \"30s\" or \"2m\".",
		},
		"completion_conversation_id": map[string]any{
			"type":        "string",
			"description": "When spec.completion=\"conversation\", deliver the final result to this conversation ID.",
		},
		"completion_channel": loopCompletionChannelTargetProperty(),
		"metadata": map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": "string"},
			"description":          "Opaque string/string tags attached to the launched loop for correlation or audit. NOT used for routing, tools, budgets, or any runtime behavior. To override the model use top-level \"model\". To override tools use \"allowed_tools\" / \"exclude_tools\". To override budgets use \"max_iterations\" / \"max_output_tokens\".",
		},
		"fallback_content": map[string]any{
			"type":        "string",
			"description": "Static fallback reply used if the nested agent run produces no content but the launch still needs a last-resort response.",
		},
		"tool_timeout": map[string]any{
			"type":        "string",
			"description": "Per-tool-call timeout for nested agent tool executions inside this loop. Use a Go duration string like \"30s\" or \"2m\".",
		},
		"usage_role": map[string]any{
			"type":        "string",
			"description": "Usage attribution role label recorded on model and tool usage for this launch (for example \"delegate\").",
		},
		"usage_task_name": map[string]any{
			"type":        "string",
			"description": "Usage attribution task label recorded on model and tool usage for this launch (for example \"general\").",
		},
	}
}

func findLoopDefinition(snapshot *looppkg.DefinitionRegistrySnapshot, name string) (looppkg.DefinitionSnapshot, bool) {
	if snapshot == nil {
		return looppkg.DefinitionSnapshot{}, false
	}
	for _, def := range snapshot.Definitions {
		if def.Name == name {
			return def, true
		}
	}
	return looppkg.DefinitionSnapshot{}, false
}

func currentLoopDefinitionSnapshot(r *Registry) (*looppkg.DefinitionRegistrySnapshot, error) {
	if r.loopDefinitionRegistry == nil {
		return nil, fmt.Errorf("loop definition registry not configured")
	}
	snapshot := r.loopDefinitionRegistry.Snapshot()
	if snapshot == nil {
		return nil, fmt.Errorf("loop definition registry snapshot unavailable")
	}
	return snapshot, nil
}

func findLoopDefinitionView(view *looppkg.DefinitionRegistryView, name string) (looppkg.DefinitionView, bool) {
	if view == nil {
		return looppkg.DefinitionView{}, false
	}
	for _, def := range view.Definitions {
		if def.Name == name {
			return def, true
		}
	}
	return looppkg.DefinitionView{}, false
}

func currentLoopDefinitionView(r *Registry) (*looppkg.DefinitionRegistryView, error) {
	if r.loopDefinitionView != nil {
		if view := r.loopDefinitionView(); view != nil {
			return view, nil
		}
	}
	snapshot, err := currentLoopDefinitionSnapshot(r)
	if err != nil {
		return nil, err
	}
	return looppkg.BuildDefinitionRegistryView(snapshot, nil), nil
}

func applyLoopLaunchContextDefaults(ctx context.Context, def looppkg.DefinitionView, launch looppkg.Launch) looppkg.Launch {
	if launch.ChannelBinding == nil {
		launch.ChannelBinding = ChannelBindingFromContext(ctx)
	}
	completion := launch.Spec.Completion
	if completion == "" {
		completion = def.Spec.Completion
	}
	switch completion {
	case looppkg.CompletionConversation:
		if strings.TrimSpace(launch.CompletionConversationID) == "" {
			_, conversationID, _ := LoopCompletionTargetFromContext(ctx)
			launch.CompletionConversationID = conversationID
		}
	case looppkg.CompletionChannel:
		if launch.CompletionChannel == nil {
			_, _, target := LoopCompletionTargetFromContext(ctx)
			launch.CompletionChannel = target
		}
	}
	return launch
}

type loopCompletionDecision struct {
	Mode           looppkg.Completion               `json:"mode,omitempty"`
	ConversationID string                           `json:"conversation_id,omitempty"`
	Channel        *looppkg.CompletionChannelTarget `json:"channel,omitempty"`
	Inferred       bool                             `json:"inferred,omitempty"`
	Reason         string                           `json:"reason,omitempty"`
	Warnings       []string                         `json:"warnings,omitempty"`
}

func applyAdHocLoopLaunchContextDefaults(ctx context.Context, launch looppkg.Launch) (looppkg.Launch, loopCompletionDecision) {
	var decision loopCompletionDecision
	if launch.ChannelBinding == nil {
		launch.ChannelBinding = ChannelBindingFromContext(ctx)
	}
	naturalMode, conversationID, target := LoopCompletionTargetFromContext(ctx)
	if launch.Spec.Operation == looppkg.OperationBackgroundTask && launch.Spec.Completion == "" {
		launch.Spec.Completion = naturalMode
		decision.Inferred = true
		decision.Reason = "defaulted from current tool-call origin for detached background completion"
	}
	switch launch.Spec.Completion {
	case looppkg.CompletionConversation:
		if strings.TrimSpace(launch.CompletionConversationID) == "" {
			launch.CompletionConversationID = conversationID
		}
		decision.Mode = looppkg.CompletionConversation
		decision.ConversationID = launch.CompletionConversationID
		if naturalMode == looppkg.CompletionChannel {
			decision.Warnings = append(decision.Warnings, "conversation completion injects an internal callback into conversation memory; use channel completion when the result should arrive as a normal reply on the current interactive channel")
		}
	case looppkg.CompletionChannel:
		if launch.CompletionChannel == nil {
			launch.CompletionChannel = target
		}
		decision.Mode = looppkg.CompletionChannel
		decision.Channel = looppkg.CloneCompletionChannelTarget(launch.CompletionChannel)
		if launch.CompletionChannel == nil {
			decision.Warnings = append(decision.Warnings, "channel completion was selected but the current context did not provide a routable channel target; set completion_channel explicitly if delivery must go to a specific channel endpoint")
		}
	case looppkg.CompletionNone, "":
		decision.Mode = launch.Spec.Completion
	}
	return launch, decision
}
