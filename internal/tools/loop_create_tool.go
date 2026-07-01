package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// thane_loop_create is the Core, always-on front door for creating a durable
// loop (#1106 A). It replaces the cryptic thane_curate / thane_create_container
// verbs with one tool that takes an explicit operation and intent-shaped args,
// builds the right Spec, and creates + launches it through the same durable
// commit path the loop_* lifecycle tools use. Being Core, it's offered even when
// the `loops` capability isn't active — the trailhead into that lifecycle
// (inspect/edit/relaunch/reparent), which the model reaches via
// tag_activate("loops").

// registerThaneLoopCreate registers the Core loop-creation front door.
func (r *Registry) registerThaneLoopCreate() {
	r.Register(&Tool{
		Name: "thane_loop_create",
		Core: true,
		Description: "Create and launch a durable, reusable loop. This is the always-on front door for standing up recurring work; for the full lifecycle afterwards (inspect, edit, relaunch, reparent) activate the `loops` capability. " +
			"operation is explicit and picks the kind: " +
			"\"service\" = a recurring loop that self-paces within a sleep envelope (requires sleep_min and sleep_max); " +
			"\"event_driven\" = a loop that stays quiescent and wakes only when a subscribed entity changes (no sleep envelope); " +
			"\"container\" = a non-executing node that groups loops and shares its tags with descendants (takes only parent_name and tags). " +
			"output (service/event_driven only) declares a managed markdown document the loop maintains — \"journal\" appends a dated entry each cycle, \"maintain\" rewrites it idempotently — and is scaffolded with ownership frontmatter before launch; omit it for a loop that acts without maintaining a document. " +
			"parent_name nests the loop under a container by name, inheriting its tags and subscriptions. " +
			"entities are Home Assistant subscriptions surfaced into the loop's context each iteration (for event_driven, the entities whose changes wake it). " +
			"Returns the loop definition name, loop_id, and the canonical loop row; plus output_tool/document_path when a document was declared.",
		ContentResolveExempt: []string{
			"name", "intent", "operation", "parent_name", "output", "entities", "tags",
			"instructions", "sleep_min", "sleep_max", "sleep_default", "jitter",
			"quality_floor", "exclude_tools", "metadata", "replace",
		},
		Parameters: thaneLoopCreateSchema(),
		Handler:    r.handleThaneLoopCreate,
	})
}

func (r *Registry) handleThaneLoopCreate(ctx context.Context, args map[string]any) (string, error) {
	deps := r.loopIntentDeps
	if deps.Registry == nil || deps.LaunchDefinition == nil {
		return "", fmt.Errorf("thane_loop_create not configured: ConfigureLoopIntentTools must be called at startup")
	}

	name := toolargs.TrimmedString(args, "name")
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	intent := toolargs.TrimmedString(args, "intent")
	if intent == "" {
		return "", fmt.Errorf("intent is required")
	}

	operation := looppkg.Operation(toolargs.TrimmedString(args, "operation"))
	switch operation {
	case looppkg.OperationContainer:
		return r.createLoopContainer(ctx, args, name, intent)
	case looppkg.OperationService, looppkg.OperationEventDriven:
		return r.createLoopExecuting(ctx, args, name, intent, operation)
	default:
		return "", fmt.Errorf("operation must be one of \"service\", \"container\", \"event_driven\"; got %q", operation)
	}
}

// createLoopContainer builds and launches a container definition. Containers
// don't execute, so any execution/output arg is a mistake to surface loudly.
func (r *Registry) createLoopContainer(ctx context.Context, args map[string]any, name, intent string) (string, error) {
	if err := rejectArgsForOperation(args, looppkg.OperationContainer,
		"output", "entities", "instructions", "quality_floor", "exclude_tools",
		"sleep_min", "sleep_max", "sleep_default", "jitter", "metadata"); err != nil {
		return "", err
	}

	deps := r.loopIntentDeps
	parentName := toolargs.TrimmedString(args, "parent_name")
	replace, _ := args["replace"].(bool)
	tags := parseLoopCreateTags(args)

	if snap := deps.Registry.Snapshot(); snap != nil {
		if existing, ok := looppkg.FindDefinition(snap, name); ok {
			if existing.Source == looppkg.DefinitionSourceConfig {
				return "", (&looppkg.ImmutableDefinitionError{Name: name})
			}
			if !replace {
				return "", fmt.Errorf("loop definition %q already exists; pass replace=true to overwrite", name)
			}
			if existing.Spec.Operation != looppkg.OperationContainer {
				return "", fmt.Errorf("loop definition %q already exists as operation %q; refusing to convert it into a container", name, existing.Spec.Operation)
			}
		}
	}

	if err := r.resolveLoopParent(parentName); err != nil {
		return "", err
	}

	spec := looppkg.Spec{
		Name:       name,
		Enabled:    true,
		Operation:  looppkg.OperationContainer,
		Intent:     intent,
		Tags:       tags,
		ParentName: parentName,
	}
	if err := spec.ValidatePersistable(); err != nil {
		return "", fmt.Errorf("derived container spec invalid: %w", err)
	}

	launchResult, err := r.commitAndLaunchLoop(ctx, spec)
	if err != nil {
		return "", err
	}

	var parentID string
	if deps.LiveRegistry != nil {
		if running := deps.LiveRegistry.Get(launchResult.LoopID); running != nil {
			parentID = running.ParentID()
		}
	}

	result := map[string]any{
		"status":               "ok",
		"loop_definition_name": name,
		"loop_id":              launchResult.LoopID,
		"operation":            string(looppkg.OperationContainer),
		"parent_name":          parentName,
		"parent_loop_id":       parentID,
		"tags":                 tags,
	}
	r.attachCreatedLoopView(result, launchResult.LoopID)
	return ldMarshalToolJSON(result)
}

// createLoopExecuting builds and launches a service or event_driven loop. The
// service path requires a sleep envelope; the event_driven path rejects one (it
// has no timer). An output document is optional for both.
func (r *Registry) createLoopExecuting(ctx context.Context, args map[string]any, name, intent string, op looppkg.Operation) (string, error) {
	deps := r.loopIntentDeps
	if op == looppkg.OperationEventDriven {
		if err := rejectArgsForOperation(args, op, "sleep_min", "sleep_max", "sleep_default", "jitter"); err != nil {
			return "", err
		}
	}

	replace, _ := args["replace"].(bool)
	parentName := toolargs.TrimmedString(args, "parent_name")
	tags := parseLoopCreateTags(args)
	instructions := toolargs.TrimmedString(args, "instructions")

	qualityFloor, err := parseLoopCreateQualityFloor(args)
	if err != nil {
		return "", err
	}
	metadata, err := parseLoopCreateMetadata(args)
	if err != nil {
		return "", err
	}
	entities, err := parseEntityList("entities", args["entities"])
	if err != nil {
		return "", err
	}
	excludeTools := loopCreateExcludeTools(args)

	var envelope sleepEnvelope
	if op == looppkg.OperationService {
		envelope, err = parseSleepEnvelope(args)
		if err != nil {
			return "", err
		}
	}

	// Output document is optional. When present, scaffold it and declare the
	// managed OutputSpec + a curate-style task; otherwise the loop's per-iteration
	// task is its intent.
	var (
		outputs     []looppkg.OutputSpec
		outputSpec  looppkg.OutputSpec
		documentRef string
		outputMode  string
		title       string
		hasOutput   bool
	)
	if raw, ok := args["output"].(map[string]any); ok && raw != nil {
		hasOutput = true
		outputMode, _ = raw["mode"].(string)
		documentRef, _ = raw["document"].(string)
		if outputMode != "journal" && outputMode != "maintain" {
			return "", fmt.Errorf("output.mode must be \"journal\" or \"maintain\"")
		}
		if strings.TrimSpace(documentRef) == "" {
			return "", fmt.Errorf("output.document is required when output is set (e.g. \"kb:dashboards/foo.md\")")
		}
		title, _ = raw["title"].(string)
		if strings.TrimSpace(title) == "" {
			title = name
		}
		if deps.DocTools == nil {
			return "", fmt.Errorf("output document requested but the document store is not configured")
		}
		outputSpec = buildCurateOutputSpec(name, documentRef, outputMode, intent)
		outputs = []looppkg.OutputSpec{outputSpec}
	}

	if snap := deps.Registry.Snapshot(); snap != nil {
		if existing, ok := looppkg.FindDefinition(snap, name); ok {
			if existing.Source == looppkg.DefinitionSourceConfig {
				return "", (&looppkg.ImmutableDefinitionError{Name: name})
			}
			if !replace {
				return "", fmt.Errorf("loop definition %q already exists; pass replace=true to overwrite", name)
			}
		}
	}
	if err := r.resolveLoopParent(parentName); err != nil {
		return "", err
	}

	task := intent
	if hasOutput {
		task = buildCurateTask(intent, documentRef, outputMode, outputSpec.ToolName())
	}

	now := time.Now().UTC()
	spec := looppkg.Spec{
		Name:          name,
		Enabled:       true,
		Task:          task,
		Intent:        intent,
		Operation:     op,
		Tags:          tags,
		Outputs:       outputs,
		Subscriptions: curateEntitiesToSubscriptions(entities, now),
		ExcludeTools:  excludeTools,
		ParentName:    parentName,
		Profile: router.LoopProfile{
			DelegationGating: "disabled",
			QualityFloor:     qualityFloor,
			Instructions:     instructions,
		},
		Metadata: metadata,
	}
	if op == looppkg.OperationService {
		jitter := envelope.jitter
		spec.SleepMin = envelope.sleepMin
		spec.SleepMax = envelope.sleepMax
		spec.SleepDefault = envelope.sleepDefault
		spec.Jitter = &jitter
	}
	if err := spec.ValidatePersistable(); err != nil {
		return "", fmt.Errorf("derived spec invalid: %w", err)
	}
	warnings := looppkg.BuildDefinitionWarnings(spec)

	if hasOutput {
		if _, readErr := deps.DocTools.Read(ctx, documents.RefArgs{Ref: documentRef}); readErr == nil && !replace {
			return "", fmt.Errorf("output document %q already exists; pass replace=true to overwrite", documentRef)
		}
		body := renderScaffoldBody(outputMode, title, intent)
		frontmatter := map[string][]string{
			"loop_definition_name": {name},
			"loop_intent":          {intent},
			"output_mode":          {outputMode},
			"created":              {now.Format(time.RFC3339)},
		}
		if op == looppkg.OperationService {
			frontmatter["sleep_min"] = []string{envelope.sleepMin.String()}
			frontmatter["sleep_max"] = []string{envelope.sleepMax.String()}
		}
		if _, err := deps.DocTools.Write(ctx, documents.WriteArgs{
			Ref:         documentRef,
			Title:       title,
			Body:        &body,
			Frontmatter: frontmatter,
		}); err != nil {
			return "", fmt.Errorf("scaffold output document: %w", err)
		}
	}

	launchResult, err := r.commitAndLaunchLoop(ctx, spec)
	if err != nil {
		return "", err
	}

	result := map[string]any{
		"status":               "ok",
		"loop_definition_name": name,
		"loop_id":              launchResult.LoopID,
		"operation":            string(op),
		"parent_name":          parentName,
		"entity_subscriptions": len(entities),
		"warnings":             warnings,
	}
	if hasOutput {
		result["document_path"] = documentRef
		result["output_mode"] = outputMode
		result["output_tool"] = outputSpec.ToolName()
	}
	if op == looppkg.OperationService {
		result["sleep_envelope"] = map[string]any{
			"sleep_min":     envelope.sleepMin.String(),
			"sleep_max":     envelope.sleepMax.String(),
			"sleep_default": envelope.sleepDefault.String(),
			"jitter":        envelope.jitter,
		}
	}
	r.attachCreatedLoopView(result, launchResult.LoopID)
	return ldMarshalToolJSON(result)
}

// commitAndLaunchLoop commits a derived spec through the durable chokepoint (or
// the bare Upsert fallback) then launches it with an empty Launch so a retry
// short-circuits to the existing loop instead of tripping the running-loop guard.
func (r *Registry) commitAndLaunchLoop(ctx context.Context, spec looppkg.Spec) (looppkg.LaunchResult, error) {
	deps := r.loopIntentDeps
	updatedAt := time.Now().UTC()
	if deps.CommitSpec != nil {
		if err := deps.CommitSpec(ctx, spec, updatedAt); err != nil {
			return looppkg.LaunchResult{}, err
		}
	} else if err := deps.Registry.Upsert(spec, updatedAt); err != nil {
		return looppkg.LaunchResult{}, err
	}
	res, err := deps.LaunchDefinition(ctx, spec.Name, looppkg.Launch{})
	if err != nil {
		return looppkg.LaunchResult{}, fmt.Errorf("launch loop %q: %w", spec.Name, err)
	}
	return res, nil
}

// resolveLoopParent validates that a named parent container is registered before
// the child is committed. A blank parent is top-level and always fine.
func (r *Registry) resolveLoopParent(parentName string) error {
	if parentName == "" {
		return nil
	}
	deps := r.loopIntentDeps
	if deps.LiveRegistry == nil {
		return fmt.Errorf("parent_name set but the live registry is not configured; the tool cannot resolve container ancestry")
	}
	if parent := deps.LiveRegistry.GetByName(parentName); parent == nil {
		return fmt.Errorf("parent container %q is not currently registered; create it first or wait for hydration", parentName)
	}
	return nil
}

// attachCreatedLoopView adds the created loop's canonical LoopView to the result
// (#1106 B2), when the live registry can resolve it. Best-effort — a missing
// live registry just omits the field rather than failing the create.
func (r *Registry) attachCreatedLoopView(result map[string]any, loopID string) {
	if lv, ok := r.loopViewByID(loopID); ok {
		result["loop"] = lv
	}
}

// rejectArgsForOperation errors when any of keys is present for the given
// operation, so a mis-shaped call (e.g. an output document on a container) fails
// loudly rather than silently dropping the argument.
func rejectArgsForOperation(args map[string]any, op looppkg.Operation, keys ...string) error {
	for _, k := range keys {
		if v, present := args[k]; present && v != nil {
			return fmt.Errorf("%q is not valid for operation %q", k, op)
		}
	}
	return nil
}

func parseLoopCreateTags(args map[string]any) []string {
	var tags []string
	if rawTags, ok := args["tags"].([]any); ok {
		for _, t := range rawTags {
			if s, ok := t.(string); ok && strings.TrimSpace(s) != "" {
				tags = append(tags, strings.TrimSpace(s))
			}
		}
	}
	return tags
}

// parseLoopCreateQualityFloor fails fast when quality_floor is present-but-invalid
// rather than silently coercing to 0 and dropping the floor the caller intended.
func parseLoopCreateQualityFloor(args map[string]any) (int, error) {
	raw, present := args["quality_floor"]
	if !present || raw == nil {
		return 0, nil
	}
	n, ok := toolargs.IntOK(args, "quality_floor")
	if !ok {
		return 0, fmt.Errorf("quality_floor must be an integer, got %v", raw)
	}
	return n, nil
}

func parseLoopCreateMetadata(args map[string]any) (map[string]string, error) {
	raw, present := args["metadata"]
	if !present || raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("metadata must be an object with string values")
	}
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("metadata[%q] must be a string, got %T", k, v)
		}
		out[k] = s
	}
	return out, nil
}

// loopCreateExcludeTools layers operator-provided exclusions on top of the
// always-denied direct human-egress baseline (#696) — a true union that never
// shrinks the egress floor.
func loopCreateExcludeTools(args map[string]any) []string {
	excludeTools := DirectHumanEgressToolNames()
	seen := make(map[string]bool, len(excludeTools))
	for _, t := range excludeTools {
		seen[t] = true
	}
	for _, t := range toolargs.StringSlice(args, "exclude_tools") {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			seen[t] = true
			excludeTools = append(excludeTools, t)
		}
	}
	return excludeTools
}

func thaneLoopCreateSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Unique loop definition name (lowercase, snake_case). Shown in the loop graph and used as the parent reference for descendants.",
			},
			"intent": map[string]any{
				"type":        "string",
				"description": "One- or two-sentence statement of what this loop is for. Recorded as the loop's first-class intent and, for executing loops, used as the per-iteration task when no output document is declared.",
			},
			"operation": map[string]any{
				"type":        "string",
				"enum":        []string{"service", "container", "event_driven"},
				"description": "The kind of loop. service = recurring, self-paced within a sleep envelope; event_driven = quiescent until a subscribed entity changes (no sleep envelope); container = non-executing grouping node that shares tags/subscriptions with descendants.",
			},
			"parent_name": map[string]any{
				"type":        "string",
				"description": "Optional container definition name to nest this loop under. The loop inherits the container's tags and subscriptions. Omit for a top-level loop.",
			},
			"output": map[string]any{
				"type":        "object",
				"description": "Optional managed document this loop maintains (service/event_driven only). Scaffolded with ownership frontmatter before launch.",
				"properties": map[string]any{
					"mode": map[string]any{
						"type":        "string",
						"enum":        []string{"journal", "maintain"},
						"description": "journal = append a dated entry each cycle; maintain = idempotent rewrite each cycle.",
					},
					"document": map[string]any{
						"type":        "string",
						"description": "Managed-root document ref, e.g. \"kb:dashboards/pr-watchlist.md\" or \"core:journal/decisions.md\".",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "Optional human title for the document. Defaults to the loop name.",
					},
				},
				"required": []string{"mode", "document"},
			},
			"entities": map[string]any{
				"type":        "array",
				"description": "Optional Home Assistant entity subscriptions surfaced into the loop's context each iteration. For event_driven loops these are the entities whose changes wake the loop. Container ancestors' subscriptions also cascade in.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"entity_id": map[string]any{
							"type":        "string",
							"description": "Home Assistant entity ID (e.g. sensor.upstairs_temperature) or a glob (e.g. binary_sensor.*door*), re-expanded live each turn.",
						},
						"history": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "integer"},
							"description": "Optional historical context windows in seconds (e.g. [600, 3600, 86400]).",
						},
						"forecast": map[string]any{
							"type":        "string",
							"enum":        []string{"daily", "hourly", "twice_daily", "none"},
							"description": "For weather.* entities, the HA forecast type to include each turn.",
						},
						"ttl_seconds": map[string]any{
							"type":        "integer",
							"description": "Optional expiration in seconds; the subscription is auto-removed after it elapses.",
						},
						"include": EntityMetadataIncludeParameter(),
					},
					"required": []string{"entity_id"},
				},
			},
			"tags": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional capability tags. For containers, every descendant inherits them; for executing loops, they scope the loop's tool surface. Omit to use only core tags.",
			},
			"instructions": map[string]any{
				"type":        "string",
				"description": "Optional steering text prepended to every iteration's task (service/event_driven). Persists on the spec's profile and shows in loop_definition_get.",
			},
			"sleep_min": map[string]any{
				"type":        "string",
				"description": "service only, required: tightest interval between iterations (Go duration, e.g. \"5m\"). Floor at 1 minute; set_next_sleep can never wake sooner.",
			},
			"sleep_max": map[string]any{
				"type":        "string",
				"description": "service only, required: loosest interval between iterations (Go duration, e.g. \"6h\"). Must be >= sleep_min; equal values pin a fixed interval.",
			},
			"sleep_default": map[string]any{
				"type":        "string",
				"description": "service only, optional: initial sleep before the loop self-adjusts. Defaults to the midpoint of the envelope.",
			},
			"jitter": map[string]any{
				"type":        "number",
				"description": "service only, optional: sleep randomization factor in [0, 1]. Defaults to 0.1; 0 for deterministic timing.",
			},
			"quality_floor": map[string]any{
				"type":        "integer",
				"description": "Optional minimum model quality rating (1–10) for the loop's iterations (service/event_driven). Omit to let the router choose.",
			},
			"exclude_tools": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional additional tool denials, layered on top of the always-denied direct human-egress tools (they extend the denylist, never replace the floor).",
			},
			"metadata": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
				"description":          "Optional opaque string-keyed metadata stored on the loop definition (service/event_driven).",
			},
			"replace": map[string]any{
				"type":        "boolean",
				"description": "When true, overwrite an existing definition (and output document) of the same name. Default false; the tool refuses to clobber existing artifacts.",
			},
		},
		"required": []string{"name", "intent", "operation"},
	}
}
