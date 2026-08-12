package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
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
			"\"event_driven\" = a quiescent handler with no timer that runs only when an external trigger wakes it — give it an entity subscription with wake: true (wakes on that entity's changes), point a feed/forge subscription or an MQTT wake at it, or have another loop notify it; without at least one trigger it never runs; " +
			"\"container\" = a non-executing node that groups loops and shares its tags with descendants; like every operation it requires intent, takes the optional parent_name and tags, and rejects execution/output fields (sleep knobs, output, entities, instructions, etc.). " +
			"output (service/event_driven only) declares a managed markdown document the loop maintains, rewriting it each cycle to reflect current state; declaring facets publishes condensed projections alongside the body instead. It comes with a private working-notes document for the loop's own thinking, and both documents are scaffolded with ownership frontmatter before launch — a faceted output's scaffold carries the exact section skeleton its publish tool fills, so the loop's first iteration sees the shape it is expected to produce. Better than the placeholder skeleton: output.initial authors the first publish at create time from the survey you just did (same arguments as the publish tool; see the parameter). A document that already exists is preserved rather than re-scaffolded or seeded (document_state / working_notes_state in the result report which happened). Document-owning loops carry the read-side doc tools regardless of tags (doc_read — which returns this loop's own outputs whole, even when large — plus doc_outline/doc_section for paging other large documents, and doc_history/doc_diff/doc_at for revision history). Omit output for a loop that acts without maintaining a document. " +
			"parent_name nests the loop under a container by name, inheriting its tags and subscriptions. " +
			"prompt_mode picks the system-prompt shape: set \"task\" for a mechanical maintainer/watcher/poller (fetch a source, check a state, update a document) — the compact worker prompt drops the reflective identity stack, the largest single prompt cost in a background loop; leave it unset for a loop that reflects on the agent or composes messages in its voice. " +
			"entities are Home Assistant subscriptions surfaced into the loop's context each iteration; an entry with wake: true ALSO wakes the loop when that entity changes (debounced/coalesced) — for a service loop an early wake, for an event_driven loop a primary trigger. " +
			"Returns the loop definition name, loop_id, and the canonical loop row; plus output_tool/document_path when a document was declared, facets when it declared any, and working_notes_document — every document-owning loop is given a private notes surface beside its document, so its reasoning has somewhere to go that is not what it publishes. If the loop lands at the root but an existing container declares tags it shares, the result also carries a non-blocking placement_advisory suggesting where it might nest (see loop_containers).",
		ContentResolveExempt: []string{
			"name", "intent", "operation", "parent_name", "output", "entities", "tags",
			"instructions", "sleep_min", "sleep_max", "sleep_default", "jitter",
			"quality_floor", "exclude_tools", "metadata", "replace", "dry_run",
			"prompt_mode",
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
		"sleep_min", "sleep_max", "sleep_default", "jitter", "metadata",
		"prompt_mode"); err != nil {
		return "", err
	}

	deps := r.loopIntentDeps
	parentName := toolargs.TrimmedString(args, "parent_name")
	replace, _ := args["replace"].(bool)
	tags := parseLoopCreateTags(args)

	existing, found, err := ensureDefinitionMutable(deps.Registry.Snapshot(), name)
	if err != nil {
		return "", err
	}
	if found {
		if !replace {
			return "", fmt.Errorf("loop definition %q already exists; pass replace=true to overwrite", name)
		}
		if existing.Spec.Operation != looppkg.OperationContainer {
			return "", fmt.Errorf("loop definition %q already exists as operation %q; refusing to convert it into a container", name, existing.Spec.Operation)
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

	// Containers honour dry_run for the same reason executing loops do, and
	// because a flag that works for two of three operations and is silently
	// ignored by the third is worse than one that does not exist.
	if dryRun, _ := args["dry_run"].(bool); dryRun {
		return ldMarshalToolJSON(map[string]any{
			"status":   "dry_run",
			"spec":     spec,
			"warnings": looppkg.BuildDefinitionWarnings(spec),
		})
	}

	launchResult, reused, err := r.commitAndLaunchLoop(ctx, spec)
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
	if reused != nil {
		result["reused_running_loop"] = true
		result["notice"] = staleRunningLoopNotice(name, reused.Operation())
	}
	if advisory := r.livePlacementAdvisory(name, tags, parentName); advisory != nil {
		result["placement_advisory"] = advisory
	}
	r.attachCreatedLoopView(result, launchResult.LoopID)
	return ldMarshalToolJSON(result)
}

// createLoopExecuting builds and launches a service or event_driven loop. The
// service path requires a sleep envelope; the event_driven path rejects one (it
// has no timer). An output document is optional for both.
// executingLoopPlan is everything createLoopExecuting derives from its
// arguments before anything is written: the spec it would persist, plus
// the scaffolding and reporting details the spec itself does not carry.
//
// Separating derivation from writes is what lets dry_run answer "what
// would you build" without creating a document or a definition, and it
// is what makes the inference this tool is growing testable without a
// registry standing behind it.
type executingLoopPlan struct {
	spec     looppkg.Spec
	warnings []looppkg.DefinitionWarning

	replace     bool
	hasOutput   bool
	documentRef string
	title       string
	outputTool  string
	notesRef    string
	entityCount int
	envelope    sleepEnvelope
	createdAt   time.Time

	// seedDocument marks that output.initial carried a first publish for
	// the maintained document; seedPayload holds it, already validated
	// against the output's own facet contract. seedNotes is the initial
	// working-notes body, empty when not seeded.
	seedDocument bool
	seedPayload  looppkg.FacetPayload
	seedNotes    string
}

// planExecutingLoop derives the plan without writing anything. It reads
// registry state — whether this name is taken, whether the named parent
// exists — because those decide whether a plan is possible at all.
func (r *Registry) planExecutingLoop(args map[string]any, name, intent string, op looppkg.Operation) (*executingLoopPlan, error) {
	deps := r.loopIntentDeps
	if op == looppkg.OperationEventDriven {
		if err := rejectArgsForOperation(args, op, "sleep_min", "sleep_max", "sleep_default", "jitter"); err != nil {
			return nil, err
		}
	}

	replace, _ := args["replace"].(bool)
	parentName := toolargs.TrimmedString(args, "parent_name")
	tags := parseLoopCreateTags(args)
	instructions := toolargs.TrimmedString(args, "instructions")

	// An omitted prompt_mode stays empty on the spec — the runtime
	// default is full, but only a caller's explicit choice becomes a
	// durable pin.
	var promptMode agentctx.PromptMode
	if raw := toolargs.TrimmedString(args, "prompt_mode"); raw != "" {
		mode, err := agentctx.ParsePromptMode(raw)
		if err != nil {
			return nil, err
		}
		promptMode = mode
	}

	qualityFloor, err := parseLoopCreateQualityFloor(args)
	if err != nil {
		return nil, err
	}
	metadata, err := parseLoopCreateMetadata(args)
	if err != nil {
		return nil, err
	}
	entities, err := parseEntityList("entities", args["entities"])
	if err != nil {
		return nil, err
	}
	excludeTools := loopCreateExcludeTools(args)

	var envelope sleepEnvelope
	if op == looppkg.OperationService {
		envelope, err = parseSleepEnvelope(args)
		if err != nil {
			return nil, err
		}
	}

	// Output document is optional. When present, scaffold it and declare the
	// managed OutputSpec + a curate-style task; otherwise the loop's per-iteration
	// task is its intent.
	var (
		outputs      []looppkg.OutputSpec
		outputSpec   looppkg.OutputSpec
		documentRef  string
		title        string
		hasOutput    bool
		facets       []looppkg.FacetSpec
		notesRef     string
		seedDocument bool
		seedPayload  looppkg.FacetPayload
		seedNotes    string
	)
	if raw, ok := args["output"].(map[string]any); ok && raw != nil {
		hasOutput = true
		documentRef, _ = raw["document"].(string)
		if strings.TrimSpace(documentRef) == "" {
			return nil, fmt.Errorf("output.document is required when output is set (e.g. \"kb:dashboards/foo.md\")")
		}
		title, _ = raw["title"].(string)
		if strings.TrimSpace(title) == "" {
			title = name
		}
		if deps.DocTools == nil {
			return nil, fmt.Errorf("output document requested but the document store is not configured")
		}
		facets, err = parseOutputFacets(raw["facets"])
		if err != nil {
			return nil, err
		}
		outputSpec = buildCurateOutputSpec(name, documentRef, intent, facets)
		outputs = []looppkg.OutputSpec{outputSpec}

		// Every document-owning loop gets a notes surface. It is not a
		// choice worth offering: a loop that publishes has reasoning that
		// should not be published, and an opt-in that every caller should
		// take is just a default in the wrong position. The cost of an
		// unused one is a scaffolded stub and a context-block line saying
		// it exists.
		notesSpec := buildWorkingNotesSpec(name, documentRef, intent)
		outputs = append(outputs, notesSpec)
		notesRef = notesSpec.Ref

		seedPayload, seedNotes, seedDocument, err = parseOutputInitial(raw["initial"], outputSpec)
		if err != nil {
			return nil, err
		}
	}

	if _, found, err := ensureDefinitionMutable(deps.Registry.Snapshot(), name); err != nil {
		return nil, err
	} else if found && !replace {
		return nil, fmt.Errorf("loop definition %q already exists; pass replace=true to overwrite", name)
	}
	if err := r.resolveLoopParent(parentName); err != nil {
		return nil, err
	}

	task := intent
	if hasOutput {
		task = buildCurateTask(intent, documentRef, outputSpec.ToolName(), len(facets) > 0)
	}

	now := time.Now().UTC()
	spec := looppkg.Spec{
		Name:          name,
		Enabled:       true,
		Task:          task,
		Intent:        intent,
		Operation:     op,
		PromptMode:    promptMode,
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
		return nil, fmt.Errorf("derived spec invalid: %w", err)
	}
	warnings := looppkg.BuildDefinitionWarnings(spec)

	return &executingLoopPlan{
		spec:         spec,
		warnings:     warnings,
		replace:      replace,
		hasOutput:    hasOutput,
		documentRef:  documentRef,
		title:        title,
		outputTool:   outputSpec.ToolName(),
		notesRef:     notesRef,
		entityCount:  len(entities),
		envelope:     envelope,
		createdAt:    now,
		seedDocument: seedDocument,
		seedPayload:  seedPayload,
		seedNotes:    seedNotes,
	}, nil
}

// parseOutputInitial reads output.initial — a first publish authored at
// create time, in the same argument shape as the loop's generated
// publish tool. The creating model has just surveyed the domain and the
// loop inheriting the document may run on a smaller model, so the
// content is worth capturing while that context exists; the contract
// stays the loop's own (a faceted seed passes the full facet
// validation, budgets and all), so a seed can never teach the first
// wake a shape a correct publish would not produce.
//
// Unknown keys are refused rather than dropped — a seed key that
// silently vanishes is content the author believes was published.
func parseOutputInitial(raw any, output looppkg.OutputSpec) (payload looppkg.FacetPayload, notes string, seedsDocument bool, err error) {
	if raw == nil {
		return looppkg.FacetPayload{}, "", false, nil
	}
	initial, ok := raw.(map[string]any)
	if !ok {
		return looppkg.FacetPayload{}, "", false, fmt.Errorf("output.initial must be an object with the publish tool's arguments, got %T", raw)
	}
	if len(initial) == 0 {
		return looppkg.FacetPayload{}, "", false, nil
	}

	documentKeys := []string{"full"}
	if output.HasFacets() {
		documentKeys = documentKeys[:0]
		for _, field := range output.FacetFields() {
			documentKeys = append(documentKeys, field.Key)
		}
	}
	allowed := map[string]bool{"notes": true}
	for _, key := range documentKeys {
		allowed[key] = true
	}
	for key := range initial {
		if !allowed[key] {
			return looppkg.FacetPayload{}, "", false, fmt.Errorf("output.initial.%s is not part of this output's publish shape; this declaration takes %s, plus notes", key, strings.Join(documentKeys, ", "))
		}
	}

	if rawNotes, present := initial["notes"]; present {
		s, ok := rawNotes.(string)
		if !ok {
			return looppkg.FacetPayload{}, "", false, fmt.Errorf("output.initial.notes must be a string, got %T", rawNotes)
		}
		if strings.TrimSpace(s) == "" {
			return looppkg.FacetPayload{}, "", false, fmt.Errorf("output.initial.notes is empty; pass the loop's starting thinking or omit the key")
		}
		if err := looppkg.ValidateOutputBodySize(s); err != nil {
			return looppkg.FacetPayload{}, "", false, fmt.Errorf("output.initial.notes: %w", err)
		}
		notes = s
	}

	seedsDocument = false
	for _, key := range documentKeys {
		if _, present := initial[key]; present {
			seedsDocument = true
			break
		}
	}
	if !seedsDocument {
		return looppkg.FacetPayload{}, notes, false, nil
	}

	if output.HasFacets() {
		payload, err = output.FacetPayloadFromArgs(initial)
		if err != nil {
			return looppkg.FacetPayload{}, "", false, fmt.Errorf("output.initial: %w", err)
		}
		// The whole declared ladder or nothing: a partial seed would
		// publish projections describing different moments, exactly what
		// the publish tool exists to prevent.
		if err := output.ValidateFacetPayload(payload); err != nil {
			return looppkg.FacetPayload{}, "", false, fmt.Errorf("output.initial: %w", err)
		}
		return payload, notes, true, nil
	}

	full, ok := initial["full"].(string)
	if !ok {
		return looppkg.FacetPayload{}, "", false, fmt.Errorf("output.initial.full must be a string, got %T", initial["full"])
	}
	if strings.TrimSpace(full) == "" {
		return looppkg.FacetPayload{}, "", false, fmt.Errorf("output.initial.full is empty; pass the document's first complete body or omit the key")
	}
	if err := looppkg.ValidateOutputBodySize(full); err != nil {
		return looppkg.FacetPayload{}, "", false, fmt.Errorf("output.initial.full: %w", err)
	}
	payload.Full = full
	return payload, notes, true, nil
}

func (r *Registry) createLoopExecuting(ctx context.Context, args map[string]any, name, intent string, op looppkg.Operation) (string, error) {
	deps := r.loopIntentDeps
	plan, err := r.planExecutingLoop(args, name, intent, op)
	if err != nil {
		return "", err
	}
	spec := plan.spec

	// dry_run stops here, before the document is scaffolded and before
	// anything is committed: the answer to "what would you build" must not
	// build it.
	if dryRun, _ := args["dry_run"].(bool); dryRun {
		result := map[string]any{
			"status":   "dry_run",
			"spec":     spec,
			"warnings": plan.warnings,
		}
		// Derived decisions are reported here for the same reason they are
		// reported on a real create: a preview that hides what the tool
		// chose for you is not a preview of what it would do.
		if plan.hasOutput {
			result["output_tool"] = plan.outputTool
			if plan.notesRef != "" {
				result["working_notes_document"] = plan.notesRef
			}
			if len(spec.Outputs) > 0 && len(spec.Outputs[0].Facets) > 0 {
				result["facets"] = spec.Outputs[0].Facets
			}
		}
		return ldMarshalToolJSON(result)
	}

	hasOutput, documentRef, title := plan.hasOutput, plan.documentRef, plan.title
	replace, warnings, envelope, now := plan.replace, plan.warnings, plan.envelope, plan.createdAt
	entityCount, outputTool := plan.entityCount, plan.outputTool
	parentName, tags := spec.ParentName, spec.Tags

	var documentState, notesState string
	if hasOutput {
		outputSpec, notesSpec := declaredOutputSpecs(spec.Outputs)

		docExists, err := declaredDocumentExists(ctx, deps.DocTools, documentRef)
		if err != nil {
			return "", err
		}
		if docExists && !replace {
			return "", fmt.Errorf("output document %q already exists; pass replace=true to replace the loop definition and adopt the document — its body is preserved, only its ownership frontmatter refreshes", documentRef)
		}
		// A seed against an existing document is a conflict to surface, not
		// resolve: the document's accumulated state wins over create-time
		// content, and silently dropping the seed would leave the caller
		// believing content was published that never landed anywhere.
		if plan.seedDocument && docExists {
			return "", fmt.Errorf("output document %q already exists, so its current state wins over output.initial — drop initial, or let the running loop republish through %s", documentRef, outputTool)
		}
		// The notes ref is derived rather than supplied, so a collision is
		// something the caller never chose and would not expect: appending a
		// loop's private reasoning onto an unrelated document is worse than
		// refusing to start.
		notesExists := false
		if plan.notesRef != "" {
			notesExists, err = declaredDocumentExists(ctx, deps.DocTools, plan.notesRef)
			if err != nil {
				return "", err
			}
			if notesExists && !replace {
				return "", fmt.Errorf("derived working-notes document %q already exists; pass replace=true to adopt it with its body preserved, or use loop_definition_set to place the notes elsewhere", plan.notesRef)
			}
			if plan.seedNotes != "" && notesExists {
				return "", fmt.Errorf("working-notes document %q already exists, so the loop's own thinking wins over output.initial.notes — drop the notes seed", plan.notesRef)
			}
		}

		frontmatter := map[string][]string{
			"loop_definition_name":                {name},
			"loop_intent":                         {intent},
			looppkg.OutputAudienceFrontmatterKey:  {string(outputSpec.EffectiveAudience())},
			looppkg.OutputManagedByFrontmatterKey: {outputSpec.ToolName()},
		}
		if op == looppkg.OperationService {
			frontmatter["sleep_min"] = []string{envelope.sleepMin.String()}
			frontmatter["sleep_max"] = []string{envelope.sleepMax.String()}
		}
		// An existing document keeps its body: a re-created definition is
		// an iteration on the loop, and blowing away the state its
		// predecessor accumulated would hand the next iteration a
		// placeholder where its carried belief used to be. Only the
		// ownership frontmatter is refreshed. A fresh document gets the
		// seed when output.initial carried one — a real first publish,
		// authored while the creating model still holds the survey that
		// justified the loop — and the placeholder skeleton otherwise.
		documentState, notesState = "scaffolded", "scaffolded"
		writeArgs := documents.WriteArgs{
			Ref:         documentRef,
			Title:       title,
			Frontmatter: frontmatter,
		}
		switch {
		case docExists:
			documentState = "preserved_existing"
		case plan.seedDocument:
			frontmatter["created"] = []string{now.Format(time.RFC3339)}
			body := plan.seedPayload.Full
			if outputSpec.HasFacets() {
				body = outputSpec.RenderFacetDocument(plan.seedPayload)
			}
			writeArgs.Body = &body
			documentState = "seeded"
		default:
			frontmatter["created"] = []string{now.Format(time.RFC3339)}
			body := renderOutputScaffoldBody(outputSpec, title, intent)
			writeArgs.Body = &body
		}
		if _, err := deps.DocTools.Write(ctx, writeArgs); err != nil {
			return "", fmt.Errorf("scaffold output document: %w", err)
		}

		if notesSpec != nil {
			notesFrontmatter := map[string][]string{
				"loop_definition_name":                {name},
				looppkg.OutputAudienceFrontmatterKey:  {string(notesSpec.EffectiveAudience())},
				looppkg.OutputManagedByFrontmatterKey: {notesSpec.ToolName()},
			}
			notesArgs := documents.WriteArgs{
				Ref:         notesSpec.Ref,
				Title:       title + " — Working Notes",
				Frontmatter: notesFrontmatter,
			}
			switch {
			case notesExists:
				notesState = "preserved_existing"
			case plan.seedNotes != "":
				notesFrontmatter["created"] = []string{now.Format(time.RFC3339)}
				notesBody := plan.seedNotes
				notesArgs.Body = &notesBody
				notesState = "seeded"
			default:
				notesFrontmatter["created"] = []string{now.Format(time.RFC3339)}
				notesBody := renderWorkingNotesScaffoldBody(name)
				notesArgs.Body = &notesBody
			}
			if _, err := deps.DocTools.Write(ctx, notesArgs); err != nil {
				return "", fmt.Errorf("scaffold working-notes document: %w", err)
			}
		}
	}

	launchResult, reused, err := r.commitAndLaunchLoop(ctx, spec)
	if err != nil {
		return "", err
	}

	result := map[string]any{
		"status":               "ok",
		"loop_definition_name": name,
		"loop_id":              launchResult.LoopID,
		"operation":            string(op),
		"parent_name":          parentName,
		"entity_subscriptions": entityCount,
		"warnings":             warnings,
	}
	if reused != nil {
		result["reused_running_loop"] = true
		result["notice"] = staleRunningLoopNotice(name, reused.Operation())
	}
	if hasOutput {
		result["document_path"] = documentRef
		result["output_tool"] = outputTool
		result["document_state"] = documentState
		if plan.notesRef != "" {
			result["working_notes_document"] = plan.notesRef
			result["working_notes_state"] = notesState
		}
		if len(spec.Outputs) > 0 && len(spec.Outputs[0].Facets) > 0 {
			result["facets"] = spec.Outputs[0].Facets
		}
	}
	if op == looppkg.OperationService {
		result["sleep_envelope"] = map[string]any{
			"sleep_min":     envelope.sleepMin.String(),
			"sleep_max":     envelope.sleepMax.String(),
			"sleep_default": envelope.sleepDefault.String(),
			"jitter":        envelope.jitter,
		}
	}
	if advisory := r.livePlacementAdvisory(name, tags, parentName); advisory != nil {
		result["placement_advisory"] = advisory
	}
	r.attachCreatedLoopView(result, launchResult.LoopID)
	return ldMarshalToolJSON(result)
}

// commitAndLaunchLoop commits a derived spec through the durable chokepoint (or
// the bare Upsert fallback) then launches it with an empty Launch so a retry
// short-circuits to the existing loop instead of tripping the running-loop guard.
// reused reports that short-circuit: non-nil when the returned loop_id belongs
// to a loop that was already running before the commit, which keeps its
// launched-time config — the replacement spec applies only at its next
// relaunch. Callers must surface that (keying any guidance off the reused
// instance's own operation, which replace may have diverged from), or the
// result reads as if the new spec is live.
func (r *Registry) commitAndLaunchLoop(ctx context.Context, spec looppkg.Spec) (result looppkg.LaunchResult, reused *looppkg.Loop, err error) {
	stale, err := r.persistLoopSpec(ctx, spec)
	if err != nil {
		return looppkg.LaunchResult{}, nil, err
	}
	res, err := r.loopIntentDeps.LaunchDefinition(ctx, spec.Name, looppkg.Launch{})
	if err != nil {
		return looppkg.LaunchResult{}, nil, fmt.Errorf("launch loop %q: %w", spec.Name, err)
	}
	if stale != nil && res.LoopID == stale.ID() {
		return res, stale, nil
	}
	return res, nil, nil
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

// declaredDocumentExists probes whether a declared output document is
// already present, distinguishing absence from a failed read. The
// answer decides between scaffolding and preserving, so a read that
// fails for any other reason — unknown root, provenance verification,
// IO — must stop the create: misreading it as "absent" would scaffold
// over whatever is actually there.
func declaredDocumentExists(ctx context.Context, docTools *documents.Tools, ref string) (bool, error) {
	_, err := docTools.Read(ctx, documents.RefArgs{Ref: ref})
	switch {
	case err == nil:
		return true, nil
	case documents.IsNotFound(err):
		return false, nil
	default:
		return false, fmt.Errorf("inspect output document %q: %w", ref, err)
	}
}

// declaredOutputSpecs splits a created loop's outputs into the
// maintained document and its derived working notes, by type rather
// than by position so the scaffold cannot silently bind to the wrong
// declaration if the build order ever changes.
func declaredOutputSpecs(outputs []looppkg.OutputSpec) (doc looppkg.OutputSpec, notes *looppkg.OutputSpec) {
	for i := range outputs {
		switch outputs[i].Type {
		case looppkg.OutputTypeMaintainedDocument:
			doc = outputs[i]
		case looppkg.OutputTypeWorkingNotes:
			notes = &outputs[i]
		}
	}
	return doc, notes
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
				"description": "The kind of loop. service = recurring, self-paced within a sleep envelope; event_driven = a quiescent handler with no timer that runs only when an external trigger wakes it (a feed/forge subscription or MQTT wake pointed at it, or an inter-loop notification), which you wire separately; container = non-executing grouping node that shares tags/subscriptions with descendants.",
			},
			"parent_name": map[string]any{
				"type":        "string",
				"description": "Optional container definition name to nest this loop under. The loop inherits the container's tags and subscriptions. Omit for a top-level loop.",
			},
			"output": map[string]any{
				"type":        "object",
				"description": "Optional managed document this loop maintains (service/event_driven only). Scaffolded with ownership frontmatter before launch.",
				"properties": map[string]any{
					"document": map[string]any{
						"type":        "string",
						"description": "Managed-root document ref, e.g. \"kb:dashboards/pr-watchlist.md\" or \"core:journal/decisions.md\".",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "Optional human title for the document. Defaults to the loop name.",
					},
					"facets": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string", "enum": []string{"status_line", "teaser", "digest"}},
						"description": "Publish condensed projections alongside the full body, so each consumer takes the length it can afford — an ambient row takes status_line, a search snippet takes teaser, a digest row takes digest. Declaring these swaps the loop's generated tool from replace_output_* to publish_output_*, which takes one argument per projection. Declare them whenever anything other than this loop will read the document.",
					},
					"initial": map[string]any{
						"type":        "object",
						"description": "Optional first publish, written at create time in the same argument shape as the loop's generated publish tool: one key per declared facet plus full (all together, same budgets — over-budget values are rejected with the limit named), or just full for an unfaceted document; notes seeds the private working notes with your starting theory. You have just surveyed this domain, and the loop inheriting the document may run on a smaller model — author the first publish from what you observed instead of leaving a placeholder; the loop revises from live state at its first wake. Refused when the document already exists.",
						"properties": map[string]any{
							"status_line": map[string]any{"type": "string"},
							"teaser":      map[string]any{"type": "string"},
							"digest":      map[string]any{"type": "string"},
							"full":        map[string]any{"type": "string"},
							"notes":       map[string]any{"type": "string"},
						},
					},
				},
				"required": []string{"document"},
			},
			"entities": map[string]any{
				"type":        "array",
				"description": "Optional Home Assistant entity subscriptions surfaced into the loop's context each iteration. By default they provide context only; an entry with wake: true also wakes this loop when the entity changes (debounced/coalesced — the simple-change-trigger door; compound conditions stay HA-side via MQTT wakes). Container ancestors' subscriptions also cascade in. For a loop monitoring a room or domain, prefer two layers: one area:<area_id> entry for ambient coverage (expansion honors HA visibility, so it is safe by default and tracks the room as devices move), plus the few sharp entities that carry wake, transitions, or history — hand-enumerating a whole room invites omissions an area target makes impossible.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"entity_id": map[string]any{
							"type":        "string",
							"description": "Home Assistant entity ID (e.g. sensor.upstairs_temperature); a glob (e.g. binary_sensor.*door*), re-expanded live each turn; or an organizational target — area:<area_id>, label:<label_id>, floor:<floor_id> — watching that group's current members, re-resolved live. Organizational expansion honors HA visibility: hidden and diagnostic/config members stay out by default (include_hidden / include_diagnostic widen it).",
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
						"mode": map[string]any{
							"type":        "string",
							"enum":        []string{"render", "ingest", "both"},
							"description": "What the subscription feeds. render (default): live state in context each iteration. ingest: feed the recent-state-changes window only. both: both. ingest/both accept entity ids and globs, not area/label/floor targets.",
						},
						"self_only": map[string]any{
							"type":        "boolean",
							"description": "Meaningful on containers: true keeps this subscription out of descendant loops' inherited sets.",
						},
						"requires_tag": map[string]any{
							"type":        "string",
							"description": "Optional capability tag gating visibility: the entity renders only while this tag is active in the loop's context. Render-only; incompatible with mode ingest/both.",
						},
						"transitions": map[string]any{
							"type":        "integer",
							"description": "Include the entity's last n observed state changes in its rendered block ({from, to, ago}, class-aware). Declaring a log automatically feeds the entity into state-change capture. Capped per subscription; entity ids and globs only.",
						},
						"transitions_window_seconds": map[string]any{
							"type":        "integer",
							"description": "Bound the transition log to changes within this trailing window (seconds); combine with transitions or set alone (still capped).",
						},
						"wake": map[string]any{
							"type":        "boolean",
							"description": "Wake this loop when the entity changes — debounced and coalesced; capture follows automatically. Entity ids and globs only; incompatible with requires_tag.",
						},
						"wake_debounce_seconds": map[string]any{
							"type":        "integer",
							"description": "How long changes coalesce before waking (default a few seconds).",
						},
						"include_hidden": map[string]any{
							"type":        "boolean",
							"description": "Widen an area/label/floor target's expansion to members the owner hid in Home Assistant — a deliberate forensic watch. Registry targets only; a concrete entity or glob you name is always watched.",
						},
						"include_diagnostic": map[string]any{
							"type":        "boolean",
							"description": "Widen an area/label/floor target's expansion to diagnostic/config-category members the default expansion leaves out. Registry targets only.",
						},
						"include": EntityMetadataIncludeParameter(),
					},
					"required": []string{"entity_id"},
				},
			},
			"tags": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Optional capability tags. Each tag binds two things into the loop: its tool surface, AND every KB doc whose frontmatter carries that tag (injected into the loop's context each wake). For containers, every descendant inherits them; for executing loops, they scope the tool surface and pull in tagged knowledge. A knowledge-only tag (no tools, just docs) works too and needs no catalog entry. Omit to use only core tags.",
			},
			"instructions": map[string]any{
				"type":        "string",
				"description": "Optional steering text prepended to every iteration's task (service/event_driven). Persists on the spec's profile and shows in loop_definition_get.",
			},
			"prompt_mode": map[string]any{
				"type":        "string",
				"enum":        []string{"full", "task"},
				"description": "Optional system-prompt shape (service/event_driven). \"task\" is the compact worker prompt: it sheds the reflective identity layer while keeping tagged guidance, the task, and current conditions — right for mechanical maintainer/watcher/poller loops, where identity prose is the largest single prompt cost. Omit (or set \"full\") for loops that reflect on the agent or compose messages in its voice. Changeable later, live, via loop_definition_update.",
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
			"dry_run": map[string]any{
				"type":        "boolean",
				"description": "Return the loop spec this call would create, without creating anything: no document is scaffolded, no definition is committed, nothing launches. Use it to inspect or adjust the derived spec before committing, or to hand the spec to loop_definition_set yourself when you want to change a field this tool does not expose.",
			},
			"replace": map[string]any{
				"type":        "boolean",
				"description": "When true, overwrite an existing definition (and output document) of the same name. A loop that is already running is NOT restarted — it keeps its launched-time config and the replacement applies at its next relaunch (stop_loop then loop_definition_launch); the result flags this with reused_running_loop and a notice. Default false; the tool refuses to clobber existing artifacts.",
			},
		},
		"required": []string{"name", "intent", "operation"},
	}
}
