package media

import (
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

func feedKeyWakeLoopID(id string) string          { return "feed:" + id + ":wake_loop_id" }
func feedKeyWakeLoopName(id string) string        { return "feed:" + id + ":wake_loop_name" }
func feedKeyWakeForceSupervisor(id string) string { return "feed:" + id + ":wake_force_supervisor" }
func feedKeyWakePriority(id string) string        { return "feed:" + id + ":wake_priority" }
func feedKeyWakeInstructions(id string) string    { return "feed:" + id + ":wake_instructions" }

func feedWakeKeys(id string) []string {
	return []string{
		feedKeyWakeLoopID(id),
		feedKeyWakeLoopName(id),
		feedKeyWakeForceSupervisor(id),
		feedKeyWakePriority(id),
		feedKeyWakeInstructions(id),
	}
}

func parseFeedWakeTarget(raw any) (messages.LoopWakeTarget, bool, error) {
	target, ok, err := messages.ParseLoopWakeTarget(raw)
	if err != nil {
		return messages.LoopWakeTarget{}, false, fmt.Errorf("wake_loop: %w", err)
	}
	return target, ok, nil
}

func storeFeedWakeTarget(state *opstate.Store, feedID string, target messages.LoopWakeTarget, configured bool) error {
	if !configured {
		return nil
	}
	values := map[string]string{
		feedKeyWakeLoopID(feedID):          target.LoopID,
		feedKeyWakeLoopName(feedID):        target.Name,
		feedKeyWakeForceSupervisor(feedID): fmt.Sprintf("%t", target.ForceSupervisor),
		feedKeyWakePriority(feedID):        string(target.Priority),
		feedKeyWakeInstructions(feedID):    target.Instructions,
	}
	for key, value := range values {
		if err := state.Set(feedNamespace, key, value); err != nil {
			return fmt.Errorf("store %s: %w", key, err)
		}
	}
	return nil
}

func loadFeedWakeTarget(state *opstate.Store, feedID string) (messages.LoopWakeTarget, bool, error) {
	loopID, err := state.Get(feedNamespace, feedKeyWakeLoopID(feedID))
	if err != nil {
		return messages.LoopWakeTarget{}, false, err
	}
	name, err := state.Get(feedNamespace, feedKeyWakeLoopName(feedID))
	if err != nil {
		return messages.LoopWakeTarget{}, false, err
	}
	if loopID == "" && name == "" {
		return messages.LoopWakeTarget{}, false, nil
	}
	forceRaw, err := state.Get(feedNamespace, feedKeyWakeForceSupervisor(feedID))
	if err != nil {
		return messages.LoopWakeTarget{}, false, err
	}
	priorityRaw, err := state.Get(feedNamespace, feedKeyWakePriority(feedID))
	if err != nil {
		return messages.LoopWakeTarget{}, false, err
	}
	instructions, err := state.Get(feedNamespace, feedKeyWakeInstructions(feedID))
	if err != nil {
		return messages.LoopWakeTarget{}, false, err
	}

	target, ok, err := messages.ParseLoopWakeTarget(map[string]any{
		"loop_id":          loopID,
		"name":             name,
		"force_supervisor": forceRaw == "true",
		"priority":         priorityRaw,
		"instructions":     instructions,
	})
	return target, ok, err
}

func feedWakeTargetDefinition() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Optional existing loop to wake when this feed has new entries. Use this to feed a thane_curate-managed document instead of starting the default media-analysis conversation.",
		"properties": map[string]any{
			"loop_id": map[string]any{
				"type":        "string",
				"description": "Exact live loop ID to signal. Preferred when available from loop_status.",
			},
			"name": map[string]any{
				"type":        "string",
				"description": "Exact live loop name to signal when loop_id is not known.",
			},
			"force_supervisor": map[string]any{
				"type":        "boolean",
				"description": "When true, force the target loop's next iteration to use supervisor routing.",
			},
			"priority": map[string]any{
				"type":        "string",
				"enum":        []string{"low", "normal", "urgent"},
				"description": "Delivery priority recorded on the loop notification. Default: normal.",
			},
			"instructions": map[string]any{
				"type":        "string",
				"description": "Compact source-specific instructions included with the wake event.",
			},
		},
	}
}

func feedWakeTargetJSON(target messages.LoopWakeTarget, ok bool) map[string]any {
	if !ok {
		return nil
	}
	out := map[string]any{
		"force_supervisor": target.ForceSupervisor,
		"priority":         target.Priority,
	}
	if target.LoopID != "" {
		out["loop_id"] = target.LoopID
	}
	if target.Name != "" {
		out["name"] = target.Name
	}
	if target.Instructions != "" {
		out["instructions"] = target.Instructions
	}
	return out
}
