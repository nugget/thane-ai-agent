package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/tools/toolargs"
)

func (r *Registry) handleLoopDefinitionSet(ctx context.Context, args map[string]any) (string, error) {
	if r.loopDefinitionRegistry == nil {
		return "", fmt.Errorf("loop definition registry not configured")
	}
	spec, err := decodeLoopSpecArg(args, "spec")
	if err != nil {
		return "", err
	}
	snapshot, err := currentLoopDefinitionSnapshot(r)
	if err != nil {
		return "", err
	}
	if existing, ok := looppkg.FindDefinition(snapshot, spec.Name); ok && existing.Source == looppkg.DefinitionSourceConfig {
		return "", (&looppkg.ImmutableDefinitionError{Name: spec.Name})
	}
	updatedAt := time.Now().UTC()
	// Single durable commit (persist + upsert + reconcile). Fall back to a
	// bare overlay upsert when no commit hook is wired so a registry-only
	// configuration still works.
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
	def, ok := looppkg.FindDefinitionView(view, spec.Name)
	if !ok {
		return "", fmt.Errorf("loop definition stored but snapshot is unavailable")
	}
	return ldMarshalToolJSON(map[string]any{
		"status":     "ok",
		"generation": view.Generation,
		"definition": def,
	})
}

func (r *Registry) handleLoopDefinitionDelete(ctx context.Context, args map[string]any) (string, error) {
	if r.loopDefinitionRegistry == nil {
		return "", fmt.Errorf("loop definition registry not configured")
	}
	name := toolargs.TrimmedString(args, "name")
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	snapshot, err := currentLoopDefinitionSnapshot(r)
	if err != nil {
		return "", err
	}
	existing, ok := looppkg.FindDefinition(snapshot, name)
	if ok && existing.Source == looppkg.DefinitionSourceConfig {
		return "", (&looppkg.ImmutableDefinitionError{Name: name})
	} else if !ok {
		return "", (&looppkg.UnknownDefinitionError{Name: name})
	}

	// Containers anchor a chunk of the loop graph; deleting one while
	// children still live would orphan their parent_id and silently drop
	// inherited tags. Refuse here so the model removes children first
	// (or moves them to a different parent), keeping the parent->child
	// invariant honest.
	if existing.Spec.Operation == looppkg.OperationContainer && r.loopIntentDeps.LiveRegistry != nil {
		if container := r.loopIntentDeps.LiveRegistry.GetByName(name); container != nil {
			if children := r.loopIntentDeps.LiveRegistry.Children(container.ID()); len(children) > 0 {
				names := make([]string, 0, len(children))
				for _, child := range children {
					names = append(names, child.Name())
				}
				return "", fmt.Errorf("container %q still has %d child loop(s): %v; remove or re-parent them before deleting the container", name, len(children), names)
			}
		}
	}

	// Subscriptions live on Spec.Subscriptions; deleting the spec
	// removes them transitively. No watchlist-store wipe needed —
	// that path was for the old scope_tag indirection.

	if r.deletePersistedLoopDefinition != nil {
		if err := r.deletePersistedLoopDefinition(name); err != nil {
			return "", fmt.Errorf("delete persisted loop definition: %w", err)
		}
	}
	if err := r.loopDefinitionRegistry.Delete(name, time.Now().UTC()); err != nil {
		return "", err
	}
	if r.reconcileLoopDefinition != nil {
		if err := r.reconcileLoopDefinition(ctx, name); err != nil {
			return "", fmt.Errorf("reconcile loop definition: %w", err)
		}
	}
	view, err := currentLoopDefinitionView(r)
	if err != nil {
		return "", err
	}
	result := map[string]any{
		"status":     "ok",
		"generation": view.Generation,
		"name":       name,
	}
	// Cascade: a runtime MQTT wake subscription can target this loop and
	// would be orphaned by the delete (it isn't part of the loop's spec).
	// Remove those and tell the model what got caught, so it isn't
	// surprised later and so a stale row can't fail a future startup.
	if r.cascadeWakeOnLoopDelete != nil {
		removed, configRefs, cascadeErr := r.cascadeWakeOnLoopDelete(name)
		if len(removed) > 0 {
			result["removed_wake_subscriptions"] = removed
			result["notice"] = fmt.Sprintf("Also removed %d MQTT wake subscription(s) that targeted this loop.", len(removed))
		}
		if len(configRefs) > 0 {
			// Config subs are not auto-removed — flag them so the operator
			// updates config before the next restart treats it as fatal.
			result["config_wake_subscriptions_still_targeting"] = configRefs
			result["warning"] = fmt.Sprintf("%d config-defined MQTT wake subscription(s) still target this now-deleted loop; update config or startup will fail.", len(configRefs))
		}
		if cascadeErr != nil {
			// The loop definition was deleted, but some targeted runtime
			// subscriptions couldn't be cleaned up. Don't fail the tool —
			// surface a warning so the operator can prune them by hand and
			// startup verification can skip them in the meantime.
			result["wake_cleanup_error"] = cascadeErr.Error()
		}
	}
	return ldMarshalToolJSON(result)
}

func (r *Registry) handleLoopDefinitionSetPolicy(ctx context.Context, args map[string]any) (string, error) {
	snapshot, err := currentLoopDefinitionSnapshot(r)
	if err != nil {
		return "", err
	}
	name := toolargs.TrimmedString(args, "name")
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if _, found := looppkg.FindDefinition(snapshot, name); !found {
		return "", (&looppkg.UnknownDefinitionError{Name: name})
	}
	clearOverride, _ := args["clear_override"].(bool)
	stateRaw := toolargs.TrimmedString(args, "state")
	if clearOverride && stateRaw != "" {
		return "", fmt.Errorf("clear_override cannot be combined with state")
	}
	if !clearOverride && stateRaw == "" {
		return "", fmt.Errorf("state is required unless clear_override=true")
	}

	if clearOverride {
		if r.deletePersistedLoopDefinitionPolicy != nil {
			if err := r.deletePersistedLoopDefinitionPolicy(name); err != nil {
				return "", fmt.Errorf("delete persisted definition policy: %w", err)
			}
		}
		if err := r.loopDefinitionRegistry.ClearPolicy(name, time.Now().UTC()); err != nil {
			return "", err
		}
	} else {
		state, err := looppkg.ParseDefinitionPolicyState(stateRaw)
		if err != nil {
			return "", err
		}
		policy := looppkg.DefinitionPolicy{
			State:     state,
			Reason:    toolargs.TrimmedString(args, "reason"),
			UpdatedAt: time.Now().UTC(),
		}
		if r.persistLoopDefinitionPolicy != nil {
			if err := r.persistLoopDefinitionPolicy(name, policy); err != nil {
				return "", fmt.Errorf("persist definition policy: %w", err)
			}
		}
		if err := r.loopDefinitionRegistry.ApplyPolicy(name, policy, policy.UpdatedAt); err != nil {
			return "", err
		}
	}
	if r.reconcileLoopDefinition != nil {
		if err := r.reconcileLoopDefinition(ctx, name); err != nil {
			return "", fmt.Errorf("reconcile loop definition: %w", err)
		}
	}

	view, err := currentLoopDefinitionView(r)
	if err != nil {
		return "", err
	}
	def, found := looppkg.FindDefinitionView(view, name)
	if !found {
		return "", fmt.Errorf("definition policy updated but snapshot is unavailable")
	}
	return ldMarshalToolJSON(map[string]any{
		"status":     "ok",
		"generation": view.Generation,
		"definition": def,
	})
}

func (r *Registry) handleLoopDefinitionLaunch(ctx context.Context, args map[string]any) (string, error) {
	if r.launchLoopDefinition == nil {
		return "", fmt.Errorf("loop definition launch is not configured")
	}
	name := toolargs.TrimmedString(args, "name")
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	launch, err := decodeLoopLaunchArg(args, "launch")
	if err != nil {
		return "", fmt.Errorf("launch: %w", err)
	}
	view, err := currentLoopDefinitionView(r)
	if err != nil {
		return "", err
	}
	def, found := looppkg.FindDefinitionView(view, name)
	if !found {
		return "", (&looppkg.UnknownDefinitionError{Name: name})
	}
	launch = applyLoopLaunchContextDefaults(ctx, def, launch)
	result, err := r.launchLoopDefinition(ctx, name, launch)
	if err != nil {
		return "", err
	}
	view, err = currentLoopDefinitionView(r)
	if err != nil {
		return "", err
	}
	def, found = looppkg.FindDefinitionView(view, name)
	if !found {
		return "", fmt.Errorf("definition launched but snapshot is unavailable")
	}
	return ldMarshalToolJSON(map[string]any{
		"status":     "ok",
		"generation": view.Generation,
		"definition": def,
		"result":     result,
	})
}

// handleLoopReparent moves a stored loop under a different container (or to
// top-level) by name and relaunches it so it re-homes immediately. Parenting
// is resolved at launch, so a plain definition edit does not move a running
// loop — this verb pairs the parent_name change with a stop+reconcile so the
// loop comes back up under the new parent in one step. It is the first-class
// form of the manual edit-then-relaunch recipe.
func (r *Registry) handleLoopReparent(ctx context.Context, args map[string]any) (string, error) {
	if r.loopDefinitionRegistry == nil {
		return "", fmt.Errorf("loop definition registry not configured")
	}
	name := toolargs.TrimmedString(args, "name")
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	// Empty, omitted, or "core" means top-level: directly under the
	// structural root, with no container inheritance.
	target := toolargs.TrimmedString(args, "parent_name")
	topLevel := target == "" || strings.EqualFold(target, "core")

	snapshot, err := currentLoopDefinitionSnapshot(r)
	if err != nil {
		return "", err
	}
	def, ok := looppkg.FindDefinition(snapshot, name)
	if !ok {
		return "", (&looppkg.UnknownDefinitionError{Name: name})
	}
	if def.Source == looppkg.DefinitionSourceConfig {
		return "", (&looppkg.ImmutableDefinitionError{Name: name})
	}

	live := r.loopIntentDeps.LiveRegistry

	newParent := ""
	if !topLevel {
		if strings.EqualFold(target, name) {
			return "", fmt.Errorf("cannot reparent %q under itself", name)
		}
		if live == nil {
			return "", fmt.Errorf("live registry not configured; cannot resolve container %q", target)
		}
		container := live.GetByName(target)
		if container == nil {
			return "", fmt.Errorf("target container %q is not currently registered; create it first or wait for hydration", target)
		}
		if container.Operation() != looppkg.OperationContainer {
			return "", fmt.Errorf("target %q is a %s, not a container; loops can only nest under containers", target, container.Operation())
		}
		newParent = container.Name()
	}

	oldParent := strings.TrimSpace(def.Spec.ParentName)
	if oldParent == newParent {
		dest := newParent
		if dest == "" {
			dest = "core (top-level)"
		}
		return ldMarshalToolJSON(map[string]any{
			"status":      "noop",
			"name":        name,
			"parent_name": newParent,
			"detail":      fmt.Sprintf("%q is already parented to %s", name, dest),
		})
	}

	// A container being moved must not have live children: descendants resolve
	// their home from the container's current launch, so relaunching it would
	// strand them. Reparent or stop the children first.
	if live != nil && def.Spec.Operation == looppkg.OperationContainer {
		if cur := live.GetByName(name); cur != nil {
			if children := live.Children(cur.ID()); len(children) > 0 {
				kids := make([]string, 0, len(children))
				for _, c := range children {
					kids = append(kids, c.Name())
				}
				return "", fmt.Errorf("container %q still has %d child loop(s): %v; reparent or stop them before moving the container", name, len(children), kids)
			}
		}
	}

	// Commit the new structural parent onto the persisted spec. ParentID is
	// cleared so hydration resolves parent_name -> the live parent id afresh.
	spec := def.Spec
	spec.ParentName = newParent
	spec.ParentID = ""
	updatedAt := time.Now().UTC()
	if r.commitLoopDefinitionSpec != nil {
		if err := r.commitLoopDefinitionSpec(ctx, spec, updatedAt); err != nil {
			return "", err
		}
	} else if err := r.loopDefinitionRegistry.Upsert(spec, updatedAt); err != nil {
		return "", err
	}

	// The commit reconciles, but reconcile is a no-op on an already-running
	// loop. Force a relaunch so the loop re-homes: stop it (StopLoop
	// deregisters synchronously once the goroutine exits), then reconcile to
	// respawn through the hydration path that resolves parent_name.
	relaunched := false
	if live != nil {
		if cur := live.GetByName(name); cur != nil {
			if err := live.StopLoop(cur.ID()); err != nil {
				return "", fmt.Errorf("parent_name updated but relaunch could not stop %q: %w", name, err)
			}
			if live.GetByName(name) != nil {
				return "", fmt.Errorf("parent_name updated but %q did not stop cleanly; run loop_reparent again to finish the relaunch", name)
			}
			relaunched = true
		}
	}
	if r.reconcileLoopDefinition != nil {
		if err := r.reconcileLoopDefinition(ctx, name); err != nil {
			return "", fmt.Errorf("reconcile after reparent: %w", err)
		}
	}

	result := map[string]any{
		"status":      "ok",
		"name":        name,
		"old_parent":  oldParent,
		"parent_name": newParent,
		"relaunched":  relaunched,
	}
	if newParent == "" {
		result["detail"] = fmt.Sprintf("%q moved to top-level (under core)", name)
	} else {
		result["detail"] = fmt.Sprintf("%q reparented under %q", name, newParent)
	}
	// Return the moved loop as the canonical LoopView so the model reads back
	// the full row — new parent_name, ancestry, inherited tags — in the same
	// shape loop_status emits. old_parent/relaunched stay envelope-level as
	// transition facts that aren't loop-row fields.
	if live != nil {
		statuses := live.Statuses()
		resolver := looppkg.NewLoopViewResolver(statuses, r.loopPolicyByName(), time.Now())
		for _, s := range statuses {
			if s.Name == name {
				result["loop"] = resolver.FromStatus(s)
				break
			}
		}
	}
	return ldMarshalToolJSON(result)
}
