package models

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"
)

// Registry is the long-lived model registry for one Thane process. It
// holds an immutable config-defined base catalog, a mutable discovered
// inventory overlay, and the effective merged catalog derived from the
// two.
type Registry struct {
	mu         sync.RWMutex
	base       *Catalog
	overlay    *Inventory
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
	ID               string `json:"id"`
	Provider         string `json:"provider"`
	URL              string `json:"url,omitempty"`
	LastRefresh      string `json:"last_refresh,omitempty"`
	LastError        string `json:"last_error,omitempty"`
	DiscoveredModels int    `json:"discovered_models,omitempty"`
}

// RegistryDeploymentSnapshot is the API-facing state for one effective
// deployment in the merged catalog.
type RegistryDeploymentSnapshot struct {
	ID            string           `json:"id"`
	Model         string           `json:"model"`
	Provider      string           `json:"provider"`
	Resource      string           `json:"resource"`
	Source        DeploymentSource `json:"source"`
	Routable      bool             `json:"routable"`
	SupportsTools bool             `json:"supports_tools,omitempty"`
	ContextWindow int              `json:"context_window,omitempty"`
	Speed         int              `json:"speed,omitempty"`
	Quality       int              `json:"quality,omitempty"`
	CostTier      int              `json:"cost_tier,omitempty"`
	MinComplexity string           `json:"min_complexity,omitempty"`
	Family        string           `json:"family,omitempty"`
	Families      []string         `json:"families,omitempty"`
	ParameterSize string           `json:"parameter_size,omitempty"`
	Quantization  string           `json:"quantization,omitempty"`
}

// NewRegistry constructs a registry from the immutable config-defined
// base catalog.
func NewRegistry(base *Catalog) (*Registry, error) {
	if base == nil {
		return nil, fmt.Errorf("nil base catalog")
	}
	effective, err := MergeInventory(base, nil)
	if err != nil {
		return nil, err
	}
	return &Registry{
		base:      base,
		overlay:   &Inventory{},
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
	r.mu.RUnlock()

	effective, err := MergeInventory(base, inv)
	if err != nil {
		return err
	}

	resourceState := baseResourceRuntime(base.Resources)
	for _, ri := range inv.Resources {
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
			ID:               res.ID,
			Provider:         res.Provider,
			URL:              res.URL,
			LastError:        runtime.LastError,
			DiscoveredModels: runtime.DiscoveredModels,
		}
		if !runtime.LastRefresh.IsZero() {
			rs.LastRefresh = runtime.LastRefresh.UTC().Format(time.RFC3339)
		}
		snap.Resources = append(snap.Resources, rs)
	}

	for _, dep := range r.effective.Deployments {
		snap.Deployments = append(snap.Deployments, RegistryDeploymentSnapshot{
			ID:            dep.ID,
			Model:         dep.ModelName,
			Provider:      dep.Provider,
			Resource:      dep.ResourceID,
			Source:        dep.Source,
			Routable:      dep.Routable,
			SupportsTools: dep.SupportsTools,
			ContextWindow: dep.ContextWindow,
			Speed:         dep.Speed,
			Quality:       dep.Quality,
			CostTier:      dep.CostTier,
			MinComplexity: dep.MinComplexity,
			Family:        dep.Family,
			Families:      append([]string(nil), dep.Families...),
			ParameterSize: dep.ParameterSize,
			Quantization:  dep.Quantization,
		})
	}

	return snap
}

func baseResourceRuntime(resources []Resource) map[string]ResourceRuntime {
	out := make(map[string]ResourceRuntime, len(resources))
	for _, res := range resources {
		out[res.ID] = ResourceRuntime{
			ID:       res.ID,
			Provider: res.Provider,
			URL:      res.URL,
		}
	}
	return out
}
