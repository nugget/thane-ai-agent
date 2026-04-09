package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
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
	return launch, nil
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
