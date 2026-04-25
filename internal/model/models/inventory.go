package models

import (
	"context"
	"fmt"
	"sort"
	"time"

	modelproviders "github.com/nugget/thane-ai-agent/internal/model/models/providers"
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
	ResourceID   string
	Provider     string
	Capabilities modelproviders.Capabilities
	Attempted    bool
	Models       []DiscoveredModel
	Error        string
}

// DiscoveredModel is provider-exported model metadata normalized just
// enough for Thane's overlay layer.
type DiscoveredModel struct {
	SupportsChat        bool
	Name                string
	ModelType           string
	Publisher           string
	CompatibilityType   string
	State               string
	Family              string
	Families            []string
	ParameterSize       string
	Quantization        string
	SupportsTools       bool
	TrainedForToolUse   bool
	SupportsStreaming   bool
	SupportsImages      bool
	ContextWindow       int
	MaxContextWindow    int
	LoadedContextWindow int
	LoadedInstanceID    string
}

// DiscoverInventory probes configured resources for live model
// inventory. Discovery is best-effort; individual resource failures are
// captured in the returned overlay instead of aborting startup.
func DiscoverInventory(ctx context.Context, cat *Catalog, bundle *ClientBundle) *Inventory {
	if cat == nil || bundle == nil {
		return &Inventory{}
	}

	inv := &Inventory{
		Resources: make([]ResourceInventory, 0, len(cat.Resources)),
	}

	for _, res := range cat.Resources {
		ri := ResourceInventory{
			ResourceID:   res.ID,
			Provider:     res.Provider,
			Capabilities: providerCapabilities(res.Provider, res.Capabilities),
		}

		switch res.Provider {
		case "ollama":
			ri.Attempted = true
			client := bundle.OllamaClients[res.ID]
			if client == nil {
				ri.Error = "missing ollama client"
				inv.Resources = append(inv.Resources, ri)
				continue
			}
			models, err := client.ListModelInfos(ctx)
			if err != nil {
				ri.Error = err.Error()
				inv.Resources = append(inv.Resources, ri)
				continue
			}
			sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
			for _, m := range models {
				ri.Models = append(ri.Models, DiscoveredModel{
					SupportsChat:      true,
					Name:              m.Name,
					Family:            m.Details.Family,
					Families:          append([]string(nil), m.Details.Families...),
					ParameterSize:     m.Details.ParameterSize,
					Quantization:      m.Details.QuantizationLevel,
					SupportsTools:     ri.Capabilities.SupportsTools,
					SupportsStreaming: ri.Capabilities.SupportsStreaming,
					SupportsImages: modelproviders.SupportsImagesForModel(
						ri.Provider,
						m.Name,
						m.Details.Family,
						m.Details.Families,
						ri.Capabilities,
					),
				})
			}
		case "lmstudio":
			ri.Attempted = true
			client := bundle.LMStudioClients[res.ID]
			if client == nil {
				ri.Error = "missing lmstudio client"
				inv.Resources = append(inv.Resources, ri)
				continue
			}
			models, err := client.ListModelInfos(ctx)
			if err != nil {
				ri.Error = err.Error()
				inv.Resources = append(inv.Resources, ri)
				continue
			}
			sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
			for _, m := range models {
				supportsChat := modelproviders.SupportsChatForModel(ri.Provider, m.Type, ri.Capabilities)
				contextWindow := m.LoadedContextLength
				if contextWindow <= 0 {
					contextWindow = m.MaxContextLength
				}
				ri.Models = append(ri.Models, DiscoveredModel{
					SupportsChat:      supportsChat,
					Name:              m.ID,
					ModelType:         m.Type,
					Publisher:         m.Publisher,
					CompatibilityType: m.CompatibilityType,
					State:             m.State,
					Family:            m.Arch,
					Quantization:      m.Quantization,
					SupportsTools:     supportsChat && ri.Capabilities.SupportsTools,
					TrainedForToolUse: m.TrainedForToolUse,
					SupportsStreaming: supportsChat && ri.Capabilities.SupportsStreaming,
					SupportsImages: (m.Vision || modelproviders.SupportsImagesForModel(
						ri.Provider,
						m.ID,
						m.Arch,
						nil,
						modelproviders.Capabilities{
							SupportsImages: supportsChat && ri.Capabilities.SupportsImages,
						},
					)) && supportsChat,
					ContextWindow:       contextWindow,
					MaxContextWindow:    m.MaxContextLength,
					LoadedContextWindow: m.LoadedContextLength,
					LoadedInstanceID:    m.LoadedInstanceID,
				})
			}
		}

		if !ri.Attempted {
			continue
		}
		inv.Resources = append(inv.Resources, ri)
	}

	return inv
}

func discoveredModelSupportsChat(provider string, caps modelproviders.Capabilities, model DiscoveredModel) bool {
	if model.SupportsChat {
		return true
	}
	if model.ModelType != "" {
		return modelproviders.SupportsChatForModel(provider, model.ModelType, caps)
	}
	return caps.SupportsChat
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
	deploymentIndexByResourceModel := make(map[string]int, len(out.Deployments))
	for i, dep := range out.Deployments {
		existingByResourceModel[deploymentKey(dep.ResourceID, dep.ModelName)] = true
		deploymentIndexByResourceModel[deploymentKey(dep.ResourceID, dep.ModelName)] = i
		existingByID[dep.ID] = true
	}

	for _, ri := range inv.Resources {
		if !ri.Attempted || ri.Error != "" {
			continue
		}
		if _, ok := base.resourceBy[ri.ResourceID]; !ok {
			continue
		}
		caps := providerCapabilities(ri.Provider, ri.Capabilities)
		for _, m := range ri.Models {
			if !discoveredModelSupportsChat(ri.Provider, caps, m) {
				continue
			}
			key := deploymentKey(ri.ResourceID, m.Name)
			if idx, ok := deploymentIndexByResourceModel[key]; ok {
				dep := out.Deployments[idx]
				mergeDiscoveredDeployment(&dep, m, caps)
				out.Deployments[idx] = dep
				continue
			}
			id := ri.ResourceID + "/" + m.Name
			if existingByID[id] {
				continue
			}
			dep := newDiscoveredDeployment(base, ri, m, caps, id)
			out.Deployments = append(out.Deployments, dep)
			existingByResourceModel[key] = true
			deploymentIndexByResourceModel[key] = len(out.Deployments) - 1
			existingByID[id] = true
		}
	}

	if err := out.reindex(base.DefaultModel, base.RecoveryModel); err != nil {
		return nil, err
	}
	return out, nil
}

func mergeDiscoveredDeployment(dep *Deployment, model DiscoveredModel, caps modelproviders.Capabilities) {
	if dep == nil {
		return
	}
	if model.ModelType != "" {
		dep.ModelType = model.ModelType
	}
	if model.Publisher != "" {
		dep.Publisher = model.Publisher
	}
	if model.CompatibilityType != "" {
		dep.CompatibilityType = model.CompatibilityType
	}
	if model.State != "" {
		dep.RunnerState = model.State
	}
	if model.Family != "" {
		dep.Family = model.Family
	}
	if len(model.Families) > 0 {
		dep.Families = append([]string(nil), model.Families...)
	}
	if model.ParameterSize != "" {
		dep.ParameterSize = model.ParameterSize
	}
	if model.Quantization != "" {
		dep.Quantization = model.Quantization
	}
	dep.ObservedSupportsTools = observedBoolCapability(model.SupportsTools, caps.SupportsTools)
	dep.ObservedSupportsStreaming = observedBoolCapability(model.SupportsStreaming, caps.SupportsStreaming)
	if model.ContextWindow > 0 {
		dep.ObservedContextWindow = model.ContextWindow
	}
	if model.MaxContextWindow > 0 {
		dep.MaxContextWindow = model.MaxContextWindow
	}
	if model.LoadedContextWindow > 0 {
		dep.LoadedContextWindow = model.LoadedContextWindow
	}
	if model.LoadedInstanceID != "" {
		dep.LoadedInstanceID = model.LoadedInstanceID
	}
	dep.SupportsImages = dep.SupportsImages || model.SupportsImages
	if model.TrainedForToolUse {
		dep.TrainedForToolUse = true
	}
	applyObservedCapabilities(dep, caps)
}

func newDiscoveredDeployment(base *Catalog, ri ResourceInventory, model DiscoveredModel, caps modelproviders.Capabilities, id string) Deployment {
	dep := Deployment{
		ID:                        id,
		ModelName:                 model.Name,
		ModelType:                 model.ModelType,
		Publisher:                 model.Publisher,
		Provider:                  ri.Provider,
		ResourceID:                ri.ResourceID,
		Server:                    ri.ResourceID,
		CompatibilityType:         model.CompatibilityType,
		RunnerState:               model.State,
		ObservedSupportsTools:     observedBoolCapability(model.SupportsTools, caps.SupportsTools),
		TrainedForToolUse:         model.TrainedForToolUse,
		ObservedSupportsStreaming: observedBoolCapability(model.SupportsStreaming, caps.SupportsStreaming),
		SupportsImages:            model.SupportsImages,
		ObservedContextWindow:     firstPositiveInt(model.ContextWindow, base.ContextWindowForModel(model.Name, 0)),
		MaxContextWindow:          model.MaxContextWindow,
		LoadedContextWindow:       model.LoadedContextWindow,
		LoadedInstanceID:          model.LoadedInstanceID,
		Speed:                     5,
		Quality:                   5,
		CostTier:                  defaultCostTier(ri.Provider),
		Source:                    DeploymentSourceDiscovered,
		Routable:                  false,
		Family:                    model.Family,
		Families:                  append([]string(nil), model.Families...),
		ParameterSize:             model.ParameterSize,
		Quantization:              model.Quantization,
	}
	applyObservedCapabilities(&dep, caps)
	return dep
}

func observedBoolCapability(modelValue, providerValue bool) bool {
	return modelValue || providerValue
}

func firstPositiveInt(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func cloneDeployments(in []Deployment) []Deployment {
	out := make([]Deployment, len(in))
	for i, dep := range in {
		dep.Families = append([]string(nil), dep.Families...)
		out[i] = dep
	}
	return out
}

func applyResourcePolicies(cat *Catalog, policies map[string]ResourcePolicy) (*Catalog, error) {
	if cat == nil {
		return nil, fmt.Errorf("nil model catalog")
	}

	out := *cat
	out.Resources = append([]Resource(nil), cat.Resources...)
	out.Deployments = cloneDeployments(cat.Deployments)
	for i := range out.Resources {
		out.Resources[i].PolicyState = DeploymentPolicyStateActive
		out.Resources[i].PolicySource = DeploymentPolicySourceDefault
		out.Resources[i].PolicyReason = ""
		out.Resources[i].PolicyUpdatedAt = time.Time{}
		if policy, ok := policies[out.Resources[i].ID]; ok {
			out.Resources[i].PolicyState = policy.State
			out.Resources[i].PolicySource = DeploymentPolicySourceOverlay
			out.Resources[i].PolicyReason = policy.Reason
			out.Resources[i].PolicyUpdatedAt = policy.UpdatedAt
		}
	}
	resourceByID := make(map[string]Resource, len(out.Resources))
	for _, res := range out.Resources {
		resourceByID[res.ID] = res
	}
	for i := range out.Deployments {
		out.Deployments[i].ResourcePolicyState = DeploymentPolicyStateActive
		out.Deployments[i].ResourcePolicySource = DeploymentPolicySourceDefault
		out.Deployments[i].ResourcePolicyReason = ""
		out.Deployments[i].ResourcePolicyUpdatedAt = time.Time{}
		if res, ok := resourceByID[out.Deployments[i].ResourceID]; ok {
			out.Deployments[i].ResourcePolicyState = res.PolicyState
			out.Deployments[i].ResourcePolicySource = res.PolicySource
			out.Deployments[i].ResourcePolicyReason = res.PolicyReason
			out.Deployments[i].ResourcePolicyUpdatedAt = res.PolicyUpdatedAt
		}
	}
	if err := out.reindex(cat.DefaultModel, cat.RecoveryModel); err != nil {
		return nil, err
	}
	return &out, nil
}

func applyDeploymentPolicies(cat *Catalog, policies map[string]DeploymentPolicy) (*Catalog, error) {
	if cat == nil {
		return nil, fmt.Errorf("nil model catalog")
	}

	out := *cat
	out.Resources = append([]Resource(nil), cat.Resources...)
	out.Deployments = cloneDeployments(cat.Deployments)
	for i := range out.Deployments {
		out.Deployments[i].PolicyState = DeploymentPolicyStateActive
		out.Deployments[i].PolicySource = DeploymentPolicySourceDefault
		out.Deployments[i].PolicyReason = ""
		out.Deployments[i].PolicyUpdatedAt = time.Time{}
		out.Deployments[i].RoutableSource = DeploymentPolicySourceDefault
		if policy, ok := policies[out.Deployments[i].ID]; ok {
			if policy.State != "" {
				out.Deployments[i].PolicyState = policy.State
			}
			out.Deployments[i].PolicySource = DeploymentPolicySourceOverlay
			out.Deployments[i].PolicyReason = policy.Reason
			out.Deployments[i].PolicyUpdatedAt = policy.UpdatedAt
			if policy.Routable != nil {
				out.Deployments[i].Routable = *policy.Routable
				out.Deployments[i].RoutableSource = DeploymentPolicySourceOverlay
			}
		}
	}
	if err := out.reindex(cat.DefaultModel, cat.RecoveryModel); err != nil {
		return nil, err
	}
	return &out, nil
}

func deploymentKey(resourceID, modelName string) string {
	return resourceID + "\x00" + modelName
}

func defaultCostTier(provider string) int {
	switch provider {
	case "ollama", "lmstudio":
		return 0
	case "anthropic", "openai":
		return 3
	default:
		return 1
	}
}
