package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
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
//   - thane_loop_create (the Core front door for creating a durable
//     service/container/event_driven loop) — registered below. It
//     replaced the earlier thane_curate + thane_create_container verbs.
//
// External wakes to live loops are infrastructural rather than
// tool-shaped: producer subsystems dispatch structured envelopes over
// messages.Bus directly, and request_core_attention covers
// loop → core/owner attention escalation.
//
// thane_loop_create constructs a Spec on the caller's behalf from
// intent-shaped inputs, then persists + launches through the same
// registry + reconcile + launch path used by loop_definition_set and
// loop_definition_launch.
type LoopIntentToolDeps struct {
	DocTools *documents.Tools
	Registry *looppkg.DefinitionRegistry
	// PersistSpec persists a spec without upsert/reconcile. Used by the
	// subscription-mutation path, which propagates to the live loop itself
	// rather than reconciling.
	PersistSpec func(looppkg.Spec, time.Time) error
	// CommitSpec durably commits a definition (persist + overlay upsert +
	// reconcile) in one step. thane_loop_create routes through it instead of
	// sequencing the steps by hand.
	CommitSpec       func(context.Context, looppkg.Spec, time.Time) error
	LaunchDefinition func(context.Context, string, looppkg.Launch) (looppkg.LaunchResult, error)
	// LiveRegistry resolves container parent names to live loop IDs and
	// propagates subscription mutations to running loops. Optional but
	// strongly recommended: when nil, thane_loop_create only supports
	// top-level loops, and runtime subscription mutations do not reflect on
	// the live loop until restart.
	LiveRegistry *looppkg.Registry
}

// ConfigureLoopIntentTools registers the intent-shaped loop creation tools on
// the registry. The definition registry and LaunchDefinition helper are
// required. thane_loop_create is Core and registers unconditionally; its output
// sub-mode surfaces a clear error at call time when DocTools is nil rather than
// silently disappearing.
func (r *Registry) ConfigureLoopIntentTools(deps LoopIntentToolDeps) {
	if r == nil || deps.Registry == nil || deps.LaunchDefinition == nil {
		return
	}
	r.loopIntentDeps = deps
	r.registerThaneLoopCreate()
	r.registerUpdateEntitySubscriptions()
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

// curateEntitiesToSubscriptions converts the parsed thane_loop_create
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

// curateEntity is the parsed shape of one element from the thane_loop_create
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
// (e.g. "entities" for thane_loop_create, "add" for
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
// document-maintaining loop created via thane_loop_create. The model running
// each iteration sees the
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
