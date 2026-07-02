package tools

import (
	"context"
	"fmt"
	"sort"
)

const haVocabularyTruncationNote = "Result exceeded the tool byte cap; narrow the target (one area or a few entities)."

// haVocabularyResult answers "what can I sense, check, and do" for one
// target: the purpose-specific trigger and condition identifiers plus
// the applicable services, in the exact domain.name form the
// automation config takes. This is the install's live vocabulary —
// integrations register their own triggers and conditions, so the set
// is discovered per call, never hardcoded.
type haVocabularyResult struct {
	Target     map[string]any `json:"target"`
	Triggers   []string       `json:"triggers"`
	Conditions []string       `json:"conditions"`
	Services   []string       `json:"services"`
	Note       string         `json:"note"`
}

const haVocabularyNote = "Use these in ha_automation_create: a purpose trigger is {\"trigger\": \"<identifier>\", \"target\": {...}} — same target shape as this call. ha_list_services has field detail for the services."

// registerHAAutomationVocabulary wires ha_automation_vocabulary: the
// discovery half of 2026.7 purpose-trigger authoring. The editor shows
// a human what a target can do; this shows the model the same live
// vocabulary, so automations get authored in intent terms ("motion in
// the office") instead of entity-state primitives.
func (r *Registry) registerHAAutomationVocabulary() {
	if r.ha == nil || !r.ha.HasWSClient() {
		return
	}
	r.Register(&Tool{
		Name: "ha_automation_vocabulary",
		Description: "Discover the automation vocabulary for a target: which purpose-specific triggers and conditions apply to it, and which services can act on it — the same building blocks Home Assistant's automation editor offers for that target. " +
			"Identifiers come back in the domain.name form automation configs take (e.g. light.turned_off, battery.became_low). " +
			"Call this before authoring an automation so triggers match what the install actually supports — integrations add their own, so the vocabulary varies per home.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target": map[string]any{
					"type":        "object",
					"description": "What to discover for: any of entity_id, device_id, area_id, floor_id, label_id (string or array each). Names resolve like ha_call_service targets.",
					"properties": map[string]any{
						"entity_id": map[string]any{"type": []string{"string", "array"}, "items": map[string]any{"type": "string"}},
						"device_id": map[string]any{"type": []string{"string", "array"}, "items": map[string]any{"type": "string"}},
						"area_id":   map[string]any{"type": []string{"string", "array"}, "items": map[string]any{"type": "string"}},
						"floor_id":  map[string]any{"type": []string{"string", "array"}, "items": map[string]any{"type": "string"}},
						"label_id":  map[string]any{"type": []string{"string", "array"}, "items": map[string]any{"type": "string"}},
					},
				},
			},
			"required": []string{"target"},
		},
		Handler: r.handleHAAutomationVocabulary,
	})
}

func (r *Registry) handleHAAutomationVocabulary(ctx context.Context, args map[string]any) (string, error) {
	if r.ha == nil {
		return "", fmt.Errorf("home assistant not configured")
	}
	if !r.ha.IsReady() {
		return "", fmt.Errorf("home assistant is currently unreachable (reconnecting in background)")
	}

	targetRaw, ok := args["target"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("target must be an object like {\"area_id\": \"office\"}")
	}
	resolution, err := r.resolveServiceTarget(ctx, targetRaw)
	if err != nil {
		return "", err
	}
	if resolution.Suggestion != "" {
		return resolution.Suggestion, nil
	}

	triggers, err := r.ha.GetTriggersForTarget(ctx, resolution.Resolved)
	if err != nil {
		return "", err
	}
	conditions, err := r.ha.GetConditionsForTarget(ctx, resolution.Resolved)
	if err != nil {
		return "", err
	}
	services, err := r.ha.GetServicesForTarget(ctx, resolution.Resolved)
	if err != nil {
		return "", err
	}
	sort.Strings(triggers)
	sort.Strings(conditions)
	sort.Strings(services)

	out := haVocabularyResult{
		Target:     resolution.Resolved,
		Triggers:   triggers,
		Conditions: conditions,
		Services:   services,
		Note:       haVocabularyNote,
	}
	if len(triggers) == 0 && len(conditions) == 0 && len(services) == 0 {
		out.Note = "Nothing applies to this target — it may contain no entities (or only hidden ones). Verify the target with ha_search_states."
	}
	return toIndentedJSONWithTruncationNote(out, haVocabularyTruncationNote), nil
}
