package app

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

const modelRegistryResourcePolicyNamespace = "model_registry_resource_policy"

type modelResourcePolicyStore struct {
	store *opstate.Store
}

type persistedResourcePolicy struct {
	State     fleet.DeploymentPolicyState `json:"state"`
	Reason    string                      `json:"reason,omitempty"`
	UpdatedAt time.Time                   `json:"updated_at,omitempty"`
}

func newModelResourcePolicyStore(store *opstate.Store) *modelResourcePolicyStore {
	if store == nil {
		return nil
	}
	return &modelResourcePolicyStore{store: store}
}

func (s *modelResourcePolicyStore) Save(id string, policy fleet.ResourcePolicy) error {
	if s == nil || s.store == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	payload, err := json.Marshal(persistedResourcePolicy{
		State:     policy.State,
		Reason:    strings.TrimSpace(policy.Reason),
		UpdatedAt: policy.UpdatedAt.UTC(),
	})
	if err != nil {
		return err
	}
	return s.store.Set(modelRegistryResourcePolicyNamespace, id, string(payload))
}

func (s *modelResourcePolicyStore) Delete(id string) error {
	if s == nil || s.store == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	return s.store.Delete(modelRegistryResourcePolicyNamespace, id)
}

func (s *modelResourcePolicyStore) LoadInto(registry *fleet.Registry, logger *slog.Logger) error {
	if s == nil || s.store == nil || registry == nil {
		return nil
	}

	entries, err := s.store.List(modelRegistryResourcePolicyNamespace)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	policies := make(map[string]fleet.ResourcePolicy, len(entries))
	latest := time.Time{}
	for _, key := range keys {
		var persisted persistedResourcePolicy
		if err := json.Unmarshal([]byte(entries[key]), &persisted); err != nil {
			if logger != nil {
				logger.Warn("skipping invalid persisted model resource policy", "resource", key, "error", err)
			}
			continue
		}
		policies[key] = fleet.ResourcePolicy{
			State:     persisted.State,
			Reason:    strings.TrimSpace(persisted.Reason),
			UpdatedAt: persisted.UpdatedAt,
		}
		if persisted.UpdatedAt.After(latest) {
			latest = persisted.UpdatedAt
		}
	}
	if len(policies) == 0 {
		return nil
	}
	if latest.IsZero() {
		latest = time.Now()
	}
	return registry.ReplaceResourcePolicies(policies, latest)
}

func (a *App) persistModelRegistryResourcePolicy(id string, policy fleet.ResourcePolicy) error {
	if a == nil || a.modelResourcePolicyStore == nil {
		return nil
	}
	return a.modelResourcePolicyStore.Save(id, policy)
}

func (a *App) deletePersistedModelRegistryResourcePolicy(id string) error {
	if a == nil || a.modelResourcePolicyStore == nil {
		return nil
	}
	return a.modelResourcePolicyStore.Delete(id)
}
