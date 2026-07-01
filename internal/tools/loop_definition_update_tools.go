package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

// loopDefinitionUpdateFields maps each model-facing editable field to its path
// in the spec's JSON wire form (the same human-facing shape loop_definition_set
// accepts). A two-element path lands inside a nested object — profile or
// supervisor_profile — which setLoopWirePath auto-creates when the current
// spec omitted it.
//
// The set is deliberately scoped to scalar/text fields that are common,
// low-blast-radius incremental edits — the ones whose full-replace friction
// triggered the narrate-then-stop stall (#1110). Structural and list-valued
// fields are intentionally absent; loopDefinitionUpdateRedirects points the
// model at the right tool for those.
var loopDefinitionUpdateFields = map[string][]string{
	"task":                    {"task"},
	"model":                   {"profile", "model"},
	"instructions":            {"profile", "instructions"},
	"quality_floor":           {"profile", "quality_floor"},
	"mission":                 {"profile", "mission"},
	"supervisor":              {"supervisor"},
	"supervisor_prob":         {"supervisor_prob"},
	"supervisor_instructions": {"supervisor_profile", "instructions"},
	"sleep_min":               {"sleep_min"},
	"sleep_max":               {"sleep_max"},
	"sleep_default":           {"sleep_default"},
	"max_iter":                {"max_iter"},
}

// loopDefinitionUpdateRedirects names the right tool for fields this tool
// deliberately does not own, so an unknown-field error teaches the next move
// instead of just rejecting (model-facing-tools.md #4).
var loopDefinitionUpdateRedirects = map[string]string{
	"parent_name":   "use loop_reparent — it relaunches the loop so it actually re-homes; a bare parent_name edit does not move a running loop",
	"enabled":       "use loop_definition_set_policy — it owns runtime lifecycle (active/paused/inactive), applies to a running loop, and is unambiguous; a spec-level enabled flip is ignored whenever a policy overlay exists and can stop a running loop mid-call",
	"operation":     "structural field; use loop_definition_set to rewrite the full spec",
	"completion":    "structural field; use loop_definition_set to rewrite the full spec",
	"outputs":       "list field; use loop_definition_set to rewrite the full spec",
	"subscriptions": "list field; use loop_definition_set to rewrite the full spec",
	"tags":          "list field; use loop_definition_set to rewrite the full spec",
	"exclude_tools": "list field; use loop_definition_set to rewrite the full spec",
	"conditions":    "structured field; use loop_definition_set to rewrite the full spec",
	"metadata":      "map field; use loop_definition_set to rewrite the full spec",
}

// handleLoopDefinitionUpdate changes a subset of fields on one stored loop
// definition without resending the whole spec. The editable fields are passed
// as top-level parameters alongside name (matching the *_update tool family);
// every field omitted is preserved. It reads the current spec, merges the
// requested fields at the JSON wire level, and re-decodes through the spec's
// own UnmarshalJSON so the result validates exactly as loop_definition_set
// would.
func (r *Registry) handleLoopDefinitionUpdate(ctx context.Context, args map[string]any) (string, error) {
	if r.loopDefinitionRegistry == nil {
		return "", fmt.Errorf("loop definition registry not configured")
	}
	name := toolargs.TrimmedString(args, "name")
	if name == "" {
		return "", fmt.Errorf("name is required")
	}

	// Every argument except the loop name is a field change. Validating the
	// change-set keys up front turns a typo or an uneditable field into a
	// teaching error rather than a silent no-op.
	changes := make(map[string]any, len(args))
	for k, v := range args {
		if k == "name" {
			continue
		}
		changes[k] = v
	}
	if len(changes) == 0 {
		return "", fmt.Errorf("no fields to change; pass at least one editable field alongside name")
	}
	if err := validateLoopDefinitionUpdateKeys(changes); err != nil {
		return "", err
	}

	snapshot, err := currentLoopDefinitionSnapshot(r)
	if err != nil {
		return "", err
	}
	def, found := looppkg.FindDefinition(snapshot, name)
	if !found {
		return "", (&looppkg.UnknownDefinitionError{Name: name})
	}
	if def.Source == looppkg.DefinitionSourceConfig {
		return "", (&looppkg.ImmutableDefinitionError{Name: name})
	}

	// Read-modify-write through the spec's own JSON wire form. The overlay
	// already persists specs via these MarshalJSON/UnmarshalJSON methods, so
	// the round-trip is an identity for every field left alone, and the
	// changed fields reuse the exact duration parsing, profile nesting, and
	// quality_floor int/string compatibility loop_definition_set relies on.
	baseJSON, err := json.Marshal(def.Spec)
	if err != nil {
		return "", fmt.Errorf("encode current spec: %w", err)
	}
	var base map[string]any
	if err := json.Unmarshal(baseJSON, &base); err != nil {
		return "", fmt.Errorf("decode current spec: %w", err)
	}
	for key, val := range changes {
		setLoopWirePath(base, loopDefinitionUpdateFields[key], val)
	}
	mergedJSON, err := json.Marshal(base)
	if err != nil {
		return "", fmt.Errorf("encode merged spec: %w", err)
	}

	var spec looppkg.Spec
	if err := json.Unmarshal(mergedJSON, &spec); err != nil {
		return "", fmt.Errorf("updated spec is invalid: %w", err)
	}
	// Name keys the definition and is not editable here. The round-trip already
	// preserves it; pin it so a future field addition can never rename in place
	// (which would fork a second definition rather than edit this one).
	spec.Name = def.Spec.Name
	if err := spec.ValidatePersistable(); err != nil {
		return "", err
	}

	// Captured before the commit: the commit's reconcile can spawn an absent
	// active definition, and a loop that is live only after the commit runs
	// the just-written spec — only an instance that survived the commit is
	// stale.
	prior := r.runningLoopByName(name)
	updatedAt := time.Now().UTC()
	// Single durable commit (persist + upsert + reconcile), falling back to a
	// bare overlay upsert when no commit hook is wired. Policy is a separate
	// overlay and is left untouched — this edits the spec only.
	if r.commitLoopDefinitionSpec != nil {
		if err := r.commitLoopDefinitionSpec(ctx, spec, updatedAt); err != nil {
			return "", err
		}
	} else if err := r.loopDefinitionRegistry.Upsert(spec, updatedAt); err != nil {
		return "", err
	}

	view, err := currentLoopDefinitionView(r)
	if err != nil {
		return "", err
	}
	viewDef, ok := looppkg.FindDefinitionView(view, name)
	if !ok {
		return "", fmt.Errorf("loop definition updated but snapshot is unavailable")
	}

	result := map[string]any{
		"status":         "ok",
		"generation":     view.Generation,
		"updated_fields": sortedStringKeys(changes),
		"definition":     viewDef,
	}
	// The retune conformance path (#1153): the same instance survived the
	// commit, so push the merged spec's hot-swappable scalars into the live
	// loop. Promotion is turn-boundary-safe inside the engine; a rejection
	// (operation drift, stopped/finished instance) falls back to honest
	// relaunch guidance — without recommending this tool back to itself.
	if prior != nil {
		if after := r.runningLoopByName(name); after != nil && after.ID() == prior.ID() {
			if retuneErr := after.QueueRetune(spec); retuneErr != nil {
				result["notice"] = fmt.Sprintf("The edit is persisted, but it could not be applied to the running loop (%v). %q keeps its launched-time config; the edit takes effect at the next relaunch (stop_loop then loop_definition_launch, or process restart).", retuneErr, name)
			} else {
				result["retune"] = "applied"
				notice := retuneAppliedNotice(name)
				// MaxIter counts this instance's lifetime attempts: a cap at
				// or below what is already consumed stops the loop at its
				// next wake. Applied conformance, but say it loudly.
				if spec.MaxIter > 0 {
					if st := after.Status(); st.Attempts >= spec.MaxIter {
						notice += fmt.Sprintf(" Warning: max_iter %d is at or below this instance's %d attempts — the loop will stop at its next wake; relaunch to reset the count.", spec.MaxIter, st.Attempts)
					}
				}
				result["notice"] = notice
			}
		}
	}
	return ldMarshalToolJSON(result)
}

// validateLoopDefinitionUpdateKeys rejects any change-set key the tool does not
// own, with a redirect to the correct tool when the key is a known-but-
// uneditable field.
func validateLoopDefinitionUpdateKeys(changes map[string]any) error {
	for k := range changes {
		if _, ok := loopDefinitionUpdateFields[k]; ok {
			continue
		}
		if hint, ok := loopDefinitionUpdateRedirects[k]; ok {
			return fmt.Errorf("%q is not editable here: %s", k, hint)
		}
		return fmt.Errorf("unknown field %q; editable fields: %s", k, strings.Join(sortedStringKeys(loopDefinitionUpdateFields), ", "))
	}
	return nil
}

// setLoopWirePath assigns val at the (1- or 2-element) path in base, creating
// the intermediate object when the current spec omitted it (e.g. a nil
// supervisor_profile).
func setLoopWirePath(base map[string]any, path []string, val any) {
	m := base
	for _, p := range path[:len(path)-1] {
		child, ok := m[p].(map[string]any)
		if !ok {
			child = map[string]any{}
			m[p] = child
		}
		m = child
	}
	m[path[len(path)-1]] = val
}

func sortedStringKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
