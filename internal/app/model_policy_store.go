package app

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/models"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

const modelRegistryPolicyNamespace = "model_registry_policy"

type modelPolicyStore struct {
	store *opstate.Store
}

type persistedDeploymentPolicy struct {
	State     models.DeploymentPolicyState `json:"state"`
	Routable  *bool                        `json:"routable,omitempty"`
	Reason    string                       `json:"reason,omitempty"`
	UpdatedAt time.Time                    `json:"updated_at,omitempty"`
}

func newModelPolicyStore(store *opstate.Store) *modelPolicyStore {
	if store == nil {
		return nil
	}
	return &modelPolicyStore{store: store}
}

func (s *modelPolicyStore) Save(id string, policy models.DeploymentPolicy) error {
	if s == nil || s.store == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	payload, err := json.Marshal(persistedDeploymentPolicy{
		State:     policy.State,
		Routable:  policy.Routable,
		Reason:    strings.TrimSpace(policy.Reason),
		UpdatedAt: policy.UpdatedAt.UTC(),
	})
	if err != nil {
		return err
	}
	return s.store.Set(modelRegistryPolicyNamespace, id, string(payload))
}

func (s *modelPolicyStore) Delete(id string) error {
	if s == nil || s.store == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	return s.store.Delete(modelRegistryPolicyNamespace, id)
}

func (s *modelPolicyStore) LoadInto(registry *models.Registry, logger *slog.Logger) error {
	if s == nil || s.store == nil || registry == nil {
		return nil
	}

	entries, err := s.store.List(modelRegistryPolicyNamespace)
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

	policies := make(map[string]models.DeploymentPolicy, len(entries))
	latest := time.Time{}
	for _, key := range keys {
		var persisted persistedDeploymentPolicy
		if err := json.Unmarshal([]byte(entries[key]), &persisted); err != nil {
			if logger != nil {
				logger.Warn("skipping invalid persisted model policy", "deployment", key, "error", err)
			}
			continue
		}
		policies[key] = models.DeploymentPolicy{
			State:     persisted.State,
			Routable:  persisted.Routable,
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
	return registry.ReplaceDeploymentPolicies(policies, latest)
}

func (a *App) persistModelRegistryPolicy(id string, policy models.DeploymentPolicy) error {
	if a == nil || a.modelPolicyStore == nil {
		return nil
	}
	return a.modelPolicyStore.Save(id, policy)
}

func (a *App) deletePersistedModelRegistryPolicy(id string) error {
	if a == nil || a.modelPolicyStore == nil {
		return nil
	}
	return a.modelPolicyStore.Delete(id)
}
