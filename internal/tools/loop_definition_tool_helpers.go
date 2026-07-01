package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

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
	raw, err := coerceStringifiedJSON(raw)
	if err != nil {
		return looppkg.Spec{}, fmt.Errorf("%s was a JSON string but did not parse: %w", key, err)
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
	raw, err := coerceStringifiedJSON(raw)
	if err != nil {
		return looppkg.Launch{}, fmt.Errorf("%s was a JSON string but did not parse: %w", key, err)
	}
	if err := rejectLaunchModelKeys(raw); err != nil {
		return looppkg.Launch{}, err
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return looppkg.Launch{}, err
	}
	var launch looppkg.Launch
	if err := json.Unmarshal(data, &launch); err != nil {
		return looppkg.Launch{}, err
	}
	return launch, nil
}

// coerceStringifiedJSON canonicalizes a tool argument that models sometimes
// emit as a JSON *string* (a stringified object/array) instead of a native
// object. This is a known LLM quirk that bites hardest on tools whose whole
// argument is a single large nested object — the loop spec/launch payloads —
// where big or complex values get serialized as a quoted string while small
// ones arrive as a native dict. Without coercion the string flows into
// json.Marshal→Unmarshal and fails with the opaque "cannot unmarshal string
// into Go value of type loop.specJSON" (see #1116). This applies the
// model-facing-tools.md §2 principle: accept how models think, then
// canonicalize.
//
// A string is only coerced when it looks like a JSON object or array (first
// non-space byte is '{' or '['), so a legitimate bare-string value is never
// reinterpreted. On success the decoded value is returned for the normal
// marshal→unmarshal path (which preserves rejectLaunchModelKeys and every
// other downstream check). A JSON-looking string that fails to parse returns
// an error so the caller can surface a precise message rather than the opaque
// type error. Non-string and non-JSON-looking values pass through unchanged.
func coerceStringifiedJSON(raw any) (any, error) {
	s, ok := raw.(string)
	if !ok {
		return raw, nil
	}
	trimmed := strings.TrimSpace(s)
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return raw, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

// rejectLaunchModelKeys catches tool-facing launch payloads that try to
// set the model. The router's persistent baseline lives on the stored
// spec (`spec.profile.model`) and is the only durable place to pin a
// model. The Go [looppkg.Launch] type no longer carries a Model field —
// so unmarshalling alone would silently drop a `"model"` key. This
// raw-map pre-check preserves a useful error pointing callers at
// `spec.profile.model`.
//
// Rejection fires on any non-null value, not just non-empty strings.
// A caller sending `{"model": 5}` or `{"model": {...}}` would otherwise
// slip past a string-only check and have the key silently dropped by
// the unmarshaller. Same logic applies to `launch.metadata.model`.
//
// Both top-level `launch.model` and `launch.metadata.model` are
// rejected. Other tool-facing layers (the JSON schema for
// [loop_definition_launch] and [spawn_loop]) already omit `model`; this
// is the runtime backstop.
func rejectLaunchModelKeys(raw any) error {
	launch, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	if v, present := launch["model"]; present && !isNilOrEmptyString(v) {
		return fmt.Errorf(
			"launch.model=%v is not accepted from tool input; "+
				"to pin a model, set spec.profile.model "+
				"(via loop_definition_set for persisted definitions, "+
				"or inside the spec passed to spawn_loop for ad-hoc loops)",
			v)
	}
	metadata, _ := launch["metadata"].(map[string]any)
	if v, present := metadata["model"]; present && !isNilOrEmptyString(v) {
		return fmt.Errorf(
			"launch.metadata.model=%v is opaque tagging and does not override the model; "+
				"set spec.profile.model instead "+
				"(via loop_definition_set for persisted definitions, "+
				"or inside the spec passed to spawn_loop for ad-hoc loops)",
			v)
	}
	return nil
}

// isNilOrEmptyString reports whether v is nil or an empty/whitespace
// string. Used by [rejectLaunchModelKeys] to treat null and "" as
// "not set" while still rejecting non-string values like numbers or
// objects, which would otherwise be silently dropped by JSON unmarshal
// when the target field is absent.
func isNilOrEmptyString(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) == ""
	}
	return false
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
// their behavior rather than guessing at an opaque object.
//
// `model` is not a field. Persistent model selection lives on
// `spec.profile.model`; mutate the stored spec via loop_definition_set,
// or set profile.model inside the spec passed to spawn_loop. See
// [rejectLaunchModelKeys] for the runtime backstop that surfaces a
// useful error if a caller sends `launch.model` anyway.
func loopLaunchOverrideProperties() map[string]any {
	return map[string]any{
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
		"routing_factors": map[string]any{
			"type":                 "object",
			"additionalProperties": map[string]any{"type": "string"},
			"description":          "Open-ended routing factors the model router consumes as scoring inputs. Well-known keys live in `internal/model/router` (quality_floor, mission, local_only, prefer_speed, model_preference, channel). Use named top-level launch fields (allowed_tools, max_iterations, etc.) for well-known overrides; routing_factors is for the router's softer signals. To pin a model, set spec.profile.model on the stored definition or in the spec passed to spawn_loop — not via routing_factors.",
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
			"description":          "Opaque string/string tags attached to the launched loop for correlation or audit. NOT used for routing, tools, budgets, or any runtime behavior. To pin a model, set spec.profile.model on the stored definition (or in the spec passed to spawn_loop). To override tools use \"allowed_tools\" / \"exclude_tools\". To override budgets use \"max_iterations\" / \"max_output_tokens\".",
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

// runningLoopByName resolves a live loop by definition name, preferring the
// always-wired runtime registry and falling back to the intent-tool deps for
// registry-only configurations (the same resolution loop_reparent uses).
// Returns nil when no live registry is wired — liveness is unknowable there
// and callers treat it as "not running".
func (r *Registry) runningLoopByName(name string) *looppkg.Loop {
	live := r.liveLoopRegistry
	if live == nil {
		live = r.loopIntentDeps.LiveRegistry
	}
	if live == nil {
		return nil
	}
	return live.GetByName(name)
}

// staleRunningLoopNotice is the model-facing sentence a definition-write
// result carries when the target loop was already running before the write
// and survived it: a running loop holds its launched-time config and does
// NOT re-read the spec on wake — changes apply only after a full relaunch.
// Saying so explicitly keeps the model from waiting for the next wake and
// concluding the change failed. One shared helper so every write surface
// (loop_definition_set, loop_definition_update, thane_loop_create replace)
// teaches the same contract and the wording cannot drift.
//
// Callers must gate on the SAME instance surviving the write (capture the
// live loop before, compare IDs after) — a loop spawned by the write's own
// reconcile is running the just-written spec and must not get this notice.
//
// Containers get their own recipe: a container with live children refuses
// stop_loop (ContainerHasChildrenError), so the two-step relaunch is taught
// with that prerequisite.
func staleRunningLoopNotice(name string, op looppkg.Operation) string {
	if op == looppkg.OperationContainer {
		return fmt.Sprintf("%q is currently running and keeps its launched-time config; changes from this write apply only after a full relaunch — NOT on the next wake. A container with live children cannot be stopped in place: reparent or stop its children first, then stop_loop and loop_definition_launch (or restart the process).", name)
	}
	return fmt.Sprintf("%q is currently running and keeps its launched-time config; changes from this write apply only after a full relaunch (stop_loop then loop_definition_launch, or process restart) — NOT on the loop's next wake. For scalar retunes (task, model, instructions, sleep envelope, supervisor, max_iter), loop_definition_update applies live without a relaunch.", name)
}

// retuneAppliedNotice confirms live conformance after a successful
// [looppkg.Loop.QueueRetune]: the stored spec and the running loop now
// agree, with the sole exception of a turn already in flight.
func retuneAppliedNotice(name string) string {
	return fmt.Sprintf("%q is running; the edit is applied live — the next turn embodies it (a turn already in flight finishes under its previous config, and an in-flight sleep re-clamps to an edited envelope, waking now if overdue).", name)
}

// reusedRunningLoopLaunchNotice is the launch-side counterpart: the launch
// short-circuited to an already-running durable loop instead of starting a
// new one, so a caller relaunching to apply a spec edit has not actually
// relaunched anything. This closes the drain race in the taught recipe —
// stop_loop returns ok after ~10s even if the loop is still draining, and
// the follow-up launch would otherwise be indistinguishable from a fresh
// start.
func reusedRunningLoopLaunchNotice(name string) string {
	return fmt.Sprintf("%q was already running; this launch returned the existing loop instead of starting a new one, and it keeps its launched-time config. To relaunch with the current stored definition, stop_loop and confirm the loop deregistered, then call loop_definition_launch again.", name)
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
