package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// LoopIntentToolDeps wires the loop-definition registry, document
// store, and launch helper into the intent-shaped loop creation tools.
//
// Intent-shaped tools form a thane_* family — verbs after the prefix
// carry the lifecycle:
//   - thane_now (sync delegate) — registered in app.initDelegation via
//     delegate.NowToolHandler; not part of this package.
//   - thane_assign (async one-shot delegate) — registered in
//     app.initDelegation via delegate.AssignToolHandler; not part of
//     this package.
//   - thane_curate (recurring service loop) — registered below.
//   - thane_create_container (durable, non-executing node that groups
//     loops and holds inheritable tags) — registered below.
//
// External wakes to live loops are infrastructural rather than
// tool-shaped: producer subsystems dispatch structured envelopes over
// messages.Bus directly, and request_core_attention covers
// loop → core/owner attention escalation.
//
// thane_curate constructs a Spec on the caller's behalf from intent-
// shaped inputs, then persists + launches through the same registry +
// reconcile + launch path used by loop_definition_set and
// loop_definition_launch.
type LoopIntentToolDeps struct {
	DocTools *documents.Tools
	Registry *looppkg.DefinitionRegistry
	// PersistSpec persists a spec without upsert/reconcile. Used by the
	// subscription-mutation path, which propagates to the live loop itself
	// rather than reconciling.
	PersistSpec func(looppkg.Spec, time.Time) error
	// CommitSpec durably commits a definition (persist + overlay upsert +
	// reconcile) in one step. thane_curate and thane_create_container route
	// through it instead of sequencing the steps by hand.
	CommitSpec       func(context.Context, looppkg.Spec, time.Time) error
	LaunchDefinition func(context.Context, string, looppkg.Launch) (looppkg.LaunchResult, error)
	// LiveRegistry resolves container parent names to live loop IDs and
	// propagates subscription mutations to running loops. Optional but
	// strongly recommended: when nil, thane_create_container only
	// supports top-level containers, and runtime subscription
	// mutations do not reflect on the live loop until restart.
	LiveRegistry *looppkg.Registry
}

// ConfigureLoopIntentTools registers the intent-shaped loop creation
// tools on the registry. The definition registry and LaunchDefinition
// helper are required for any tool in the family. thane_curate also
// needs the document store; when DocTools is nil, it is skipped while
// thane_create_container still registers (containers don't own
// documents). Missing the load-bearing pieces silently disables a
// tool rather than registering one that would panic at call time.
func (r *Registry) ConfigureLoopIntentTools(deps LoopIntentToolDeps) {
	if r == nil || deps.Registry == nil || deps.LaunchDefinition == nil {
		return
	}
	r.loopIntentDeps = deps
	if deps.DocTools != nil {
		r.registerThaneCurate()
	}
	r.registerThaneCreateContainer()
	r.registerUpdateEntitySubscriptions()
}

func (r *Registry) registerThaneCreateContainer() {
	r.Register(&Tool{
		Name: "thane_create_container",
		Description: "Create a durable container loop that groups related loops and provides inheritable capability tags to its descendants. " +
			"A container is a non-executing node in the loop graph — it never wakes, never runs a task, and produces no output. It exists to organize loops (like a folder), to hold capability tags every descendant inherits at iteration time, and to give the loop visualizer a stable structural node. " +
			"Use this when you have several loops that share context (e.g., \"home_automation\" containing curate loops for upstairs, downstairs, and the garage) and want them to all inherit a common tag set. " +
			"parent_name optionally nests this container inside another container by name; omit for a top-level container. " +
			"tags are the capability tags every descendant loop will see in addition to its own — leave empty if you only want the container for organization. " +
			"intent records the container's purpose so a future inspector knows what belongs here. " +
			"Returns the container's loop_id (use as parent_id when launching loops that should sit inside this container) and the definition name.",
		ContentResolveExempt: []string{"name", "intent", "parent_name", "tags", "replace"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Unique container loop definition name (lowercase, snake_case). Used as the parent reference for descendants and shown in the loop graph.",
				},
				"intent": map[string]any{
					"type":        "string",
					"description": "One- or two-sentence description of what this container groups and why it exists. Recorded on the container's metadata for future inspection.",
				},
				"parent_name": map[string]any{
					"type":        "string",
					"description": "Optional parent container's definition name. When set, this container is nested inside the named container at the current time of launch. Omit for a top-level container.",
				},
				"tags": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional capability tags inherited by every descendant loop. Each descendant turn sees the union of its own tags and every container ancestor's tags.",
				},
				"replace": map[string]any{
					"type":        "boolean",
					"description": "When true, overwrite an existing container definition of the same name. Default false; the tool refuses to clobber existing containers.",
				},
			},
			"required": []string{"name", "intent"},
		},
		Handler: r.handleThaneCreateContainer,
	})
}

func (r *Registry) handleThaneCreateContainer(ctx context.Context, args map[string]any) (string, error) {
	deps := r.loopIntentDeps
	if deps.Registry == nil || deps.LaunchDefinition == nil {
		return "", fmt.Errorf("thane_create_container not configured: ConfigureLoopIntentTools must be called at startup")
	}

	name := strings.TrimSpace(toolargs.TrimmedString(args, "name"))
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	intent := strings.TrimSpace(toolargs.TrimmedString(args, "intent"))
	if intent == "" {
		return "", fmt.Errorf("intent is required")
	}
	parentName := strings.TrimSpace(toolargs.TrimmedString(args, "parent_name"))
	replace, _ := args["replace"].(bool)

	var tags []string
	if rawTags, ok := args["tags"].([]any); ok {
		for _, t := range rawTags {
			if s, ok := t.(string); ok && strings.TrimSpace(s) != "" {
				tags = append(tags, strings.TrimSpace(s))
			}
		}
	}

	// Refuse to clobber existing definition unless replace=true. The
	// snapshot read here mirrors handleThaneCurate; the path is duplicated
	// rather than abstracted because the surrounding handler shape is
	// shallow and an extracted helper would obscure the refusal flow.
	if snap := deps.Registry.Snapshot(); snap != nil {
		if existing, ok := looppkg.FindDefinition(snap, name); ok {
			if existing.Source == looppkg.DefinitionSourceConfig {
				return "", (&looppkg.ImmutableDefinitionError{Name: name})
			}
			if !replace {
				return "", fmt.Errorf("loop definition %q already exists; pass replace=true to overwrite", name)
			}
			if existing.Spec.Operation != looppkg.OperationContainer {
				return "", fmt.Errorf("loop definition %q already exists as operation %q; refusing to convert into a container", name, existing.Spec.Operation)
			}
		}
	}

	if parentName != "" {
		if deps.LiveRegistry == nil {
			return "", fmt.Errorf("parent_name set but live registry is not configured; tool cannot resolve container ancestry")
		}
		if parent := deps.LiveRegistry.GetByName(parentName); parent == nil {
			return "", fmt.Errorf("parent container %q is not currently registered; create it first or wait for hydration", parentName)
		}
	}

	spec := looppkg.Spec{
		Name:       name,
		Enabled:    true,
		Operation:  looppkg.OperationContainer,
		Tags:       tags,
		ParentName: parentName,
		Metadata: map[string]string{
			"intent": intent,
		},
	}
	if err := spec.ValidatePersistable(); err != nil {
		return "", fmt.Errorf("derived container spec invalid: %w", err)
	}

	updatedAt := time.Now().UTC()
	if deps.CommitSpec != nil {
		if err := deps.CommitSpec(ctx, spec, updatedAt); err != nil {
			return "", err
		}
	} else if err := deps.Registry.Upsert(spec, updatedAt); err != nil {
		return "", err
	}

	// Launch with an empty Launch so a retry of this tool against an
	// already-running container short-circuits to the existing loop ID
	// instead of tripping the running-durable-loop-with-overrides guard.
	// The parent relationship is on the spec via ParentName and resolved
	// at hydration time, so the launch payload stays free of overrides.
	launchResult, err := deps.LaunchDefinition(ctx, name, looppkg.Launch{})
	if err != nil {
		return "", fmt.Errorf("launch container: %w", err)
	}

	var parentID string
	if deps.LiveRegistry != nil {
		if running := deps.LiveRegistry.Get(launchResult.LoopID); running != nil {
			parentID = running.ParentID()
		}
	}

	return ldMarshalToolJSON(map[string]any{
		"status":               "ok",
		"loop_definition_name": name,
		"loop_id":              launchResult.LoopID,
		"operation":            string(looppkg.OperationContainer),
		"parent_name":          parentName,
		"parent_loop_id":       parentID,
		"tags":                 tags,
	})
}

func (r *Registry) registerThaneCurate() {
	r.Register(&Tool{
		Name: "thane_curate",
		Description: "Create and launch a recurring service loop that curates a managed markdown collection. " +
			"Output-first: the target document (kb:, core:, scratchpad:, generated:) is scaffolded with frontmatter recording loop ownership before the loop is registered, so the loop's identity and intent are self-describing on disk. " +
			"Two output modes today: \"journal\" appends a dated entry each cycle (research notes, decision logs, daily digests); \"maintain\" rewrites the document idempotently each cycle (dashboards, current-state snapshots). " +
			"Future modes will accept a directory ref for tree-shaped collections (multiple files maintained as a structured corpus); the output parameter shape will grow additively. " +
			"Sleep envelope: pass sleep_min and sleep_max as Go duration strings (\"5m\", \"30m\", \"1h\"). The running loop uses set_next_sleep to self-pace within those bounds — pick them to match the topic's metabolism (tight when busy work deserves quick checks, loose when quiet periods should cost nothing). sleep_default and jitter are optional with sensible defaults. " +
			"Tags scope the loop's tools; omit to inherit the core tag set. " +
			"Entities is a list of Home Assistant entity subscriptions the loop should see every iteration; they are persisted on the loop's own spec and surfaced into the loop's prompt automatically. Container ancestors' subscriptions also cascade in. " +
			"Returns the document ref, loop definition name, loop_id, output mode, the generated output tool name (output_tool) the receiving loop writes through, the count of declared entity subscriptions, and the resolved sleep envelope.",
		ContentResolveExempt: []string{"name", "intent", "sleep_min", "sleep_max", "sleep_default", "jitter", "tags", "instructions", "output", "entities", "replace"},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Unique loop definition name (lowercase, snake_case). The same name is recorded in the output document's frontmatter as loop_definition_name.",
				},
				"intent": map[string]any{
					"type":        "string",
					"description": "One- or two-sentence description of what the loop tracks, why it exists, and what the document should contain. The model running each iteration sees this in its task prompt.",
				},
				"sleep_min": map[string]any{
					"type":        "string",
					"description": "Tightest interval between iterations (Go duration: \"5m\", \"30m\", \"1h\"). Floor at 1 minute. The loop's set_next_sleep can never wake sooner than this.",
				},
				"sleep_max": map[string]any{
					"type":        "string",
					"description": "Loosest interval between iterations (Go duration: \"30m\", \"6h\"). The loop's set_next_sleep can never sleep longer than this. Must be >= sleep_min; equal values pin a fixed interval.",
				},
				"sleep_default": map[string]any{
					"type":        "string",
					"description": "Optional initial sleep duration for the first wake. Defaults to the midpoint of sleep_min and sleep_max. Must lie within the envelope.",
				},
				"jitter": map[string]any{
					"type":        "number",
					"description": "Optional sleep randomization factor in [0, 1]. Defaults to 0.1. Set to 0 for deterministic timing.",
				},
				"output": map[string]any{
					"type":        "object",
					"description": "Output target the loop maintains. Required.",
					"properties": map[string]any{
						"mode": map[string]any{
							"type":        "string",
							"enum":        []string{"journal", "maintain"},
							"description": "journal = append a dated entry each cycle; maintain = idempotent rewrite each cycle.",
						},
						"document": map[string]any{
							"type":        "string",
							"description": "Managed-root document ref like \"kb:dashboards/pr-watchlist.md\" or \"core:journal/decisions.md\".",
						},
						"title": map[string]any{
							"type":        "string",
							"description": "Optional human title for the document. Defaults to the loop name.",
						},
					},
					"required": []string{"mode", "document"},
				},
				"tags": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional capability tags scoping the loop's tool surface. Omit to use only core tags.",
				},
				"entities": map[string]any{
					"type":        "array",
					"description": "Optional Home Assistant entity subscriptions the loop should see in context every iteration. Each item declares an entity_id with optional history windows (seconds, e.g. [3600, 86400] for 1h and 1d summaries), weather forecast type for weather.* entities, and a ttl_seconds expiration. Stored on the loop's own spec; if the loop has a container parent, subscriptions declared on ancestor containers also appear at iteration time.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"entity_id": map[string]any{
								"type":        "string",
								"description": "The Home Assistant entity ID to watch (e.g., sensor.upstairs_temperature, weather.home), or a glob pattern (e.g., binary_sensor.*door*, *_temperature) to watch every matching entity, re-expanded live each turn (capped per turn).",
							},
							"history": map[string]any{
								"type":        "array",
								"items":       map[string]any{"type": "integer"},
								"description": "Optional historical context windows in seconds. Each window adds a compact summary of past state to context (e.g., [600, 3600, 86400] for 10min/1hr/1day).",
							},
							"forecast": map[string]any{
								"type":        "string",
								"enum":        []string{"daily", "hourly", "twice_daily", "none"},
								"description": "For weather.* entities, fetch this HA forecast type each turn and include the compact forecast in context.",
							},
							"ttl_seconds": map[string]any{
								"type":        "integer",
								"description": "Optional expiration in seconds. After this TTL elapses, the subscription is auto-removed from future context injection.",
							},
							"include": EntityMetadataIncludeParameter(),
						},
						"required": []string{"entity_id"},
					},
				},
				"instructions": map[string]any{
					"type":        "string",
					"description": "Optional steering text prepended to every iteration's task (output format guidance, what to focus on, what to skip). Persists on the spec's Profile and shows up in loop_definition_get.",
				},
				"replace": map[string]any{
					"type":        "boolean",
					"description": "When true, overwrite an existing definition or document of the same name/ref. Default false; the tool refuses to clobber existing artifacts.",
				},
			},
			"required": []string{"name", "intent", "sleep_min", "sleep_max", "output"},
		},
		Handler: r.handleThaneCurate,
	})
}

func (r *Registry) handleThaneCurate(ctx context.Context, args map[string]any) (string, error) {
	deps := r.loopIntentDeps
	if deps.DocTools == nil || deps.Registry == nil {
		return "", fmt.Errorf("thane_curate not configured: ConfigureLoopIntentTools must be called at startup")
	}

	name, _ := args["name"].(string)
	intent, _ := args["intent"].(string)
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("name is required")
	}
	if strings.TrimSpace(intent) == "" {
		return "", fmt.Errorf("intent is required")
	}

	envelope, err := parseSleepEnvelope(args)
	if err != nil {
		return "", err
	}

	output, _ := args["output"].(map[string]any)
	if output == nil {
		return "", fmt.Errorf("output is required (with mode and document)")
	}
	outputMode, _ := output["mode"].(string)
	documentRef, _ := output["document"].(string)
	if outputMode != "journal" && outputMode != "maintain" {
		return "", fmt.Errorf("output.mode must be \"journal\" or \"maintain\"")
	}
	if strings.TrimSpace(documentRef) == "" {
		return "", fmt.Errorf("output.document is required (e.g. \"kb:dashboards/foo.md\")")
	}
	title, _ := output["title"].(string)
	if strings.TrimSpace(title) == "" {
		title = name
	}

	var tags []string
	if rawTags, ok := args["tags"].([]any); ok {
		for _, t := range rawTags {
			if s, ok := t.(string); ok && strings.TrimSpace(s) != "" {
				tags = append(tags, s)
			}
		}
	}
	instructions, _ := args["instructions"].(string)
	replace, _ := args["replace"].(bool)

	entities, err := parseEntityList("entities", args["entities"])
	if err != nil {
		return "", err
	}

	// Refuse to clobber an existing definition unless replace=true.
	// Read directly from deps.Registry rather than going through
	// currentLoopDefinitionSnapshot, because the latter checks
	// r.loopDefinitionRegistry (set by ConfigureLoopDefinitionTools)
	// rather than the registry handle this tool was configured with.
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

	outputSpec := buildCurateOutputSpec(name, documentRef, outputMode, intent)

	now := time.Now().UTC()
	jitterRatio := envelope.jitter
	spec := looppkg.Spec{
		Name:          name,
		Enabled:       true,
		Task:          buildCurateTask(intent, documentRef, outputMode, outputSpec.ToolName()),
		Operation:     looppkg.OperationService,
		SleepMin:      envelope.sleepMin,
		SleepMax:      envelope.sleepMax,
		SleepDefault:  envelope.sleepDefault,
		Jitter:        &jitterRatio,
		Tags:          tags,
		Outputs:       []looppkg.OutputSpec{outputSpec},
		Subscriptions: curateEntitiesToSubscriptions(entities, now),
		Profile: router.LoopProfile{
			DelegationGating: "disabled",
			Instructions:     strings.TrimSpace(instructions),
		},
	}
	if err := spec.ValidatePersistable(); err != nil {
		return "", fmt.Errorf("derived spec invalid: %w", err)
	}
	warnings := looppkg.BuildDefinitionWarnings(spec)

	// Refuse to clobber an existing document unless replace=true. The
	// document store's Write replaces unconditionally, so we have to
	// preflight here. doc_read returns a non-nil error when the
	// document doesn't exist; an empty error means it does.
	if _, readErr := deps.DocTools.Read(ctx, documents.RefArgs{Ref: documentRef}); readErr == nil && !replace {
		return "", fmt.Errorf("output document %q already exists; pass replace=true to overwrite", documentRef)
	}

	// Scaffold the output document. Frontmatter records loop ownership
	// so a future inspector can identify the doc as loop-managed
	// without consulting the registry.
	body := renderScaffoldBody(outputMode, title, intent)
	frontmatter := map[string][]string{
		"loop_definition_name": {name},
		"loop_intent":          {intent},
		"output_mode":          {outputMode},
		"sleep_min":            {envelope.sleepMin.String()},
		"sleep_max":            {envelope.sleepMax.String()},
		"created":              {now.Format(time.RFC3339)},
	}
	docResult, err := deps.DocTools.Write(ctx, documents.WriteArgs{
		Ref:         documentRef,
		Title:       title,
		Body:        &body,
		Frontmatter: frontmatter,
	})
	if err != nil {
		return "", fmt.Errorf("scaffold output document: %w", err)
	}
	_ = docResult // discard structured result; we surface the ref directly

	// Commit (persist + upsert + reconcile) then launch. Mirrors
	// handleLoopDefinitionSet + handleLoopDefinitionLaunch; collapsed here
	// so the model only sees one round-trip for the intent.
	updatedAt := now
	if deps.CommitSpec != nil {
		if err := deps.CommitSpec(ctx, spec, updatedAt); err != nil {
			return "", err
		}
	} else if err := deps.Registry.Upsert(spec, updatedAt); err != nil {
		return "", err
	}

	launchResult, err := deps.LaunchDefinition(ctx, name, looppkg.Launch{})
	if err != nil {
		return "", fmt.Errorf("launch loop: %w", err)
	}

	return ldMarshalToolJSON(map[string]any{
		"status":               "ok",
		"document_path":        documentRef,
		"loop_definition_name": name,
		"loop_id":              launchResult.LoopID,
		"output_mode":          outputMode,
		"output_tool":          outputSpec.ToolName(),
		"entity_subscriptions": len(entities),
		"sleep_envelope": map[string]any{
			"sleep_min":     envelope.sleepMin.String(),
			"sleep_max":     envelope.sleepMax.String(),
			"sleep_default": envelope.sleepDefault.String(),
			"jitter":        envelope.jitter,
		},
		"warnings": warnings,
	})
}

// mutateLoopSubscriptions reads the current spec for loopName, calls
// mutate to compute the next subscriptions, persists the updated
// spec, and propagates the change to the live loop if one is
// registered. Used by watch_entity, unwatch_entity, and
// update_entity_subscriptions so runtime changes to subscriptions
// survive restart and take effect on the next iteration.
//
// Returns the resulting subscription list (after mutation) along
// with any error. Mutators that need to compute removed/added
// deltas should diff against the pre-mutation list inside their
// closure.
func (r *Registry) mutateLoopSubscriptions(ctx context.Context, loopName string, mutate func([]looppkg.EntitySubscription) ([]looppkg.EntitySubscription, error)) ([]looppkg.EntitySubscription, error) {
	deps := r.loopIntentDeps
	if deps.Registry == nil {
		return nil, fmt.Errorf("loop definition registry not configured")
	}
	snap := deps.Registry.Snapshot()
	existing, ok := looppkg.FindDefinition(snap, loopName)
	if !ok {
		return nil, (&looppkg.UnknownDefinitionError{Name: loopName})
	}
	if existing.Source == looppkg.DefinitionSourceConfig {
		return nil, (&looppkg.ImmutableDefinitionError{Name: loopName})
	}

	next, err := mutate(existing.Spec.Subscriptions)
	if err != nil {
		return nil, err
	}
	newSpec := existing.Spec
	newSpec.Subscriptions = next

	updatedAt := time.Now().UTC()
	if deps.PersistSpec != nil {
		if err := deps.PersistSpec(newSpec, updatedAt); err != nil {
			return nil, fmt.Errorf("persist loop definition: %w", err)
		}
	}
	if err := deps.Registry.Upsert(newSpec, updatedAt); err != nil {
		return nil, err
	}
	if deps.LiveRegistry != nil {
		if live := deps.LiveRegistry.GetByName(loopName); live != nil {
			live.SetSubscriptions(next)
		}
	}
	// ctx reserved for future async hooks (e.g. notifying a watcher).
	_ = ctx
	return next, nil
}

// curateEntitiesToSubscriptions converts the parsed thane_curate
// entity input into the typed [looppkg.EntitySubscription] form
// persisted on Spec.Subscriptions. AddedAt is stamped at creation
// so TTL countdown is meaningful from the moment the loop is
// launched; without it [EntitySubscription.IsExpired] treats the
// row as immortal regardless of TTL. Returns nil for an empty
// input so the spec field stays empty rather than carrying an
// allocated zero-length slice.
func curateEntitiesToSubscriptions(entities []curateEntity, addedAt time.Time) []looppkg.EntitySubscription {
	if len(entities) == 0 {
		return nil
	}
	out := make([]looppkg.EntitySubscription, 0, len(entities))
	for _, e := range entities {
		out = append(out, looppkg.EntitySubscription{
			EntityID:   e.EntityID,
			History:    append([]int(nil), e.History...),
			Forecast:   e.Forecast,
			Include:    EntityMetadataIncludesPointer(e.Include),
			TTLSeconds: e.TTLSeconds,
			AddedAt:    addedAt,
		})
	}
	return out
}

// curateEntity is the parsed shape of one element from the thane_curate
// "entities" parameter. Fields mirror the watchlist subscription options.
type curateEntity struct {
	EntityID   string
	History    []int
	Forecast   string
	Include    homeassistant.EntityMetadataIncludes
	TTLSeconds int
}

// parseEntityList decodes an entity-subscription array into a typed
// list. fieldName is the caller-facing name of the parameter
// (e.g. "entities" for thane_curate, "add" for
// update_entity_subscriptions) and is woven into every error message
// so the model can see which argument failed validation.
// Empty/missing returns nil. Invalid shapes return an actionable
// error per the model-facing-tools doctrine.
func parseEntityList(fieldName string, raw any) ([]curateEntity, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of objects", fieldName)
	}
	if len(list) == 0 {
		return nil, nil
	}
	out := make([]curateEntity, 0, len(list))
	seen := make(map[string]bool, len(list))
	for i, item := range list {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be an object with at least an entity_id", fieldName, i)
		}
		entityID, _ := obj["entity_id"].(string)
		entityID = strings.TrimSpace(entityID)
		if entityID == "" {
			return nil, fmt.Errorf("%s[%d].entity_id is required", fieldName, i)
		}
		if err := homeassistant.ValidateEntityTarget(entityID); err != nil {
			return nil, fmt.Errorf("%s[%d].entity_id %q: invalid glob pattern: %w", fieldName, i, entityID, err)
		}
		if seen[entityID] {
			return nil, fmt.Errorf("%s[%d] duplicates entity_id %q; each entity may appear at most once", fieldName, i, entityID)
		}
		seen[entityID] = true

		ent := curateEntity{EntityID: entityID}
		if rawHistory, ok := obj["history"].([]any); ok {
			for j, h := range rawHistory {
				n, err := coerceInt(h)
				if err != nil {
					return nil, fmt.Errorf("%s[%d].history[%d]: %w", fieldName, i, j, err)
				}
				if n <= 0 {
					return nil, fmt.Errorf("%s[%d].history[%d]: window seconds must be > 0", fieldName, i, j)
				}
				ent.History = append(ent.History, n)
			}
		}
		if rawForecast, present := obj["forecast"]; present && rawForecast != nil {
			forecast, ok := rawForecast.(string)
			if !ok {
				return nil, fmt.Errorf("%s[%d].forecast: must be a string, got %T", fieldName, i, rawForecast)
			}
			normalized, err := normalizeForecast(forecast)
			if err != nil {
				return nil, fmt.Errorf("%s[%d].forecast: %w", fieldName, i, err)
			}
			ent.Forecast = normalized
		}
		if rawTTL, present := obj["ttl_seconds"]; present {
			ttl, err := coerceInt(rawTTL)
			if err != nil {
				return nil, fmt.Errorf("%s[%d].ttl_seconds: %w", fieldName, i, err)
			}
			if ttl < 0 {
				return nil, fmt.Errorf("%s[%d].ttl_seconds: must be >= 0", fieldName, i)
			}
			ent.TTLSeconds = ttl
		}
		include, err := ParseEntityMetadataIncludesArg(obj["include"], fmt.Sprintf("%s[%d].include", fieldName, i))
		if err != nil {
			return nil, err
		}
		ent.Include = include
		out = append(out, ent)
	}
	return out, nil
}

// normalizeForecast validates a forecast value at the tool boundary
// so invalid strings can't reach the renderer. "none" and empty
// collapse to "" (no forecast fetch); the three real types pass
// through unchanged; anything else is an actionable error. Exposed
// for the app-side watch_entity handler too, since both surfaces
// land in Spec.Subscriptions.Forecast and downstream rendering
// treats any non-empty value as a real HA forecast type.
func normalizeForecast(raw string) (string, error) {
	v := strings.TrimSpace(raw)
	switch v {
	case "", "none":
		return "", nil
	case "daily", "hourly", "twice_daily":
		return v, nil
	default:
		return "", fmt.Errorf("must be one of [daily, hourly, twice_daily, none], got %q", raw)
	}
}

// NormalizeForecast is the exported form of [normalizeForecast] used
// by the app-side runtime tools that also persist subscriptions.
// Kept thin so the two surfaces share one source of truth for what
// is and isn't a valid forecast value.
func NormalizeForecast(raw string) (string, error) {
	return normalizeForecast(raw)
}

func coerceInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		// JSON decoders deliver every number as float64; reject any
		// fractional value so callers see an actionable error instead
		// of a silent truncation (e.g. 3600.5 quietly becoming 3600).
		if n != float64(int64(n)) {
			return 0, fmt.Errorf("must be an integer, got fractional value %v", n)
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("must be an integer, got %T", v)
	}
}

// buildCurateOutputSpec converts the intent-shaped output argument into
// a declared OutputSpec on the loop. Once declared, the hydration layer
// generates a scoped mutation tool (replace_output_* / append_output_*)
// and injects current-document context into each iteration prompt — so
// the model gets a typed write surface and "what's already there?"
// answered without re-reading the doc itself.
func buildCurateOutputSpec(name, docRef, outputMode, intent string) looppkg.OutputSpec {
	out := looppkg.OutputSpec{
		Name:    name,
		Ref:     docRef,
		Purpose: intent,
	}
	switch outputMode {
	case "journal":
		out.Type = looppkg.OutputTypeJournalDocument
		out.Mode = looppkg.OutputModeAppend
	case "maintain":
		out.Type = looppkg.OutputTypeMaintainedDocument
		out.Mode = looppkg.OutputModeReplace
	}
	return out
}

// buildCurateTask renders the per-iteration task prompt for a
// thane_curate-created loop. The model running each iteration sees the
// intent, the document target, the output mode, and the scoped output
// tool name. Caller-supplied steering text lives on
// [router.LoopProfile.Instructions] and is prepended during task-turn
// construction (see [loop.Loop.buildTaskTurn]), so it doesn't appear
// here. Kept short and shape-clear so the model can act without
// re-reading the loop's own definition.
func buildCurateTask(intent, docRef, outputMode, outputToolName string) string {
	var verb string
	switch outputMode {
	case "journal":
		verb = "Append a dated entry to"
	case "maintain":
		verb = "Update the body of"
	default:
		verb = "Update"
	}
	var sb strings.Builder
	sb.WriteString(intent)
	sb.WriteString("\n\n")
	sb.WriteString(verb)
	sb.WriteString(" ")
	sb.WriteString(docRef)
	sb.WriteString(" with the current state. Write through the declared output tool ")
	sb.WriteString(outputToolName)
	sb.WriteString(". ")
	switch outputMode {
	case "journal":
		// Append-only: the recent tail in the context block is enough; the
		// model never needs the full history to write a new entry.
		sb.WriteString("Recent entries are surfaced in the Declared Durable Outputs context block above; no separate read is needed before appending.")
	case "maintain":
		// Complete-replacement: the context block shows the document head,
		// possibly truncated at 16 KiB (see loopOutputContentBytes in
		// app.loop_outputs). The model MUST notice the `truncated` flag and
		// read the full document before replacing, or it will silently drop
		// everything past the truncation boundary.
		sb.WriteString("The current document body is shown in the Declared Durable Outputs context block above. If that entry is marked `truncated: true`, read the full document with doc_read before replacing — the output tool overwrites the entire body.")
	default:
		sb.WriteString("The document's current contents are surfaced in the Declared Durable Outputs context block above.")
	}
	return sb.String()
}

// renderScaffoldBody returns the initial markdown body for the output
// document. Two templates today (journal, maintain); the Phase 2/3
// rollout will add investigation, digest, and freeform.
func renderScaffoldBody(outputMode, title, intent string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n", title)
	fmt.Fprintf(&sb, "*%s*\n\n", intent)
	switch outputMode {
	case "journal":
		sb.WriteString("*This document is maintained by a service loop. Each cycle appends a dated entry below.*\n\n")
		sb.WriteString("## Entries\n\n")
		sb.WriteString("_(awaiting first cycle)_\n")
	case "maintain":
		sb.WriteString("*This document is maintained by a service loop. Each cycle rewrites the body to reflect the current snapshot.*\n\n")
		sb.WriteString("## Current State\n\n")
		sb.WriteString("_(awaiting first cycle)_\n")
	}
	return sb.String()
}

// sleepEnvelope captures the sleep_min/max/default/jitter quartet that
// shapes a service loop's sleep behavior. The fields mirror the
// corresponding [looppkg.Spec] fields; this struct centralizes the
// validation/defaulting that turns raw tool input into a known-good
// envelope.
type sleepEnvelope struct {
	sleepMin     time.Duration
	sleepMax     time.Duration
	sleepDefault time.Duration
	jitter       float64
}

// parseSleepEnvelope reads sleep_min, sleep_max, sleep_default, and
// jitter from the tool args and returns a validated envelope. sleep_min
// and sleep_max are required Go duration strings (>= 1 minute, with
// sleep_min <= sleep_max). sleep_default defaults to the midpoint of
// the envelope; jitter defaults to 0.1. Returns a single error per
// failure, so the caller surfaces the first problem to the model
// without piling on cascading complaints.
func parseSleepEnvelope(args map[string]any) (sleepEnvelope, error) {
	sleepMin, presentMin, err := parseDurationArg(args, "sleep_min")
	if err != nil {
		return sleepEnvelope{}, err
	}
	if !presentMin {
		return sleepEnvelope{}, fmt.Errorf("sleep_min is required (Go duration string, e.g. \"5m\")")
	}
	sleepMax, presentMax, err := parseDurationArg(args, "sleep_max")
	if err != nil {
		return sleepEnvelope{}, err
	}
	if !presentMax {
		return sleepEnvelope{}, fmt.Errorf("sleep_max is required (Go duration string, e.g. \"30m\")")
	}
	if sleepMin < time.Minute {
		return sleepEnvelope{}, fmt.Errorf("sleep_min %s is below the 1 minute floor", sleepMin)
	}
	if sleepMax < sleepMin {
		return sleepEnvelope{}, fmt.Errorf("sleep_max %s must be >= sleep_min %s", sleepMax, sleepMin)
	}

	sleepDefault, presentDefault, err := parseDurationArg(args, "sleep_default")
	if err != nil {
		return sleepEnvelope{}, err
	}
	if !presentDefault {
		sleepDefault = (sleepMin + sleepMax) / 2
	} else if sleepDefault < sleepMin || sleepDefault > sleepMax {
		return sleepEnvelope{}, fmt.Errorf("sleep_default %s must lie in [%s, %s]", sleepDefault, sleepMin, sleepMax)
	}

	jitter := 0.1
	if v, present := args["jitter"]; present && v != nil {
		switch j := v.(type) {
		case float64:
			jitter = j
		case int:
			jitter = float64(j)
		default:
			return sleepEnvelope{}, fmt.Errorf("jitter must be a number in [0, 1], got %T", v)
		}
		if jitter < 0 || jitter > 1 {
			return sleepEnvelope{}, fmt.Errorf("jitter %v must be in [0, 1]", jitter)
		}
	}

	return sleepEnvelope{
		sleepMin:     sleepMin,
		sleepMax:     sleepMax,
		sleepDefault: sleepDefault,
		jitter:       jitter,
	}, nil
}

// parseDurationArg returns the parsed duration from args[key]. present
// is false when the key is absent, JSON null, or an empty/whitespace
// string — all "caller didn't set this" shapes. Returns an error when
// the key is present with a non-string type or a string that does not
// parse as a Go duration. Distinguishing "wrong type present" from
// "absent" matters because the JSON schema isn't enforced at handler
// entry; a caller sending `{"sleep_default": 300}` would otherwise
// have the value silently ignored and the loop launched with an
// unexpected default.
func parseDurationArg(args map[string]any, key string) (d time.Duration, present bool, err error) {
	v, found := args[key]
	if !found || v == nil {
		return 0, false, nil
	}
	s, ok := v.(string)
	if !ok {
		return 0, true, fmt.Errorf("%s must be a Go duration string (e.g. \"5m\"), got %T", key, v)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, nil
	}
	d, err = time.ParseDuration(s)
	if err != nil {
		return 0, true, fmt.Errorf("%s %q: %w", key, s, err)
	}
	return d, true, nil
}
