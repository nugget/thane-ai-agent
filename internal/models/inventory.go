package models

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
)

// Inventory is the mutable provider-exported overlay that sits on top
// of the immutable config-defined model catalog.
type Inventory struct {
	Resources []ResourceInventory
}

// ResourceInventory captures the models advertised by one provider
// resource at a point in time. Errors are recorded per-resource so the
// overlay can be partial without blocking startup.
type ResourceInventory struct {
	ResourceID string
	Provider   string
	Models     []DiscoveredModel
	Error      string
}

// DiscoveredModel is provider-exported model metadata normalized just
// enough for Thane's overlay layer.
type DiscoveredModel struct {
	Name          string
	Family        string
	Families      []string
	ParameterSize string
	Quantization  string
}

// DiscoverInventory probes configured resources for live model
// inventory. Discovery is best-effort; individual resource failures are
// captured in the returned overlay instead of aborting startup.
func DiscoverInventory(ctx context.Context, cat *Catalog, bundle *ClientBundle, logger *slog.Logger) *Inventory {
	if cat == nil || bundle == nil {
		return &Inventory{}
	}
	if logger == nil {
		logger = slog.Default()
	}

	inv := &Inventory{
		Resources: make([]ResourceInventory, 0, len(cat.Resources)),
	}

	for _, res := range cat.Resources {
		ri := ResourceInventory{
			ResourceID: res.ID,
			Provider:   res.Provider,
		}

		switch res.Provider {
		case "ollama":
			client := bundle.OllamaClients[res.ID]
			if client == nil {
				ri.Error = "missing ollama client"
				logger.Warn("model inventory discovery skipped", "resource", res.ID, "provider", res.Provider, "error", ri.Error)
				inv.Resources = append(inv.Resources, ri)
				continue
			}
			models, err := client.ListModelInfos(ctx)
			if err != nil {
				ri.Error = err.Error()
				logger.Warn("model inventory discovery failed", "resource", res.ID, "provider", res.Provider, "error", err)
				inv.Resources = append(inv.Resources, ri)
				continue
			}
			sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
			for _, m := range models {
				ri.Models = append(ri.Models, DiscoveredModel{
					Name:          m.Name,
					Family:        m.Details.Family,
					Families:      append([]string(nil), m.Details.Families...),
					ParameterSize: m.Details.ParameterSize,
					Quantization:  m.Details.QuantizationLevel,
				})
			}
			logger.Info("model inventory discovered", "resource", res.ID, "provider", res.Provider, "models", len(ri.Models))
		default:
			logger.Debug("model inventory discovery not implemented for provider", "resource", res.ID, "provider", res.Provider)
		}

		inv.Resources = append(inv.Resources, ri)
	}

	return inv
}

// MergeInventory overlays discovered provider inventory on top of the
// immutable config-defined catalog. Config deployments keep their IDs
// and routing authority; discovered deployments are explicit-use only
// until a later policy layer promotes them into routing.
func MergeInventory(base *Catalog, inv *Inventory) (*Catalog, error) {
	if base == nil {
		return nil, fmt.Errorf("nil base catalog")
	}
	if inv == nil {
		out := *base
		out.Resources = append([]Resource(nil), base.Resources...)
		out.Deployments = cloneDeployments(base.Deployments)
		if err := out.reindex(base.DefaultModel, base.RecoveryModel); err != nil {
			return nil, err
		}
		return &out, nil
	}

	out := &Catalog{
		DefaultModel:  base.DefaultModel,
		RecoveryModel: base.RecoveryModel,
		LocalFirst:    base.LocalFirst,
		Resources:     append([]Resource(nil), base.Resources...),
		Deployments:   cloneDeployments(base.Deployments),
	}

	existingByResourceModel := make(map[string]bool, len(out.Deployments))
	existingByID := make(map[string]bool, len(out.Deployments))
	for _, dep := range out.Deployments {
		existingByResourceModel[deploymentKey(dep.ResourceID, dep.ModelName)] = true
		existingByID[dep.ID] = true
	}

	for _, ri := range inv.Resources {
		if ri.Error != "" {
			continue
		}
		if _, ok := base.resourceBy[ri.ResourceID]; !ok {
			continue
		}
		for _, m := range ri.Models {
			key := deploymentKey(ri.ResourceID, m.Name)
			if existingByResourceModel[key] {
				continue
			}
			id := ri.ResourceID + "/" + m.Name
			if existingByID[id] {
				continue
			}
			out.Deployments = append(out.Deployments, Deployment{
				ID:            id,
				ModelName:     m.Name,
				Provider:      ri.Provider,
				ResourceID:    ri.ResourceID,
				Server:        ri.ResourceID,
				SupportsTools: false,
				ContextWindow: base.ContextWindowForModel(m.Name, 8192),
				Speed:         5,
				Quality:       5,
				CostTier:      defaultCostTier(ri.Provider),
				Source:        DeploymentSourceDiscovered,
				Routable:      false,
				Family:        m.Family,
				Families:      append([]string(nil), m.Families...),
				ParameterSize: m.ParameterSize,
				Quantization:  m.Quantization,
			})
			existingByResourceModel[key] = true
			existingByID[id] = true
		}
	}

	if err := out.reindex(base.DefaultModel, base.RecoveryModel); err != nil {
		return nil, err
	}
	return out, nil
}

func cloneDeployments(in []Deployment) []Deployment {
	out := make([]Deployment, len(in))
	for i, dep := range in {
		dep.Families = append([]string(nil), dep.Families...)
		out[i] = dep
	}
	return out
}

func deploymentKey(resourceID, modelName string) string {
	return resourceID + "\x00" + modelName
}

func defaultCostTier(provider string) int {
	switch provider {
	case "ollama":
		return 0
	case "anthropic", "openai":
		return 3
	default:
		return 1
	}
}
