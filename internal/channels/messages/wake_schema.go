package messages

// LoopWakeTargetSchema returns the canonical JSON-schema description
// of the [LoopWakeTarget] wire shape, used by every source-specific
// subscription tool that lets the model declare an existing loop to
// receive event-source wakes (forge_repo_follow, media_follow,
// mqtt_wake_add, etc.). One definition site, one description style.
//
// description is the per-source phrasing that introduces the field;
// the rest of the schema (property names, enum values, priority
// description) is fixed so the model sees the same shape regardless
// of the source. Pass an empty string to use a generic fallback.
func LoopWakeTargetSchema(description string) map[string]any {
	if description == "" {
		description = "Existing loop to wake when this subscription fires. The target loop's next iteration sees the event in its pending notifications; no new loop is spawned."
	}
	return map[string]any{
		"type":        "object",
		"description": description,
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
				"description": "When true, force the target loop's next iteration to run as a supervisor turn (the more capable model with the augmented prompt). Costlier than a normal wake — reserve for signals that genuinely warrant the extra capacity.",
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
