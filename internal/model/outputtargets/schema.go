package outputtargets

import "fmt"

// Schema returns the JSON-Schema parameter object for a tool that fills
// this target's slots: one property per slot, typed and bounded by the
// slot's kind, with the required slots listed.
//
// The schema is advisory in the same sense as the loop spec schema — it
// does not set additionalProperties:false, because rejecting an unknown
// key silently at the provider layer teaches the model nothing.
// [Target.Normalize] rejects unknown keys with an error that names the
// valid slots instead.
func (t Target) Schema() map[string]any {
	properties := make(map[string]any, len(t.Slots))
	required := make([]string, 0, len(t.Slots))
	for _, slot := range t.Slots {
		properties[slot.Name] = slot.schema()
		if slot.Required {
			required = append(required, slot.Name)
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// schema returns the JSON-Schema fragment for one slot.
func (s Slot) schema() map[string]any {
	switch s.Kind {
	case SlotKindText:
		return map[string]any{
			"type":        "string",
			"maxLength":   s.MaxRunes,
			"description": fmt.Sprintf("%s Maximum %d characters — over-budget values are rejected, not truncated.", s.Description, s.MaxRunes),
		}
	case SlotKindFraction:
		return map[string]any{
			"type":        "number",
			"minimum":     0,
			"maximum":     1,
			"description": s.Description,
		}
	case SlotKindColor:
		return map[string]any{
			"type":        "string",
			"pattern":     "^#?[0-9A-Fa-f]{6}$",
			"description": s.Description + " A six-digit hex colour, case-insensitive, with or without a leading \"#\" — stored as \"#RRGGBB\".",
		}
	default:
		// Unreachable for registered targets: Target.validate rejects
		// unknown kinds before a target enters the registry. Kept so a
		// future kind that skips a schema case fails loudly in review
		// rather than advertising an untyped parameter to the model.
		return map[string]any{"description": s.Description}
	}
}

// ToolDescription returns the model-facing description for the
// request-scoped tool that fills this target, framed for one named
// output. The caller supplies the output name and the entity the payload
// lands on so the model can tell two declarations of the same target
// apart.
func (t Target) ToolDescription(outputName, entityID string) string {
	return fmt.Sprintf(
		"Set the loop-declared structured output %q, rendered as %s. %s Every call replaces the whole payload: slots you omit are cleared, so send the complete current state each time. Published to %s. %s",
		outputName, t.Title, t.Summary, entityID, t.Binding,
	)
}
