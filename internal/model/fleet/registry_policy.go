package fleet

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

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
	if policy.State == "" && policy.Routable == nil {
		return fmt.Errorf("state or routable override is required")
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
	resourcePolicies := cloneResourcePolicies(r.resourcePolicies)
	currentEffective := r.effective
	r.mu.RUnlock()

	if currentEffective == nil {
		return fmt.Errorf("model registry is not initialized")
	}
	if _, ok := currentEffective.byID[id]; !ok {
		return &UnknownDeploymentError{Deployment: id}
	}

	current[id] = policy
	effective, err := buildEffectiveCatalog(base, inv, current, resourcePolicies)
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
	resourcePolicies := cloneResourcePolicies(r.resourcePolicies)
	currentEffective := r.effective
	r.mu.RUnlock()

	if currentEffective == nil {
		return fmt.Errorf("model registry is not initialized")
	}
	if _, ok := currentEffective.byID[id]; !ok {
		return &UnknownDeploymentError{Deployment: id}
	}

	delete(current, id)
	effective, err := buildEffectiveCatalog(base, inv, current, resourcePolicies)
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

// ReplaceDeploymentPolicies swaps the explicit runtime policy overlay
// with the provided policy map. Policies for currently absent
// deployments are retained so they can reapply automatically when a
// discovered deployment returns in a later inventory refresh.
func (r *Registry) ReplaceDeploymentPolicies(policies map[string]DeploymentPolicy, updatedAt time.Time) error {
	if r == nil {
		return fmt.Errorf("nil registry")
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}

	r.mu.RLock()
	base := r.base
	inv := cloneInventory(r.overlay)
	resourcePolicies := cloneResourcePolicies(r.resourcePolicies)
	r.mu.RUnlock()

	next := clonePolicies(policies)
	effective, err := buildEffectiveCatalog(base, inv, next, resourcePolicies)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	changed := !reflect.DeepEqual(next, r.policies)
	r.policies = next
	r.effective = effective
	if changed {
		r.generation++
	}
	r.updatedAt = updatedAt.UTC()
	return nil
}

// ApplyResourcePolicy upserts a runtime policy override for one
// configured resource ID in the current registry.
func (r *Registry) ApplyResourcePolicy(id string, policy ResourcePolicy, updatedAt time.Time) error {
	if r == nil {
		return fmt.Errorf("nil registry")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("resource is required")
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
	deploymentPolicies := clonePolicies(r.policies)
	current := cloneResourcePolicies(r.resourcePolicies)
	currentEffective := r.effective
	r.mu.RUnlock()

	if currentEffective == nil {
		return fmt.Errorf("model registry is not initialized")
	}
	if _, ok := currentEffective.resourceBy[id]; !ok {
		return &UnknownResourceError{Resource: id}
	}

	current[id] = policy
	effective, err := buildEffectiveCatalog(base, inv, deploymentPolicies, current)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	changed := !reflect.DeepEqual(current, r.resourcePolicies)
	r.resourcePolicies = current
	r.effective = effective
	if changed {
		r.generation++
	}
	r.updatedAt = updatedAt.UTC()
	return nil
}

// ClearResourcePolicy removes an explicit runtime policy override for
// one configured resource ID.
func (r *Registry) ClearResourcePolicy(id string, updatedAt time.Time) error {
	if r == nil {
		return fmt.Errorf("nil registry")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("resource is required")
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}

	r.mu.RLock()
	base := r.base
	inv := cloneInventory(r.overlay)
	deploymentPolicies := clonePolicies(r.policies)
	current := cloneResourcePolicies(r.resourcePolicies)
	currentEffective := r.effective
	r.mu.RUnlock()

	if currentEffective == nil {
		return fmt.Errorf("model registry is not initialized")
	}
	if _, ok := currentEffective.resourceBy[id]; !ok {
		return &UnknownResourceError{Resource: id}
	}

	delete(current, id)
	effective, err := buildEffectiveCatalog(base, inv, deploymentPolicies, current)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	changed := !reflect.DeepEqual(current, r.resourcePolicies)
	r.resourcePolicies = current
	r.effective = effective
	if changed {
		r.generation++
	}
	r.updatedAt = updatedAt.UTC()
	return nil
}

// ReplaceResourcePolicies swaps the explicit runtime resource-policy
// overlay with the provided policy map.
func (r *Registry) ReplaceResourcePolicies(policies map[string]ResourcePolicy, updatedAt time.Time) error {
	if r == nil {
		return fmt.Errorf("nil registry")
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}

	r.mu.RLock()
	base := r.base
	inv := cloneInventory(r.overlay)
	deploymentPolicies := clonePolicies(r.policies)
	r.mu.RUnlock()

	next := cloneResourcePolicies(policies)
	effective, err := buildEffectiveCatalog(base, inv, deploymentPolicies, next)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	changed := !reflect.DeepEqual(next, r.resourcePolicies)
	r.resourcePolicies = next
	r.effective = effective
	if changed {
		r.generation++
	}
	r.updatedAt = updatedAt.UTC()
	return nil
}

func buildEffectiveCatalog(base *Catalog, inv *Inventory, policies map[string]DeploymentPolicy, resourcePolicies map[string]ResourcePolicy) (*Catalog, error) {
	merged, err := MergeInventory(base, inv)
	if err != nil {
		return nil, err
	}
	withResources, err := applyResourcePolicies(merged, resourcePolicies)
	if err != nil {
		return nil, err
	}
	return applyDeploymentPolicies(withResources, policies)
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

func cloneResourcePolicies(in map[string]ResourcePolicy) map[string]ResourcePolicy {
	if len(in) == 0 {
		return make(map[string]ResourcePolicy)
	}
	out := make(map[string]ResourcePolicy, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
