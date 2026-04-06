package tools

import (
	"context"
	"fmt"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
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
	if r.persistLoopDefinition != nil {
		if err := r.persistLoopDefinition(spec, updatedAt); err != nil {
			return "", fmt.Errorf("persist loop definition: %w", err)
		}
	}
	if err := r.loopDefinitionRegistry.Upsert(spec, updatedAt); err != nil {
		return "", err
	}
	if r.reconcileLoopDefinition != nil {
		if err := r.reconcileLoopDefinition(ctx, spec.Name); err != nil {
			return "", fmt.Errorf("reconcile loop definition: %w", err)
		}
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
	name := ldStringArg(args, "name")
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	snapshot, err := currentLoopDefinitionSnapshot(r)
	if err != nil {
		return "", err
	}
	if existing, ok := findLoopDefinition(snapshot, name); ok && existing.Source == looppkg.DefinitionSourceConfig {
		return "", (&looppkg.ImmutableDefinitionError{Name: name})
	} else if !ok {
		return "", (&looppkg.UnknownDefinitionError{Name: name})
	}
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
	return ldMarshalToolJSON(map[string]any{
		"status":     "ok",
		"generation": view.Generation,
		"name":       name,
	})
}

func (r *Registry) handleLoopDefinitionSetPolicy(ctx context.Context, args map[string]any) (string, error) {
	snapshot, err := currentLoopDefinitionSnapshot(r)
	if err != nil {
		return "", err
	}
	name := ldStringArg(args, "name")
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	if _, found := findLoopDefinition(snapshot, name); !found {
		return "", (&looppkg.UnknownDefinitionError{Name: name})
	}
	clearOverride, _ := args["clear_override"].(bool)
	stateRaw := ldStringArg(args, "state")
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
			Reason:    ldStringArg(args, "reason"),
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
	name := ldStringArg(args, "name")
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
