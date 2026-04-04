package loop

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// ApplyPolicy upserts a runtime policy override for one stored loop
// definition.
func (r *DefinitionRegistry) ApplyPolicy(name string, policy DefinitionPolicy, updatedAt time.Time) error {
	if r == nil {
		return fmt.Errorf("loop: definition registry is nil")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("definition name is required")
	}
	if policy.State == "" {
		return fmt.Errorf("state must be one of [\"active\" \"inactive\"]")
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	policy.Reason = strings.TrimSpace(policy.Reason)
	policy.UpdatedAt = updatedAt.UTC()

	r.mu.RLock()
	_, exists := r.base[name]
	if !exists {
		_, exists = r.overlay[name]
	}
	current := cloneDefinitionPolicies(r.policies)
	r.mu.RUnlock()
	if !exists {
		return &UnknownDefinitionError{Name: name}
	}

	current[name] = policy

	r.mu.Lock()
	defer r.mu.Unlock()
	changed := !reflect.DeepEqual(current, r.policies)
	r.policies = current
	if changed {
		r.generation++
	}
	r.updatedAt = updatedAt.UTC()
	return nil
}

// ClearPolicy removes an explicit runtime policy override for one loop
// definition and returns it to its default state.
func (r *DefinitionRegistry) ClearPolicy(name string, updatedAt time.Time) error {
	if r == nil {
		return fmt.Errorf("loop: definition registry is nil")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("definition name is required")
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}

	r.mu.RLock()
	_, exists := r.base[name]
	if !exists {
		_, exists = r.overlay[name]
	}
	current := cloneDefinitionPolicies(r.policies)
	r.mu.RUnlock()
	if !exists {
		return &UnknownDefinitionError{Name: name}
	}

	delete(current, name)

	r.mu.Lock()
	defer r.mu.Unlock()
	changed := !reflect.DeepEqual(current, r.policies)
	r.policies = current
	if changed {
		r.generation++
	}
	r.updatedAt = updatedAt.UTC()
	return nil
}

// ReplacePolicies swaps the explicit runtime policy overlay with the
// provided policy map during startup-time hydration.
func (r *DefinitionRegistry) ReplacePolicies(policies map[string]DefinitionPolicy, updatedAt time.Time) error {
	if r == nil {
		return fmt.Errorf("loop: definition registry is nil")
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}

	next := make(map[string]DefinitionPolicy, len(policies))
	for name, policy := range policies {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if policy.State == "" {
			return fmt.Errorf("loop: policy for %q is missing state", name)
		}
		policy.Reason = strings.TrimSpace(policy.Reason)
		if policy.UpdatedAt.IsZero() {
			policy.UpdatedAt = updatedAt.UTC()
		} else {
			policy.UpdatedAt = policy.UpdatedAt.UTC()
		}
		next[name] = policy
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for name := range next {
		if _, exists := r.base[name]; exists {
			continue
		}
		if _, exists := r.overlay[name]; exists {
			continue
		}
		delete(next, name)
	}

	changed := !reflect.DeepEqual(next, r.policies)
	r.policies = next
	if changed {
		r.generation++
	}
	r.updatedAt = updatedAt.UTC()
	return nil
}
