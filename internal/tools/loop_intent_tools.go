package tools

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/router"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// LoopIntentToolDeps wires the loop-definition registry, document
// store, and launch helper into the intent-shaped loop creation tools.
//
// Intent-shaped tools (service_journal today; task_now and
// task_background in later phases) construct a Spec and Launch on the
// caller's behalf from intent-shaped inputs (cadence, output target,
// natural-language intent), then persist + launch through the same
// registry + reconcile + launch path used by loop_definition_set and
// loop_definition_launch.
type LoopIntentToolDeps struct {
	DocTools         *documents.Tools
	Registry         *looppkg.DefinitionRegistry
	PersistSpec      func(looppkg.Spec, time.Time) error
	Reconcile        func(context.Context, string) error
	LaunchDefinition func(context.Context, string, looppkg.Launch) (looppkg.LaunchResult, error)
}

// ConfigureLoopIntentTools registers the intent-shaped loop creation
// tools on the registry. Requires the document store (for
// output-target scaffolding) and the loop-definition registry (for
// spec persistence and launch).
func (r *Registry) ConfigureLoopIntentTools(deps LoopIntentToolDeps) {
	if r == nil || deps.DocTools == nil || deps.Registry == nil {
		return
	}
	r.loopIntentDeps = deps
	r.registerServiceJournal()
}

func (r *Registry) registerServiceJournal() {
	r.Register(&Tool{
		Name: "service_journal",
		Description: "Create and launch a recurring service loop that maintains or journals into a managed markdown document. " +
			"Output-first: the document target (kb:, core:, scratchpad:, generated:) is scaffolded with frontmatter recording loop ownership before the loop is registered, so the loop's identity and intent are self-describing on disk. " +
			"Two output modes: \"journal\" appends a dated entry each cycle (research notes, decision logs, daily digests); \"maintain\" rewrites the document idempotently each cycle (dashboards, current-state snapshots). " +
			"Cadence accepts \"hourly\", \"daily\", \"every 30 minutes\", \"5m\", or \"1h\". Sleep_min/max/jitter are derived automatically. " +
			"Tags scope the loop's tools; omit to inherit the always-active set. " +
			"Returns the document ref, loop definition name, loop_id, and next wake time.",
		ContentResolveExempt: []string{"name", "intent", "cadence", "tags", "guidance", "output", "replace"},
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
				"cadence": map[string]any{
					"type":        "string",
					"description": "How often the loop wakes. Accepts \"hourly\", \"daily\", \"every 30 minutes\", \"30m\", \"1h\", or any time.Duration string. Below 1 minute is rejected.",
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
					"description": "Optional capability tags scoping the loop's tool surface. Omit to use only always-active tags.",
				},
				"guidance": map[string]any{
					"type":        "string",
					"description": "Optional extra steering injected into each iteration's task prompt (output format hints, what to focus on, what to skip).",
				},
				"replace": map[string]any{
					"type":        "boolean",
					"description": "When true, overwrite an existing definition or document of the same name/ref. Default false; the tool refuses to clobber existing artifacts.",
				},
			},
			"required": []string{"name", "intent", "cadence", "output"},
		},
		Handler: r.handleServiceJournal,
	})
}

func (r *Registry) handleServiceJournal(ctx context.Context, args map[string]any) (string, error) {
	deps := r.loopIntentDeps
	if deps.DocTools == nil || deps.Registry == nil {
		return "", fmt.Errorf("service_journal not configured: ConfigureLoopIntentTools must be called at startup")
	}

	name, _ := args["name"].(string)
	intent, _ := args["intent"].(string)
	cadenceInput, _ := args["cadence"].(string)
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("name is required")
	}
	if strings.TrimSpace(intent) == "" {
		return "", fmt.Errorf("intent is required")
	}
	if strings.TrimSpace(cadenceInput) == "" {
		return "", fmt.Errorf("cadence is required")
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
	guidance, _ := args["guidance"].(string)
	replace, _ := args["replace"].(bool)

	cad, err := parseCadence(cadenceInput)
	if err != nil {
		return "", err
	}

	// Refuse to clobber an existing definition unless replace=true.
	// Read directly from deps.Registry rather than going through
	// currentLoopDefinitionSnapshot, because the latter checks
	// r.loopDefinitionRegistry (set by ConfigureLoopDefinitionTools)
	// rather than the registry handle this tool was configured with.
	if snap := deps.Registry.Snapshot(); snap != nil {
		if existing, ok := findLoopDefinition(snap, name); ok {
			if existing.Source == looppkg.DefinitionSourceConfig {
				return "", (&looppkg.ImmutableDefinitionError{Name: name})
			}
			if !replace {
				return "", fmt.Errorf("loop definition %q already exists; pass replace=true to overwrite", name)
			}
		}
	}

	jitterRatio := 0.1
	spec := looppkg.Spec{
		Name:         name,
		Enabled:      true,
		Task:         buildServiceJournalTask(intent, documentRef, outputMode, guidance),
		Operation:    looppkg.OperationService,
		SleepMin:     cad.sleepMin,
		SleepMax:     cad.sleepMax,
		SleepDefault: cad.sleepDefault,
		Jitter:       &jitterRatio,
		Tags:         tags,
		Profile: router.LoopProfile{
			DelegationGating: "disabled",
		},
	}
	if err := spec.ValidatePersistable(); err != nil {
		return "", fmt.Errorf("derived spec invalid: %w", err)
	}
	warnings := looppkg.BuildDefinitionWarnings(spec)

	// Scaffold the output document first. Frontmatter records loop
	// ownership so a future inspector can identify the doc as
	// loop-managed without consulting the registry.
	body := renderScaffoldBody(outputMode, title, intent)
	frontmatter := map[string][]string{
		"loop_definition_name": {name},
		"loop_intent":          {intent},
		"output_mode":          {outputMode},
		"cadence":              {cadenceInput},
		"created_at":           {time.Now().UTC().Format(time.RFC3339)},
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

	// Persist + reconcile + launch. Mirrors handleLoopDefinitionSet +
	// handleLoopDefinitionLaunch; collapsed here so the model only sees
	// one round-trip for the intent.
	updatedAt := time.Now().UTC()
	if deps.PersistSpec != nil {
		if err := deps.PersistSpec(spec, updatedAt); err != nil {
			return "", fmt.Errorf("persist loop definition: %w", err)
		}
	}
	if err := deps.Registry.Upsert(spec, updatedAt); err != nil {
		return "", err
	}
	if deps.Reconcile != nil {
		if err := deps.Reconcile(ctx, name); err != nil {
			return "", fmt.Errorf("reconcile loop definition: %w", err)
		}
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
		"cadence": map[string]any{
			"input":         cadenceInput,
			"sleep_default": cad.sleepDefault.String(),
			"sleep_min":     cad.sleepMin.String(),
			"sleep_max":     cad.sleepMax.String(),
		},
		"warnings": warnings,
	})
}

// buildServiceJournalTask renders the per-iteration task prompt for a
// service_journal-created loop. The model running each iteration sees
// the intent, the document target, and the output mode, plus any
// caller guidance. Kept short and shape-clear so the model can act
// without re-reading the loop's own definition.
func buildServiceJournalTask(intent, docRef, outputMode, guidance string) string {
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
	sb.WriteString(" with the current state. Use document mutation tools (")
	if outputMode == "journal" {
		sb.WriteString("doc_journal_update for the dated entry, doc_read to inspect existing entries")
	} else {
		sb.WriteString("doc_write to replace the body, doc_read to inspect the prior snapshot before rewriting")
	}
	sb.WriteString(").")
	if strings.TrimSpace(guidance) != "" {
		sb.WriteString("\n\nGuidance: ")
		sb.WriteString(guidance)
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

// cadence captures the sleep_min/max/default/jitter triple derived from
// a human-facing cadence string. The loops-ng spec already has these
// fields; this helper centralizes the input parsing so the intent
// tools share one parser.
type cadence struct {
	sleepMin     time.Duration
	sleepMax     time.Duration
	sleepDefault time.Duration
}

var cadenceUnitPattern = regexp.MustCompile(`(\d+)\s*(minutes?|mins?|m|hours?|hrs?|h|days?|d)\b`)

// parseCadence accepts canonical cadence strings ("hourly", "daily",
// "5m", "1h") and natural-language equivalents ("every 30 minutes",
// "every 2 hours"). Returns sleep_min, sleep_max, and a sleep_default
// derived from a symmetric ~10% jitter window. Floor 1 minute.
func parseCadence(input string) (cadence, error) {
	s := strings.ToLower(strings.TrimSpace(input))
	if s == "" {
		return cadence{}, fmt.Errorf("cadence is required")
	}

	switch s {
	case "hourly":
		return cadenceFromInterval(time.Hour), nil
	case "daily":
		return cadenceFromInterval(24 * time.Hour), nil
	}

	// Strip leading "every " — "every 30 minutes" reads to "30 minutes".
	s = strings.TrimPrefix(s, "every ")
	s = strings.TrimSpace(s)

	// Normalize natural-language units to time.Duration form. "30 minutes"
	// → "30m", "2 hours" → "2h", "1 day" → "24h".
	if normalized, ok := normalizeCadenceUnits(s); ok {
		s = normalized
	}

	d, err := time.ParseDuration(s)
	if err != nil {
		return cadence{}, fmt.Errorf("unsupported cadence %q: try \"hourly\", \"daily\", \"30m\", or \"1h\"", input)
	}
	if d < time.Minute {
		return cadence{}, fmt.Errorf("cadence %q is below the 1 minute floor", input)
	}
	return cadenceFromInterval(d), nil
}

func cadenceFromInterval(d time.Duration) cadence {
	jit := d / 10
	if jit < 30*time.Second {
		jit = 30 * time.Second
	}
	return cadence{
		sleepMin:     d - jit,
		sleepMax:     d + jit,
		sleepDefault: d,
	}
}

// normalizeCadenceUnits replaces natural-language unit suffixes with
// time.Duration-compatible single-letter units. Returns (normalized,
// true) on a successful match anywhere in the input; otherwise
// (input, false) so the caller can try time.ParseDuration directly.
func normalizeCadenceUnits(input string) (string, bool) {
	matched := false
	out := cadenceUnitPattern.ReplaceAllStringFunc(input, func(match string) string {
		parts := cadenceUnitPattern.FindStringSubmatch(match)
		if len(parts) < 3 {
			return match
		}
		matched = true
		n, _ := strconv.Atoi(parts[1])
		switch parts[2] {
		case "minute", "minutes", "min", "mins", "m":
			return fmt.Sprintf("%dm", n)
		case "hour", "hours", "hr", "hrs", "h":
			return fmt.Sprintf("%dh", n)
		case "day", "days", "d":
			return fmt.Sprintf("%dh", n*24)
		}
		return match
	})
	return strings.ReplaceAll(out, " ", ""), matched
}
