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

// loopDefinitionPatchFields maps each model-facing patch key to its path in
// the spec's JSON wire form (the same human-facing shape loop_definition_set
// accepts). A two-element path lands inside a nested object — profile or
// supervisor_profile — which setLoopWirePath auto-creates when the current
// spec omitted it.
//
// The set is deliberately scoped to scalar/text fields that are common,
// low-blast-radius incremental edits — the ones whose full-replace friction
// triggered the narrate-then-stop stall (#1110). Structural and list-valued
// fields are intentionally absent; loopDefinitionPatchRedirects points the
// model at the right tool for those.
var loopDefinitionPatchFields = map[string][]string{
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

// loopDefinitionPatchRedirects names the right tool for fields patch
// deliberately does not own, so an unknown-field error teaches the next move
// instead of just rejecting (model-facing-tools.md #4).
var loopDefinitionPatchRedirects = map[string]string{
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
	"name":          "a loop definition cannot be renamed in place; create the renamed definition with loop_definition_set and remove the old one with loop_definition_delete",
}

// handleLoopDefinitionPatch applies a small, field-scoped edit to one stored
// loop definition without resending the whole spec. It reads the current spec,
// merges the requested fields at the JSON wire level, and re-decodes through
// the spec's own UnmarshalJSON so the result validates exactly as
// loop_definition_set would — every field the patch does not touch is
// preserved verbatim by the round-trip.
func (r *Registry) handleLoopDefinitionPatch(ctx context.Context, args map[string]any) (string, error) {
	if r.loopDefinitionRegistry == nil {
		return "", fmt.Errorf("loop definition registry not configured")
	}
	name := toolargs.TrimmedString(args, "name")
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	patchRaw, ok := args["patch"]
	if !ok {
		return "", fmt.Errorf("patch is required")
	}
	patch, ok := patchRaw.(map[string]any)
	if !ok {
		return "", fmt.Errorf("patch must be an object mapping field names to new values")
	}
	if len(patch) == 0 {
		return "", fmt.Errorf("patch is empty; include at least one field to change")
	}
	if err := validateLoopDefinitionPatchKeys(patch); err != nil {
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
	// the round-trip is an identity for every field the patch leaves alone,
	// and the patched fields reuse the exact duration parsing, profile
	// nesting, and quality_floor int/string compatibility that
	// loop_definition_set relies on.
	baseJSON, err := json.Marshal(def.Spec)
	if err != nil {
		return "", fmt.Errorf("encode current spec: %w", err)
	}
	var base map[string]any
	if err := json.Unmarshal(baseJSON, &base); err != nil {
		return "", fmt.Errorf("decode current spec: %w", err)
	}
	for key, val := range patch {
		setLoopWirePath(base, loopDefinitionPatchFields[key], val)
	}
	mergedJSON, err := json.Marshal(base)
	if err != nil {
		return "", err
	}

	var spec looppkg.Spec
	if err := json.Unmarshal(mergedJSON, &spec); err != nil {
		return "", fmt.Errorf("patched spec is invalid: %w", err)
	}
	// Name keys the definition and is not patchable. The round-trip already
	// preserves it; pin it so a future field addition can never rename in place
	// (which would fork a second definition rather than edit this one).
	spec.Name = def.Spec.Name
	if err := spec.ValidatePersistable(); err != nil {
		return "", err
	}

	updatedAt := time.Now().UTC()
	// Single durable commit (persist + upsert + reconcile), falling back to a
	// bare overlay upsert when no commit hook is wired. Policy is a separate
	// overlay and is left untouched — patch edits the spec only.
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
		return "", fmt.Errorf("loop definition patched but snapshot is unavailable")
	}

	result := map[string]any{
		"status":         "ok",
		"generation":     view.Generation,
		"patched_fields": sortedStringKeys(patch),
		"definition":     viewDef,
	}
	// A running service loop holds its launched-time config and does NOT
	// re-read the spec on wake — the edit applies only after a full relaunch.
	// Say so explicitly so the model doesn't wait for the next wake and assume
	// the change failed. (loop_definition_set shares this next-relaunch
	// semantics; patch surfaces it.)
	if r.liveLoopRegistry != nil && r.liveLoopRegistry.GetByName(name) != nil {
		result["notice"] = fmt.Sprintf("%q is currently running; the edit is persisted but a running loop keeps its launched-time config until a full relaunch (process restart, or stop_loop then loop_definition_launch) — it will NOT change on the next wake.", name)
	}
	return ldMarshalToolJSON(result)
}

// validateLoopDefinitionPatchKeys rejects any key patch does not own, with a
// redirect to the correct tool when the key is a known-but-unpatchable field.
func validateLoopDefinitionPatchKeys(patch map[string]any) error {
	for k := range patch {
		if _, ok := loopDefinitionPatchFields[k]; ok {
			continue
		}
		if hint, ok := loopDefinitionPatchRedirects[k]; ok {
			return fmt.Errorf("%q is not patchable: %s", k, hint)
		}
		return fmt.Errorf("unknown patch field %q; patchable fields: %s", k, strings.Join(sortedStringKeys(loopDefinitionPatchFields), ", "))
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
