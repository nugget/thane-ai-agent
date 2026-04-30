package loop

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefinitionSource identifies where a loop definition came from.
type DefinitionSource string

const (
	// DefinitionSourceConfig means the definition came from immutable
	// config-file input.
	DefinitionSourceConfig DefinitionSource = "config"
	// DefinitionSourceOverlay means the definition came from the mutable
	// persistent overlay.
	DefinitionSourceOverlay DefinitionSource = "overlay"
)

// DefinitionRecord is one persistable loop definition plus its update
// timestamp. It is the durable unit stored in the dynamic overlay.
type DefinitionRecord struct {
	Spec      Spec      `yaml:"spec,omitempty" json:"spec,omitempty"`
	UpdatedAt time.Time `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
}

// DefinitionSnapshot is the API-facing state for one effective loop
// definition.
type DefinitionSnapshot struct {
	Name            string                 `yaml:"name,omitempty" json:"name"`
	Source          DefinitionSource       `yaml:"source,omitempty" json:"source"`
	UpdatedAt       time.Time              `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
	PolicyState     DefinitionPolicyState  `yaml:"policy_state,omitempty" json:"policy_state,omitempty"`
	PolicySource    DefinitionPolicySource `yaml:"policy_source,omitempty" json:"policy_source,omitempty"`
	PolicyReason    string                 `yaml:"policy_reason,omitempty" json:"policy_reason,omitempty"`
	PolicyUpdatedAt time.Time              `yaml:"policy_updated_at,omitempty" json:"policy_updated_at,omitempty"`
	Spec            Spec                   `yaml:"spec,omitempty" json:"spec,omitempty"`
}

// DefinitionRegistrySnapshot is a read-only snapshot of the effective
// loop definition registry.
type DefinitionRegistrySnapshot struct {
	Generation         int64                `yaml:"generation,omitempty" json:"generation"`
	UpdatedAt          time.Time            `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
	ConfigDefinitions  int                  `yaml:"config_definitions,omitempty" json:"config_definitions"`
	OverlayDefinitions int                  `yaml:"overlay_definitions,omitempty" json:"overlay_definitions"`
	Definitions        []DefinitionSnapshot `yaml:"definitions,omitempty" json:"definitions,omitempty"`
}

// UnknownDefinitionError reports a missing dynamic loop definition.
type UnknownDefinitionError struct {
	Name string
}

func (e *UnknownDefinitionError) Error() string {
	return fmt.Sprintf("loop: unknown definition %q", e.Name)
}

// ImmutableDefinitionError reports an attempted mutation of an immutable
// config-defined loop definition.
type ImmutableDefinitionError struct {
	Name string
}

func (e *ImmutableDefinitionError) Error() string {
	return fmt.Sprintf("loop: definition %q is immutable from config", e.Name)
}

// DefinitionRegistry holds the immutable config-defined loop definitions
// plus a mutable persistent overlay for dynamically created definitions.
// It does not track active loop runs; that remains [Registry]'s job.
type DefinitionRegistry struct {
	mu         sync.RWMutex
	base       map[string]Spec
	overlay    map[string]DefinitionRecord
	policies   map[string]DefinitionPolicy
	generation int64
	updatedAt  time.Time
}

// NewDefinitionRegistry constructs a loop-definition registry from the
// immutable config-defined base definitions.
func NewDefinitionRegistry(base []Spec) (*DefinitionRegistry, error) {
	baseMap := make(map[string]Spec, len(base))
	for _, spec := range base {
		spec = cloneSpec(spec)
		spec.Name = strings.TrimSpace(spec.Name)
		if err := spec.ValidatePersistable(); err != nil {
			return nil, err
		}
		if _, exists := baseMap[spec.Name]; exists {
			return nil, fmt.Errorf("loop: duplicate definition %q", spec.Name)
		}
		baseMap[spec.Name] = spec
	}
	return &DefinitionRegistry{
		base:       baseMap,
		overlay:    make(map[string]DefinitionRecord),
		policies:   make(map[string]DefinitionPolicy),
		generation: 1,
	}, nil
}

// Get returns the effective definition with the given name.
func (r *DefinitionRegistry) Get(name string) (Spec, bool) {
	if r == nil {
		return Spec{}, false
	}
	name = strings.TrimSpace(name)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if record, ok := r.overlay[name]; ok {
		return cloneSpec(record.Spec), true
	}
	spec, ok := r.base[name]
	if !ok {
		return Spec{}, false
	}
	return cloneSpec(spec), true
}

// Snapshot returns a read-only snapshot of the effective loop
// definitions, sorted by name.
func (r *DefinitionRegistry) Snapshot() *DefinitionRegistrySnapshot {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.base)+len(r.overlay))
	for name := range r.base {
		names = append(names, name)
	}
	for name := range r.overlay {
		if _, exists := r.base[name]; exists {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	snap := &DefinitionRegistrySnapshot{
		Generation:         r.generation,
		UpdatedAt:          r.updatedAt,
		ConfigDefinitions:  len(r.base),
		OverlayDefinitions: len(r.overlay),
		Definitions:        make([]DefinitionSnapshot, 0, len(names)),
	}

	for _, name := range names {
		if record, ok := r.overlay[name]; ok {
			policy := r.policies[name]
			state, source := effectiveDefinitionPolicy(record.Spec, policy)
			snap.Definitions = append(snap.Definitions, DefinitionSnapshot{
				Name:            name,
				Source:          DefinitionSourceOverlay,
				UpdatedAt:       record.UpdatedAt,
				PolicyState:     state,
				PolicySource:    source,
				PolicyReason:    policy.Reason,
				PolicyUpdatedAt: policy.UpdatedAt,
				Spec:            cloneSpec(record.Spec),
			})
			continue
		}
		policy := r.policies[name]
		state, source := effectiveDefinitionPolicy(r.base[name], policy)
		snap.Definitions = append(snap.Definitions, DefinitionSnapshot{
			Name:            name,
			Source:          DefinitionSourceConfig,
			PolicyState:     state,
			PolicySource:    source,
			PolicyReason:    policy.Reason,
			PolicyUpdatedAt: policy.UpdatedAt,
			Spec:            cloneSpec(r.base[name]),
		})
	}

	return snap
}

// Upsert stores or replaces one dynamic loop definition in the overlay.
func (r *DefinitionRegistry) Upsert(spec Spec, updatedAt time.Time) error {
	if r == nil {
		return fmt.Errorf("loop: definition registry is nil")
	}
	spec = cloneSpec(spec)
	spec.Name = strings.TrimSpace(spec.Name)
	if err := spec.ValidatePersistable(); err != nil {
		return err
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.base[spec.Name]; exists {
		return &ImmutableDefinitionError{Name: spec.Name}
	}
	r.overlay[spec.Name] = DefinitionRecord{
		Spec:      spec,
		UpdatedAt: updatedAt.UTC(),
	}
	r.generation++
	r.updatedAt = updatedAt.UTC()
	return nil
}

// Delete removes one dynamic loop definition from the overlay.
func (r *DefinitionRegistry) Delete(name string, updatedAt time.Time) error {
	if r == nil {
		return fmt.Errorf("loop: definition registry is nil")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.base[name]; exists {
		return &ImmutableDefinitionError{Name: name}
	}
	if _, exists := r.overlay[name]; !exists {
		return &UnknownDefinitionError{Name: name}
	}
	delete(r.overlay, name)
	delete(r.policies, name)
	r.generation++
	r.updatedAt = updatedAt.UTC()
	return nil
}

// ReplaceOverlay replaces the entire dynamic overlay. It is intended for
// startup-time hydration from persistent state.
func (r *DefinitionRegistry) ReplaceOverlay(records map[string]DefinitionRecord) error {
	if r == nil {
		return fmt.Errorf("loop: definition registry is nil")
	}
	next := make(map[string]DefinitionRecord, len(records))
	latest := time.Time{}
	for name, record := range records {
		spec := cloneSpec(record.Spec)
		spec.Name = strings.TrimSpace(spec.Name)
		if spec.Name == "" {
			spec.Name = strings.TrimSpace(name)
		}
		if spec.Name != strings.TrimSpace(name) {
			return fmt.Errorf("loop: overlay key %q does not match spec name %q", name, spec.Name)
		}
		if err := spec.ValidatePersistable(); err != nil {
			return err
		}
		if record.UpdatedAt.IsZero() {
			record.UpdatedAt = time.Now().UTC()
		} else {
			record.UpdatedAt = record.UpdatedAt.UTC()
		}
		record.Spec = spec
		next[spec.Name] = record
		if record.UpdatedAt.After(latest) {
			latest = record.UpdatedAt
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for name := range next {
		if _, exists := r.base[name]; exists {
			return &ImmutableDefinitionError{Name: name}
		}
	}

	r.overlay = next
	r.generation++
	r.updatedAt = latest
	return nil
}

func cloneSpec(s Spec) Spec {
	clone := s
	clone.Conditions = cloneConditions(s.Conditions)
	clone.Tags = append([]string(nil), s.Tags...)
	clone.ExcludeTools = append([]string(nil), s.ExcludeTools...)
	clone.Jitter = cloneFloat64Ptr(s.Jitter)
	clone.Hints = cloneStringMap(s.Hints)
	clone.Metadata = cloneStringMap(s.Metadata)
	clone.Profile = s.Profile
	clone.Profile.ExcludeTools = append([]string(nil), s.Profile.ExcludeTools...)
	clone.Profile.ExtraHints = cloneStringMap(s.Profile.ExtraHints)
	return clone
}
