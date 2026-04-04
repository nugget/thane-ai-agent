package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/models"
)

func (r *Registry) handleModelResourceSetPolicy(_ context.Context, args map[string]any) (string, error) {
	if r.modelRegistry == nil {
		return "", fmt.Errorf("model registry not configured")
	}
	resourceID := strings.TrimSpace(mrStringArg(args, "resource"))
	if resourceID == "" {
		return "", fmt.Errorf("resource is required")
	}
	clearOverride := mrBoolArg(args, "clear_override")
	stateRaw := strings.TrimSpace(mrStringArg(args, "state"))
	if clearOverride && stateRaw != "" {
		return "", fmt.Errorf("clear_override cannot be combined with state")
	}
	if !clearOverride && stateRaw == "" {
		return "", fmt.Errorf("state is required unless clear_override=true")
	}

	if clearOverride {
		if r.deletePersistedModelRegistryResourcePolicy != nil {
			if err := r.deletePersistedModelRegistryResourcePolicy(resourceID); err != nil {
				return "", fmt.Errorf("delete persisted resource policy: %w", err)
			}
		}
		if err := r.modelRegistry.ClearResourcePolicy(resourceID, time.Now().UTC()); err != nil {
			return "", err
		}
		if r.modelRegistrySyncRouter != nil {
			r.modelRegistrySyncRouter()
		}
		snapshot, stats, err := r.currentModelRegistryState()
		if err != nil {
			return "", err
		}
		for _, res := range snapshot.Resources {
			if res.ID == resourceID {
				return mrMarshalToolJSON(map[string]any{
					"status":     "ok",
					"generation": snapshot.Generation,
					"resource":   buildResourceView(snapshot, stats, res),
				})
			}
		}
		return "", fmt.Errorf("resource policy cleared but resource snapshot is unavailable")
	}

	state, err := models.ParseDeploymentPolicyState(stateRaw)
	if err != nil {
		return "", err
	}
	policy := models.ResourcePolicy{
		State:     state,
		Reason:    strings.TrimSpace(mrStringArg(args, "reason")),
		UpdatedAt: time.Now().UTC(),
	}
	if r.persistModelRegistryResourcePolicy != nil {
		if err := r.persistModelRegistryResourcePolicy(resourceID, policy); err != nil {
			return "", fmt.Errorf("persist resource policy: %w", err)
		}
	}
	if err := r.modelRegistry.ApplyResourcePolicy(resourceID, policy, policy.UpdatedAt); err != nil {
		return "", err
	}
	if r.modelRegistrySyncRouter != nil {
		r.modelRegistrySyncRouter()
	}
	snapshot, stats, err := r.currentModelRegistryState()
	if err != nil {
		return "", err
	}
	for _, res := range snapshot.Resources {
		if res.ID == resourceID {
			return mrMarshalToolJSON(map[string]any{
				"status":     "ok",
				"generation": snapshot.Generation,
				"resource":   buildResourceView(snapshot, stats, res),
			})
		}
	}
	return "", fmt.Errorf("resource policy applied but resource snapshot is unavailable")
}

func (r *Registry) handleModelDeploymentSetPolicy(_ context.Context, args map[string]any) (string, error) {
	if r.modelRegistry == nil {
		return "", fmt.Errorf("model registry not configured")
	}
	deploymentID := strings.TrimSpace(mrStringArg(args, "deployment"))
	if deploymentID == "" {
		return "", fmt.Errorf("deployment is required")
	}
	clearOverride := mrBoolArg(args, "clear_override")
	stateRaw := strings.TrimSpace(mrStringArg(args, "state"))
	_, hasRoutable := mrBoolArgOK(args, "routable")
	if clearOverride && (stateRaw != "" || hasRoutable) {
		return "", fmt.Errorf("clear_override cannot be combined with state or routable")
	}
	if !clearOverride && stateRaw == "" && !hasRoutable {
		return "", fmt.Errorf("state or routable is required unless clear_override=true")
	}

	if clearOverride {
		if r.deletePersistedModelRegistryPolicy != nil {
			if err := r.deletePersistedModelRegistryPolicy(deploymentID); err != nil {
				return "", fmt.Errorf("delete persisted deployment policy: %w", err)
			}
		}
		if err := r.modelRegistry.ClearDeploymentPolicy(deploymentID, time.Now().UTC()); err != nil {
			return "", err
		}
		if r.modelRegistrySyncRouter != nil {
			r.modelRegistrySyncRouter()
		}
		snapshot, stats, err := r.currentModelRegistryState()
		if err != nil {
			return "", err
		}
		for _, dep := range snapshot.Deployments {
			if dep.ID == deploymentID {
				return mrMarshalToolJSON(map[string]any{
					"status":     "ok",
					"generation": snapshot.Generation,
					"deployment": buildDeploymentView(stats, dep),
				})
			}
		}
		return "", fmt.Errorf("deployment policy cleared but deployment snapshot is unavailable")
	}

	var state models.DeploymentPolicyState
	if stateRaw != "" {
		parsed, err := models.ParseDeploymentPolicyState(stateRaw)
		if err != nil {
			return "", err
		}
		state = parsed
	}
	policy := models.DeploymentPolicy{
		State:     state,
		Reason:    strings.TrimSpace(mrStringArg(args, "reason")),
		UpdatedAt: time.Now().UTC(),
	}
	if value, ok := mrBoolArgOK(args, "routable"); ok {
		policy.Routable = &value
	}
	if r.persistModelRegistryPolicy != nil {
		if err := r.persistModelRegistryPolicy(deploymentID, policy); err != nil {
			return "", fmt.Errorf("persist deployment policy: %w", err)
		}
	}
	if err := r.modelRegistry.ApplyDeploymentPolicy(deploymentID, policy, policy.UpdatedAt); err != nil {
		return "", err
	}
	if r.modelRegistrySyncRouter != nil {
		r.modelRegistrySyncRouter()
	}
	snapshot, stats, err := r.currentModelRegistryState()
	if err != nil {
		return "", err
	}
	for _, dep := range snapshot.Deployments {
		if dep.ID == deploymentID {
			return mrMarshalToolJSON(map[string]any{
				"status":     "ok",
				"generation": snapshot.Generation,
				"deployment": buildDeploymentView(stats, dep),
			})
		}
	}
	return "", fmt.Errorf("deployment policy applied but deployment snapshot is unavailable")
}
