package app

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/opstate"
	"github.com/nugget/thane-ai-agent/internal/router"
)

const modelRegistryExperienceNamespace = "model_registry_experience"

type modelExperienceStore struct {
	store *opstate.Store
}

func newModelExperienceStore(store *opstate.Store) *modelExperienceStore {
	if store == nil {
		return nil
	}
	return &modelExperienceStore{store: store}
}

func (s *modelExperienceStore) SaveFrom(rtr *router.Router) error {
	if s == nil || s.store == nil || rtr == nil {
		return nil
	}
	snapshot := rtr.ExperienceSnapshot()
	if len(snapshot) == 0 {
		return nil
	}

	keys := make([]string, 0, len(snapshot))
	for key := range snapshot {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		payload, err := json.Marshal(snapshot[key])
		if err != nil {
			return err
		}
		if err := s.store.Set(modelRegistryExperienceNamespace, key, string(payload)); err != nil {
			return err
		}
	}
	return nil
}

func (s *modelExperienceStore) LoadInto(rtr *router.Router, logger *slog.Logger) error {
	if s == nil || s.store == nil || rtr == nil {
		return nil
	}

	entries, err := s.store.List(modelRegistryExperienceNamespace)
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

	experience := make(map[string]router.DeploymentStats, len(entries))
	for _, key := range keys {
		var persisted router.DeploymentStats
		if err := json.Unmarshal([]byte(entries[key]), &persisted); err != nil {
			if logger != nil {
				logger.Warn("skipping invalid persisted model experience", "deployment", key, "error", err)
			}
			continue
		}
		persisted.Provider = strings.TrimSpace(persisted.Provider)
		persisted.Resource = strings.TrimSpace(persisted.Resource)
		persisted.UpstreamModel = strings.TrimSpace(persisted.UpstreamModel)
		experience[key] = persisted
	}
	if len(experience) == 0 {
		return nil
	}
	rtr.ReplaceExperience(experience)
	return nil
}
