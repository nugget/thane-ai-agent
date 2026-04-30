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
		raw := []byte(entries[key])
		if err := json.Unmarshal(raw, &record); err != nil {
			if logger != nil {
				logger.Warn("skipping invalid persisted loop definition", "name", key, "error", err)
			}
			continue
		}
		if strings.TrimSpace(record.Spec.Name) == "" {
			record.Spec.Name = key
		}
		// Pre-migration records may have stored capability tags under
		// spec.profile.initial_tags. The field has since moved to
		// spec.tags as the single source of truth. Hoist any legacy
		// value forward so operators don't lose configured tags.
		if legacy := extractLegacyProfileInitialTags(raw); len(legacy) > 0 {
			record.Spec.Tags = mergePreservingOrder(record.Spec.Tags, legacy)
			if logger != nil {
				logger.Info("migrated legacy spec.profile.initial_tags to spec.tags",
					"name", key, "tags", legacy)
			}
		}
		records[key] = record
	}
	if len(records) == 0 {
		return nil
	}
	return registry.ReplaceOverlay(records)
}

// extractLegacyProfileInitialTags returns any non-empty
// spec.profile.initial_tags slice from a persisted definition record's
// raw JSON. The field moved off LoopProfile, so this is the only path
// that can recover values written by pre-migration servers.
func extractLegacyProfileInitialTags(raw []byte) []string {
	var envelope struct {
		Spec struct {
			Profile struct {
				InitialTags []string `json:"initial_tags"`
			} `json:"profile"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil
	}
	return envelope.Spec.Profile.InitialTags
}

// mergePreservingOrder returns a deduplicated slice containing first the
// existing entries and then any new entries not already present. Order
// of first appearance is preserved.
func mergePreservingOrder(existing, additional []string) []string {
	seen := make(map[string]struct{}, len(existing)+len(additional))
	out := make([]string, 0, len(existing)+len(additional))
	for _, v := range existing {
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	for _, v := range additional {
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
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
