package app

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

const loopDefinitionPolicyNamespace = "loop_definition_policy"

type loopDefinitionPolicyStore struct {
	store *opstate.Store
}

type persistedLoopDefinitionPolicy struct {
	State     looppkg.DefinitionPolicyState `json:"state"`
	Reason    string                        `json:"reason,omitempty"`
	UpdatedAt time.Time                     `json:"updated_at,omitempty"`
}

func newLoopDefinitionPolicyStore(store *opstate.Store) *loopDefinitionPolicyStore {
	if store == nil {
		return nil
	}
	return &loopDefinitionPolicyStore{store: store}
}

func (s *loopDefinitionPolicyStore) Save(name string, policy looppkg.DefinitionPolicy) error {
	if s == nil || s.store == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	payload, err := json.Marshal(persistedLoopDefinitionPolicy{
		State:     policy.State,
		Reason:    strings.TrimSpace(policy.Reason),
		UpdatedAt: policy.UpdatedAt.UTC(),
	})
	if err != nil {
		return err
	}
	return s.store.Set(loopDefinitionPolicyNamespace, name, string(payload))
}

func (s *loopDefinitionPolicyStore) Delete(name string) error {
	if s == nil || s.store == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	return s.store.Delete(loopDefinitionPolicyNamespace, name)
}

func (s *loopDefinitionPolicyStore) LoadInto(registry *looppkg.DefinitionRegistry, logger *slog.Logger) error {
	if s == nil || s.store == nil || registry == nil {
		return nil
	}

	entries, err := s.store.List(loopDefinitionPolicyNamespace)
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

	policies := make(map[string]looppkg.DefinitionPolicy, len(entries))
	latest := time.Time{}
	for _, key := range keys {
		var persisted persistedLoopDefinitionPolicy
		if err := json.Unmarshal([]byte(entries[key]), &persisted); err != nil {
			if logger != nil {
				logger.Warn("skipping invalid persisted loop definition policy", "name", key, "error", err)
			}
			continue
		}
		policies[key] = looppkg.DefinitionPolicy{
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
	return registry.ReplacePolicies(policies, latest)
}

func (a *App) persistLoopDefinitionPolicy(name string, policy looppkg.DefinitionPolicy) error {
	if a == nil || a.loopDefinitionPolicyStore == nil {
		return nil
	}
	return a.loopDefinitionPolicyStore.Save(name, policy)
}

func (a *App) deletePersistedLoopDefinitionPolicy(name string) error {
	if a == nil || a.loopDefinitionPolicyStore == nil {
		return nil
	}
	return a.loopDefinitionPolicyStore.Delete(name)
}
