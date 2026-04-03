package models

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	modelproviders "github.com/nugget/thane-ai-agent/internal/models/providers"
)

// Registry is the long-lived model registry for one Thane process. It
// holds an immutable config-defined base catalog, a mutable discovered
// inventory overlay, and the effective merged catalog derived from the
// two.
type Registry struct {
	mu         sync.RWMutex
	base       *Catalog
	overlay    *Inventory
	policies   map[string]DeploymentPolicy
	effective  *Catalog
	generation int64
	updatedAt  time.Time
	resources  map[string]ResourceRuntime
}

// ResourceRuntime captures runtime discovery state for one provider
// resource.
type ResourceRuntime struct {
	ID               string
	Provider         string
	URL              string
	Capabilities     modelproviders.Capabilities
	LastRefresh      time.Time
	LastError        string
	DiscoveredModels int
}

// RegistrySnapshot is the model-registry state exported for
// observability and API inspection.
type RegistrySnapshot struct {
	Generation    int64                        `json:"generation"`
	UpdatedAt     string                       `json:"updated_at,omitempty"`
	DefaultModel  string                       `json:"default_model,omitempty"`
	RecoveryModel string                       `json:"recovery_model,omitempty"`
	LocalFirst    bool                         `json:"local_first"`
	Resources     []RegistryResourceSnapshot   `json:"resources,omitempty"`
	Deployments   []RegistryDeploymentSnapshot `json:"deployments,omitempty"`
}

// RegistryResourceSnapshot is the API-facing runtime state for one
// provider resource.
type RegistryResourceSnapshot struct {
	ID                string `json:"id"`
	Provider          string `json:"provider"`
	URL               string `json:"url,omitempty"`
	SupportsChat      bool   `json:"supports_chat,omitempty"`
	SupportsStreaming bool   `json:"supports_streaming,omitempty"`
	SupportsTools     bool   `json:"supports_tools,omitempty"`
	SupportsImages    bool   `json:"supports_images,omitempty"`
	SupportsInventory bool   `json:"supports_inventory,omitempty"`
	LastRefresh       string `json:"last_refresh,omitempty"`
	LastError         string `json:"last_error,omitempty"`
	DiscoveredModels  int    `json:"discovered_models,omitempty"`
}

// RegistryDeploymentSnapshot is the API-facing state for one effective
// deployment in the merged catalog.
type RegistryDeploymentSnapshot struct {
	ID                    string                 `json:"id"`
	Model                 string                 `json:"model"`
	Provider              string                 `json:"provider"`
	Resource              string                 `json:"resource"`
	Source                DeploymentSource       `json:"source"`
	Routable              bool                   `json:"routable"`
	RoutableSource        DeploymentPolicySource `json:"routable_source"`
	SupportsTools         bool                   `json:"supports_tools,omitempty"`
	ProviderSupportsTools bool                   `json:"provider_supports_tools,omitempty"`
	SupportsStreaming     bool                   `json:"supports_streaming,omitempty"`
	SupportsImages        bool                   `json:"supports_images,omitempty"`
	ContextWindow         int                    `json:"context_window,omitempty"`
	Speed                 int                    `json:"speed,omitempty"`
	Quality               int                    `json:"quality,omitempty"`
	CostTier              int                    `json:"cost_tier,omitempty"`
	MinComplexity         string                 `json:"min_complexity,omitempty"`
	Family                string                 `json:"family,omitempty"`
	Families              []string               `json:"families,omitempty"`
	ParameterSize         string                 `json:"parameter_size,omitempty"`
	Quantization          string                 `json:"quantization,omitempty"`
	PolicyState           DeploymentPolicyState  `json:"policy_state"`
	PolicySource          DeploymentPolicySource `json:"policy_source"`
	PolicyReason          string                 `json:"policy_reason,omitempty"`
	PolicyUpdated         string                 `json:"policy_updated_at,omitempty"`
}

// NewRegistry constructs a registry from the immutable config-defined
// base catalog.
func NewRegistry(base *Catalog) (*Registry, error) {
	if base == nil {
		return nil, fmt.Errorf("nil base catalog")
	}
	effective, err := buildEffectiveCatalog(base, nil, nil)
	if err != nil {
		return nil, err
	}
	return &Registry{
		base:      base,
		overlay:   &Inventory{},
		policies:  make(map[string]DeploymentPolicy),
		effective: effective,
		resources: baseResourceRuntime(base.Resources),
	}, nil
}

// BaseCatalog returns the immutable config-defined base catalog.
func (r *Registry) BaseCatalog() *Catalog {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.base
}

// Catalog returns the current effective merged catalog. The returned
// pointer must be treated as immutable by callers.
func (r *Registry) Catalog() *Catalog {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.effective
}

// Refresh probes live inventory from the configured resources and
// applies the resulting overlay to the registry.
func (r *Registry) Refresh(ctx context.Context, bundle *ClientBundle) error {
	if r == nil {
		return fmt.Errorf("nil registry")
	}
	r.mu.RLock()
	base := r.base
	r.mu.RUnlock()
	inv := DiscoverInventory(ctx, base, bundle)
	return r.ApplyInventory(inv, time.Now())
}

// ApplyInventory replaces the mutable overlay and recomputes the
// effective merged catalog.
func (r *Registry) ApplyInventory(inv *Inventory, refreshedAt time.Time) error {
	if r == nil {
		return fmt.Errorf("nil registry")
	}
	if inv == nil {
		inv = &Inventory{}
	}

	r.mu.RLock()
	base := r.base
	policies := clonePolicies(r.policies)
	r.mu.RUnlock()

	effective, err := buildEffectiveCatalog(base, inv, policies)
	if err != nil {
		return err
	}

	resourceState := baseResourceRuntime(base.Resources)
	for _, ri := range inv.Resources {
		if !ri.Attempted {
			continue
		}
		state := resourceState[ri.ResourceID]
		state.LastRefresh = refreshedAt
		state.LastError = ri.Error
		state.DiscoveredModels = len(ri.Models)
		resourceState[ri.ResourceID] = state
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	changed := !reflect.DeepEqual(inv, r.overlay) || r.effective == nil
	r.overlay = inv
	r.effective = effective
	r.resources = resourceState
	if changed {
		r.generation++
	}
	if !refreshedAt.IsZero() {
		r.updatedAt = refreshedAt.UTC()
	}
	return nil
}

// ApplyDeploymentPolicy upserts a runtime policy override for one
// deployment ID in the current registry.
func (r *Registry) ApplyDeploymentPolicy(id string, policy DeploymentPolicy, updatedAt time.Time) error {
	if r == nil {
		return fmt.Errorf("nil registry")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("deployment is required")
	}
	if policy.State == "" {
		return fmt.Errorf("state must be one of [\"active\" \"inactive\" \"flagged\"]")
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	policy.Reason = strings.TrimSpace(policy.Reason)
	policy.UpdatedAt = updatedAt.UTC()

	r.mu.RLock()
	base := r.base
	inv := cloneInventory(r.overlay)
	current := clonePolicies(r.policies)
	currentEffective := r.effective
	r.mu.RUnlock()

	if currentEffective == nil {
		return fmt.Errorf("model registry is not initialized")
	}
	if _, ok := currentEffective.byID[id]; !ok {
		return &UnknownDeploymentError{Deployment: id}
	}

	current[id] = policy
	effective, err := buildEffectiveCatalog(base, inv, current)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	changed := !reflect.DeepEqual(current, r.policies)
	r.policies = current
	r.effective = effective
	if changed {
		r.generation++
	}
	r.updatedAt = updatedAt.UTC()
	return nil
}

// ClearDeploymentPolicy removes an explicit runtime policy override for
// one deployment ID.
func (r *Registry) ClearDeploymentPolicy(id string, updatedAt time.Time) error {
	if r == nil {
		return fmt.Errorf("nil registry")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("deployment is required")
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}

	r.mu.RLock()
	base := r.base
	inv := cloneInventory(r.overlay)
	current := clonePolicies(r.policies)
	currentEffective := r.effective
	r.mu.RUnlock()

	if currentEffective == nil {
		return fmt.Errorf("model registry is not initialized")
	}
	if _, ok := currentEffective.byID[id]; !ok {
		return &UnknownDeploymentError{Deployment: id}
	}

	delete(current, id)
	effective, err := buildEffectiveCatalog(base, inv, current)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	changed := !reflect.DeepEqual(current, r.policies)
	r.policies = current
	r.effective = effective
	if changed {
		r.generation++
	}
	r.updatedAt = updatedAt.UTC()
	return nil
}

// Snapshot returns a JSON-friendly view of the registry state.
func (r *Registry) Snapshot() *RegistrySnapshot {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	snap := &RegistrySnapshot{
		Generation:    r.generation,
		DefaultModel:  r.effective.DefaultModel,
		RecoveryModel: r.effective.RecoveryModel,
		LocalFirst:    r.effective.LocalFirst,
		Resources:     make([]RegistryResourceSnapshot, 0, len(r.effective.Resources)),
		Deployments:   make([]RegistryDeploymentSnapshot, 0, len(r.effective.Deployments)),
	}
	if !r.updatedAt.IsZero() {
		snap.UpdatedAt = r.updatedAt.Format(time.RFC3339)
	}

	for _, res := range r.effective.Resources {
		runtime := r.resources[res.ID]
		rs := RegistryResourceSnapshot{
			ID:                res.ID,
			Provider:          res.Provider,
			URL:               res.URL,
			SupportsChat:      runtime.Capabilities.SupportsChat,
			SupportsStreaming: runtime.Capabilities.SupportsStreaming,
			SupportsTools:     runtime.Capabilities.SupportsTools,
			SupportsImages:    runtime.Capabilities.SupportsImages,
			SupportsInventory: runtime.Capabilities.SupportsInventory,
			LastError:         runtime.LastError,
			DiscoveredModels:  runtime.DiscoveredModels,
		}
		if !runtime.LastRefresh.IsZero() {
			rs.LastRefresh = runtime.LastRefresh.UTC().Format(time.RFC3339)
		}
		snap.Resources = append(snap.Resources, rs)
	}

	for _, dep := range r.effective.Deployments {
		snap.Deployments = append(snap.Deployments, RegistryDeploymentSnapshot{
			ID:                    dep.ID,
			Model:                 dep.ModelName,
			Provider:              dep.Provider,
			Resource:              dep.ResourceID,
			Source:                dep.Source,
			Routable:              dep.Routable,
			RoutableSource:        dep.RoutableSource,
			SupportsTools:         dep.SupportsTools,
			ProviderSupportsTools: dep.ProviderSupportsTools,
			SupportsStreaming:     dep.SupportsStreaming,
			SupportsImages:        dep.SupportsImages,
			ContextWindow:         dep.ContextWindow,
			Speed:                 dep.Speed,
			Quality:               dep.Quality,
			CostTier:              dep.CostTier,
			MinComplexity:         dep.MinComplexity,
			Family:                dep.Family,
			Families:              append([]string(nil), dep.Families...),
			ParameterSize:         dep.ParameterSize,
			Quantization:          dep.Quantization,
			PolicyState:           dep.PolicyState,
			PolicySource:          dep.PolicySource,
			PolicyReason:          dep.PolicyReason,
			PolicyUpdated:         formatPolicyTime(dep.PolicyUpdatedAt),
		})
	}

	return snap
}

func baseResourceRuntime(resources []Resource) map[string]ResourceRuntime {
	out := make(map[string]ResourceRuntime, len(resources))
	for _, res := range resources {
		out[res.ID] = ResourceRuntime{
			ID:           res.ID,
			Provider:     res.Provider,
			URL:          res.URL,
			Capabilities: providerCapabilities(res.Provider, res.Capabilities),
		}
	}
	return out
}

func buildEffectiveCatalog(base *Catalog, inv *Inventory, policies map[string]DeploymentPolicy) (*Catalog, error) {
	merged, err := MergeInventory(base, inv)
	if err != nil {
		return nil, err
	}
	return applyDeploymentPolicies(merged, policies)
}

func clonePolicies(in map[string]DeploymentPolicy) map[string]DeploymentPolicy {
	if len(in) == 0 {
		return make(map[string]DeploymentPolicy)
	}
	out := make(map[string]DeploymentPolicy, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneInventory(inv *Inventory) *Inventory {
	if inv == nil {
		return &Inventory{}
	}
	out := &Inventory{Resources: make([]ResourceInventory, len(inv.Resources))}
	for i, ri := range inv.Resources {
		cloned := ResourceInventory{
			ResourceID:   ri.ResourceID,
			Provider:     ri.Provider,
			Capabilities: ri.Capabilities,
			Attempted:    ri.Attempted,
			Error:        ri.Error,
			Models:       make([]DiscoveredModel, len(ri.Models)),
		}
		for j, model := range ri.Models {
			model.Families = append([]string(nil), model.Families...)
			cloned.Models[j] = model
		}
		out.Resources[i] = cloned
	}
	return out
}

func formatPolicyTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
