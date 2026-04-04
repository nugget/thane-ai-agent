package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/models"
	routepkg "github.com/nugget/thane-ai-agent/internal/router"
)

func (r *Registry) handleModelRegistrySummary(_ context.Context, _ map[string]any) (string, error) {
	snapshot, stats, err := r.currentModelRegistryState()
	if err != nil {
		return "", err
	}

	providerCounts := make(map[string]int)
	resourcePolicyCounts := make(map[string]int)
	deploymentPolicyCounts := make(map[string]int)
	resourceIDs := make([]string, 0, len(snapshot.Resources))
	degradedResources := make([]string, 0)
	inactiveResources := make([]string, 0)
	flaggedResources := make([]string, 0)
	promotedDeployments := make([]string, 0)
	explicitOnlyDeployments := make([]string, 0)

	for _, res := range snapshot.Resources {
		resourceIDs = append(resourceIDs, res.ID)
		providerCounts[res.Provider]++
		resourcePolicyCounts[string(res.PolicyState)]++
		if strings.TrimSpace(res.LastError) != "" {
			degradedResources = append(degradedResources, res.ID)
		}
		switch res.PolicyState {
		case models.DeploymentPolicyStateInactive:
			inactiveResources = append(inactiveResources, res.ID)
		case models.DeploymentPolicyStateFlagged:
			flaggedResources = append(flaggedResources, res.ID)
		}
	}

	routableDeployments := 0
	discoveredDeployments := 0
	for _, dep := range snapshot.Deployments {
		deploymentPolicyCounts[string(dep.PolicyState)]++
		if dep.Routable {
			routableDeployments++
		} else {
			explicitOnlyDeployments = append(explicitOnlyDeployments, dep.ID)
		}
		if dep.Source == models.DeploymentSourceDiscovered {
			discoveredDeployments++
		}
		if dep.RoutableSource == models.DeploymentPolicySourceOverlay {
			promotedDeployments = append(promotedDeployments, dep.ID)
		}
	}

	sort.Strings(resourceIDs)
	sort.Strings(degradedResources)
	sort.Strings(inactiveResources)
	sort.Strings(flaggedResources)
	sort.Strings(promotedDeployments)
	sort.Strings(explicitOnlyDeployments)

	result := map[string]any{
		"generation":               snapshot.Generation,
		"updated_at":               snapshot.UpdatedAt,
		"default_model":            snapshot.DefaultModel,
		"recovery_model":           snapshot.RecoveryModel,
		"local_first":              snapshot.LocalFirst,
		"resource_count":           len(snapshot.Resources),
		"deployment_count":         len(snapshot.Deployments),
		"discovered_deployments":   discoveredDeployments,
		"routable_deployments":     routableDeployments,
		"provider_counts":          providerCounts,
		"resource_policy_counts":   resourcePolicyCounts,
		"deployment_policy_counts": deploymentPolicyCounts,
		"resource_ids":             resourceIDs,
		"degraded_resources":       degradedResources,
		"inactive_resources":       inactiveResources,
		"flagged_resources":        flaggedResources,
		"promoted_deployments":     promotedDeployments,
		"explicit_only_count":      len(explicitOnlyDeployments),
	}
	if stats != nil {
		result["cooldown_count"] = len(stats.ResourceHealth)
		result["cooldown_resources"] = mrSortedResourceHealthKeys(stats.ResourceHealth)
	}
	return mrMarshalToolJSON(result)
}

func (r *Registry) handleModelRegistryList(_ context.Context, args map[string]any) (string, error) {
	snapshot, stats, err := r.currentModelRegistryState()
	if err != nil {
		return "", err
	}

	kind := strings.ToLower(strings.TrimSpace(mrStringArg(args, "kind")))
	if kind == "" {
		kind = "deployments"
	}
	if kind != "deployments" && kind != "resources" {
		return "", fmt.Errorf("kind must be one of [\"deployments\" \"resources\"]")
	}

	limit := mrIntArg(args, "limit", defaultModelRegistryListLimit)
	if limit <= 0 {
		limit = defaultModelRegistryListLimit
	}
	if limit > maxModelRegistryListLimit {
		limit = maxModelRegistryListLimit
	}
	query := strings.ToLower(strings.TrimSpace(mrStringArg(args, "query")))
	provider := strings.ToLower(strings.TrimSpace(mrStringArg(args, "provider")))
	resource := strings.TrimSpace(mrStringArg(args, "resource"))
	policyState := strings.ToLower(strings.TrimSpace(mrStringArg(args, "policy_state")))
	source := strings.ToLower(strings.TrimSpace(mrStringArg(args, "source")))

	if kind == "resources" {
		items := make([]modelRegistryResourceView, 0, len(snapshot.Resources))
		for _, res := range snapshot.Resources {
			if provider != "" && !strings.EqualFold(res.Provider, provider) {
				continue
			}
			if resource != "" && res.ID != resource {
				continue
			}
			if policyState != "" && string(res.PolicyState) != policyState {
				continue
			}
			if mrHasBoolArg(args, "supports_tools") && res.SupportsTools != mrBoolArg(args, "supports_tools") {
				continue
			}
			if mrHasBoolArg(args, "supports_images") && res.SupportsImages != mrBoolArg(args, "supports_images") {
				continue
			}
			if query != "" && !matchesResourceQuery(res, query) {
				continue
			}
			items = append(items, buildResourceView(snapshot, stats, res))
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		total := len(items)
		if len(items) > limit {
			items = items[:limit]
		}
		return mrMarshalToolJSON(map[string]any{
			"kind":  "resources",
			"count": len(items),
			"total": total,
			"items": items,
		})
	}

	items := make([]modelRegistryDeploymentView, 0, len(snapshot.Deployments))
	for _, dep := range snapshot.Deployments {
		if provider != "" && !strings.EqualFold(dep.Provider, provider) {
			continue
		}
		if resource != "" && dep.Resource != resource {
			continue
		}
		if policyState != "" && string(dep.PolicyState) != policyState {
			continue
		}
		if source != "" && string(dep.Source) != source {
			continue
		}
		if mrHasBoolArg(args, "supports_tools") && dep.SupportsTools != mrBoolArg(args, "supports_tools") {
			continue
		}
		if mrHasBoolArg(args, "supports_images") && dep.SupportsImages != mrBoolArg(args, "supports_images") {
			continue
		}
		if mrHasBoolArg(args, "routable") && dep.Routable != mrBoolArg(args, "routable") {
			continue
		}
		if query != "" && !matchesDeploymentQuery(dep, query) {
			continue
		}
		items = append(items, buildDeploymentView(stats, dep))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	total := len(items)
	if len(items) > limit {
		items = items[:limit]
	}
	return mrMarshalToolJSON(map[string]any{
		"kind":  "deployments",
		"count": len(items),
		"total": total,
		"items": items,
	})
}

func (r *Registry) handleModelRegistryGet(_ context.Context, args map[string]any) (string, error) {
	snapshot, stats, err := r.currentModelRegistryState()
	if err != nil {
		return "", err
	}

	resourceID := strings.TrimSpace(mrStringArg(args, "resource"))
	deploymentID := strings.TrimSpace(mrStringArg(args, "deployment"))
	if resourceID == "" && deploymentID == "" {
		return "", fmt.Errorf("resource or deployment is required")
	}
	if resourceID != "" && deploymentID != "" {
		return "", fmt.Errorf("provide either resource or deployment, not both")
	}

	if resourceID != "" {
		for _, res := range snapshot.Resources {
			if res.ID != resourceID {
				continue
			}
			view := buildResourceView(snapshot, stats, res)
			var deployments []modelRegistryDeploymentView
			for _, dep := range snapshot.Deployments {
				if dep.Resource != resourceID {
					continue
				}
				deployments = append(deployments, buildDeploymentView(stats, dep))
			}
			sort.Slice(deployments, func(i, j int) bool { return deployments[i].ID < deployments[j].ID })
			return mrMarshalToolJSON(map[string]any{
				"kind":        "resource",
				"resource":    view,
				"deployments": deployments,
			})
		}
		return "", fmt.Errorf("resource %q not found", resourceID)
	}

	for _, dep := range snapshot.Deployments {
		if dep.ID != deploymentID {
			continue
		}
		view := buildDeploymentView(stats, dep)
		var resource *modelRegistryResourceView
		for _, res := range snapshot.Resources {
			if res.ID != dep.Resource {
				continue
			}
			rv := buildResourceView(snapshot, stats, res)
			resource = &rv
			break
		}
		return mrMarshalToolJSON(map[string]any{
			"kind":       "deployment",
			"deployment": view,
			"resource":   resource,
		})
	}
	return "", fmt.Errorf("deployment %q not found", deploymentID)
}

func (r *Registry) currentModelRegistryState() (*models.RegistrySnapshot, *routepkg.Stats, error) {
	if r.modelRegistry == nil {
		return nil, nil, fmt.Errorf("model registry not configured")
	}
	snapshot := r.modelRegistry.Snapshot()
	if snapshot == nil {
		return nil, nil, fmt.Errorf("model registry snapshot unavailable")
	}
	if r.modelRouter == nil {
		return snapshot, nil, nil
	}
	stats := r.modelRouter.GetStats()
	return snapshot, &stats, nil
}

func buildResourceView(snapshot *models.RegistrySnapshot, stats *routepkg.Stats, res models.RegistryResourceSnapshot) modelRegistryResourceView {
	view := modelRegistryResourceView{RegistryResourceSnapshot: res}
	for _, dep := range snapshot.Deployments {
		if dep.Resource == res.ID {
			view.DeploymentCount++
		}
	}
	if stats != nil {
		if health, ok := stats.ResourceHealth[res.ID]; ok {
			h := health
			view.Health = &h
		}
	}
	return view
}

func buildDeploymentView(stats *routepkg.Stats, dep models.RegistryDeploymentSnapshot) modelRegistryDeploymentView {
	view := modelRegistryDeploymentView{RegistryDeploymentSnapshot: dep}
	if stats != nil {
		if meta, ok := stats.DeploymentStats[dep.ID]; ok {
			m := meta
			view.Stats = &m
		}
	}
	return view
}
