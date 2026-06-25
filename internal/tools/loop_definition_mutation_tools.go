package tools

import (
	"context"
	"fmt"
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
	if existing, ok := findLoopDefinition(snapshot, spec.Name); ok && existing.Source == looppkg.DefinitionSourceConfig {
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
	def, ok := findLoopDefinitionView(view, spec.Name)
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
	existing, ok := findLoopDefinition(snapshot, name)
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
	if _, found := findLoopDefinition(snapshot, name); !found {
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
	def, found := findLoopDefinitionView(view, name)
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
	def, found := findLoopDefinitionView(view, name)
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
	def, found = findLoopDefinitionView(view, name)
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
