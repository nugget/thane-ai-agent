package app

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/opstate"
)

const loopDefinitionRegistryNamespace = "loop_definition_registry"

type loopDefinitionStore struct {
	store *opstate.Store
}

func newLoopDefinitionStore(store *opstate.Store) *loopDefinitionStore {
	if store == nil {
		return nil
	}
	return &loopDefinitionStore{store: store}
}

func (s *loopDefinitionStore) Save(spec looppkg.Spec, updatedAt time.Time) error {
	if s == nil || s.store == nil {
		return nil
	}
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" {
		return nil
	}
	if err := spec.ValidatePersistable(); err != nil {
		return err
	}
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(looppkg.DefinitionRecord{
		Spec:      spec,
		UpdatedAt: updatedAt.UTC(),
	})
	if err != nil {
		return err
	}
	return s.store.Set(loopDefinitionRegistryNamespace, spec.Name, string(payload))
}

func (s *loopDefinitionStore) Delete(name string) error {
	if s == nil || s.store == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	return s.store.Delete(loopDefinitionRegistryNamespace, name)
}

func (s *loopDefinitionStore) LoadInto(registry *looppkg.DefinitionRegistry, logger *slog.Logger) error {
	if s == nil || s.store == nil || registry == nil {
		return nil
	}
	entries, err := s.store.List(loopDefinitionRegistryNamespace)
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

	records := make(map[string]looppkg.DefinitionRecord, len(entries))
	for _, key := range keys {
		var record looppkg.DefinitionRecord
		if err := json.Unmarshal([]byte(entries[key]), &record); err != nil {
			if logger != nil {
				logger.Warn("skipping invalid persisted loop definition", "name", key, "error", err)
			}
			continue
		}
		if strings.TrimSpace(record.Spec.Name) == "" {
			record.Spec.Name = key
		}
		records[key] = record
	}
	if len(records) == 0 {
		return nil
	}
	return registry.ReplaceOverlay(records)
}

func (a *App) persistLoopDefinition(spec looppkg.Spec, updatedAt time.Time) error {
	if a == nil || a.loopDefinitionStore == nil {
		return nil
	}
	return a.loopDefinitionStore.Save(spec, updatedAt)
}

func (a *App) deletePersistedLoopDefinition(name string) error {
	if a == nil || a.loopDefinitionStore == nil {
		return nil
	}
	if err := a.loopDefinitionStore.Delete(name); err != nil {
		return err
	}
	if a.loopDefinitionPolicyStore != nil {
		if err := a.loopDefinitionPolicyStore.Delete(name); err != nil {
			return err
		}
	}
	return nil
}
