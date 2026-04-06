package loop

import (
	"fmt"
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
		return fmt.Errorf("loop: definition name is required")
	}
	state, err := ParseDefinitionPolicyState(string(policy.State))
	if err != nil {
		return err
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	policy.State = state
	policy.Reason = strings.TrimSpace(policy.Reason)
	policy.UpdatedAt = updatedAt.UTC()

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.base[name]; !exists {
		if _, exists := r.overlay[name]; !exists {
			return &UnknownDefinitionError{Name: name}
		}
	}
	existing, hadExisting := r.policies[name]
	changed := !hadExisting || existing != policy
	r.policies[name] = policy
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
		return fmt.Errorf("loop: definition name is required")
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.base[name]; !exists {
		if _, exists := r.overlay[name]; !exists {
			return &UnknownDefinitionError{Name: name}
		}
	}
	_, changed := r.policies[name]
	delete(r.policies, name)
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
		state, err := ParseDefinitionPolicyState(string(policy.State))
		if err != nil {
			return fmt.Errorf("loop: policy for %q: %w", name, err)
		}
		policy.State = state
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

	changed := len(next) != len(r.policies)
	if !changed {
		for name, policy := range next {
			if r.policies[name] != policy {
				changed = true
				break
			}
		}
	}
	r.policies = next
	if changed {
		r.generation++
	}
	r.updatedAt = updatedAt.UTC()
	return nil
}
