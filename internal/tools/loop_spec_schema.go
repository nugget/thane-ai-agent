package tools

// loopSpecSchema returns the JSON-Schema description of the agent-facing
// loop-definition spec object, shared by loop_definition_set,
// loop_definition_lint, and spawn_loop. Until this existed those tools
// advertised the spec as a bare {"type":"object"}: every field below was
// already decoded by decodeLoopSpecArg's json.Unmarshal, but the model was
// taught none of them, so authoring a loop meant guessing the shape.
//
// The schema is deliberately ADVISORY — it documents the canonical surface
// without setting additionalProperties:false, so it never rejects the extra
// or legacy keys the decoder still accepts (e.g. the backwards-compat
// top-level quality_floor). It adds no new `required` fields beyond what the
// handlers already enforce, so existing valid calls keep validating. The
// description argument lets each tool frame the spec for its own verb.
//
// This is the single source of truth for the agent-facing spec surface: add
// a new authorable field here and all three tools advertise it.
func loopSpecSchema(description string) map[string]any {
	return map[string]any{
		"type":        "object",
		"description": description,
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Unique definition name. Required to save or launch.",
			},
			"enabled": map[string]any{
				"type":        "boolean",
				"description": "Whether the definition is eligible for runtime lifecycle management; service loops auto-start when enabled.",
			},
			"task": map[string]any{
				"type":        "string",
				"description": "Static prompt run each iteration.",
			},
			"intent": map[string]any{
				"type":        "string",
				"description": "One- or two-sentence statement of why this loop exists — its purpose, distinct from task. Surfaced as LoopView.intent across loop tools. Set this top-level field; do not put intent in metadata.",
			},
			"operation": map[string]any{
				"type":        "string",
				"enum":        []string{"request_reply", "background_task", "service", "container", "event_driven"},
				"description": "Runtime pattern. service = perpetual self-paced loop; event_driven = wakes only on subscription/tag events (no periodic sleep); background_task = detached one-shot; request_reply = synchronous; container = non-executing graph node.",
			},
			"profile": loopProfileSchema("Routing and request-shaping for normal (non-supervisor) iterations."),
			"supervisor": map[string]any{
				"type":        "boolean",
				"description": "Enable periodic supervisor turns: a per-wake Bernoulli trial promotes the iteration to a more capable, non-local model.",
			},
			"supervisor_prob": map[string]any{
				"type":        "number",
				"description": "Per-wake probability in [0.0, 1.0] of a supervisor turn when supervisor is true.",
			},
			"supervisor_profile": loopProfileSchema("Overlay applied on supervisor turns (e.g. a higher quality_floor and review-specific instructions). Any field set here wins over profile; unset fields fall back to profile."),
			"outputs": map[string]any{
				"type":        "array",
				"description": "Durable documents this loop maintains through scoped runtime tools (replace_output_*/append_output_*).",
				"items":       loopOutputSpecSchema(),
			},
			"tags": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Capability tags activated at iteration 0 (tool-registry scope, KB exposure); stay active across iterations unless deactivated.",
			},
			"exclude_tools": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Tool names to exclude from this loop. Direct human-egress tools are denied by default for subsystem loops — wake the core loop with request_core_attention instead.",
			},
			"sleep_min": map[string]any{
				"type":        "string",
				"description": "Minimum sleep between iterations as a duration string (e.g. \"15m\"); the loop cannot self-select a shorter sleep.",
			},
			"sleep_max": map[string]any{
				"type":        "string",
				"description": "Maximum sleep between iterations as a duration string (e.g. \"12h\").",
			},
			"sleep_default": map[string]any{
				"type":        "string",
				"description": "Initial sleep before the loop self-adjusts via set_next_sleep, as a duration string.",
			},
			"jitter": map[string]any{
				"type":        "number",
				"description": "Sleep randomization factor in [0.0, 1.0]; 0.2 varies sleep by ±20%, 0 = deterministic timing.",
			},
			"max_duration": map[string]any{
				"type":        "string",
				"description": "Maximum wall-clock lifetime as a duration string; empty = unbounded.",
			},
			"max_iter": map[string]any{
				"type":        "integer",
				"description": "Maximum iteration attempts; 0 = unbounded.",
			},
			"on_retrigger": map[string]any{
				"type":        "string",
				"enum":        []string{"single", "restart", "queue", "spawn"},
				"description": "Behavior when triggered again while running: single = ignore the new trigger; restart = cancel and restart; queue = run again after the current iteration; spawn = start another concurrent instance.",
			},
			"conditions": map[string]any{
				"type":        "object",
				"description": "Eligibility constraints; empty = always eligible unless blocked by policy.",
				"properties": map[string]any{
					"schedule": map[string]any{
						"type":        "object",
						"description": "Schedule constraint controlling the window in which the definition may run or launch.",
					},
				},
			},
			"completion": map[string]any{
				"type":        "string",
				"enum":        []string{"return", "conversation", "channel", "none"},
				"description": "Where results are delivered: return = to the caller; conversation = into the originating conversation; channel = to a channel; none = no outward delivery (the default for service loops). Mainly relevant for request_reply and detached background_task loops.",
			},
			"subscriptions": map[string]any{
				"type":        "array",
				"description": "Entities surfaced in context every iteration; the effective set unions every container ancestor's subscriptions (entries marked self_only stay out of descendants).",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"entity_id": map[string]any{"type": "string", "description": "Subscribed entity id (e.g. a Home Assistant entity), a glob, or an area:/label:/floor: target."},
						"history":   map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Optional history windows to include."},
						"forecast":  map[string]any{"type": "string", "enum": []string{"daily", "hourly", "twice_daily", "none"}, "description": "For weather.* entities, the Home Assistant forecast type to include."},
						"ttl_seconds": map[string]any{
							"type":        "integer",
							"description": "Optional time-to-live; the subscription is dropped after this many seconds.",
						},
						"mode":         map[string]any{"type": "string", "enum": []string{"render", "ingest", "both"}, "description": "What the subscription feeds: render (default) injects live state each iteration; ingest feeds the recent-state-changes window only; both does both."},
						"self_only":    map[string]any{"type": "boolean", "description": "On containers: true keeps this subscription out of descendant loops' inherited sets."},
						"requires_tag": map[string]any{"type": "string", "description": "Optional capability tag gating visibility: renders only while the tag is active. Render-only; not honored by ingest capture."},
					},
				},
			},
			"metadata": map[string]any{
				"type":                 "object",
				"description":          "Opaque string-keyed metadata stored with the definition. Keys here are not interpreted as structural spec fields — a metadata entry never sets a top-level field like parent_name. To nest this loop under a parent, use the top-level parent_name field, not a metadata entry.",
				"additionalProperties": map[string]any{"type": "string"},
			},
			"parent_name": map[string]any{
				"type":        "string",
				"description": "Durable name of the container to nest this loop under. The loop is placed beneath that node in the loop graph and inherits the container's tags and subscriptions. This is the author-time parent reference — a name, not an id — resolved to a live parent at launch. Today only container parents are honored; naming a non-container parent carries no inheritance. Omit for a top-level loop.",
			},
		},
	}
}

// loopProfileSchema describes a router.LoopProfile, used for both a loop's
// normal profile and its supervisor_profile overlay. The description is
// supplied by the caller to frame which of the two it is.
func loopProfileSchema(description string) map[string]any {
	return map[string]any{
		"type":        "object",
		"description": description,
		"properties": map[string]any{
			"model":             map[string]any{"type": "string", "description": "Pin a specific model as this loop's persistent baseline; omit to let the router choose."},
			"quality_floor":     map[string]any{"type": "integer", "description": "Minimum model quality rating (1–10) for selection."},
			"mission":           map[string]any{"type": "string", "description": "Mission/context profile name shaping prompt assembly."},
			"local_only":        map[string]any{"type": "string", "description": "\"true\"/\"false\" string — restrict routing to local models. Supervisor turns force this to false."},
			"delegation_gating": map[string]any{"type": "string", "description": "Set \"disabled\" to forbid this loop from delegating or spawning sub-work."},
			"prefer_speed":      map[string]any{"type": "string", "description": "\"true\"/\"false\" string — bias routing toward faster models."},
			"exclude_tools":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Profile-level tool exclusions, unioned with the spec-level exclude_tools."},
			"instructions":      map[string]any{"type": "string", "description": "Self-only prompt prefix prepended to the task (does not cascade to container children). On a supervisor_profile this is the supervisor-turn guidance."},
			"extra_hints":       map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Free-form string routing hints."},
		},
	}
}

// loopOutputSpecSchema describes one entry in a spec's outputs array.
func loopOutputSpecSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":           map[string]any{"type": "string", "description": "Stable semantic name for this output within the loop."},
			"type":           map[string]any{"type": "string", "enum": []string{"maintained_document", "journal_document"}, "description": "maintained_document = idempotent rewrite each cycle; journal_document = append-only dated entries."},
			"ref":            map[string]any{"type": "string", "description": "Managed document ref, e.g. \"core:metacognitive.md\" or \"kb:dashboards/x.md\". Stored verbatim — not resolved to content."},
			"mode":           map[string]any{"type": "string", "enum": []string{"replace", "append"}, "description": "Write mode; defaults from type when omitted (maintained→replace, journal→append)."},
			"purpose":        map[string]any{"type": "string", "description": "Optional model-facing guidance describing what this output is for."},
			"journal_window": map[string]any{"type": "string", "enum": []string{"day", "week", "month"}, "description": "Rolling window for journal outputs; empty uses the document-layer default."},
			"max_windows":    map[string]any{"type": "integer", "description": "Cap on retained journal windows; 0 uses the document-layer default."},
		},
	}
}
