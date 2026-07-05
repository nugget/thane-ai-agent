package app

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/awareness"
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
		// Validate before adding to the batch. ReplaceOverlay is
		// all-or-nothing: it returns on the first ValidatePersistable
		// failure, and this error is fatal at startup (new_stores.go), so
		// a single corrupt persisted definition would otherwise block
		// every healthy overlay loop from loading. Mirror the bad-JSON
		// skip above — warn and drop the offender, keep the rest. This is
		// the safety net that lets OutputSpec.Validate tighten (#1068)
		// without bricking boot while already-corrupt overlays still
		// exist on disk; the dropped record stays persisted for later
		// repair.
		if err := record.Spec.ValidatePersistable(); err != nil {
			if logger != nil {
				logger.Warn("skipping invalid persisted loop definition",
					"name", key, "error", err)
			}
			continue
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
	if err := a.loopDefinitionStore.Save(spec, updatedAt); err != nil {
		return err
	}
	// Every spec write re-projects its Subscriptions into the
	// awareness registry — the single-writer discipline that keeps
	// the registry a faithful mirror (#1209).
	a.mirrorLoopSubscriptions(spec)
	return nil
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
	// Reap the definition's mirrored registry rows. Best-effort: the
	// startup orphan sweep catches any miss. Reserved owners (core's
	// always-visible tier, the system floor) are never a definition's
	// mirror and must not be reaped by a name collision.
	if a.watchlistStore != nil && strings.TrimSpace(name) != "" &&
		name != awareness.OwnerCore && name != awareness.OwnerSystem {
		if err := a.watchlistStore.RemoveAllForOwner(name); err != nil {
			a.logger.Warn("failed to remove mirrored subscription rows for deleted definition",
				"name", name, "error", err)
		} else if a.ingestFilterRebuild != nil {
			a.ingestFilterRebuild()
		}
	}
	return nil
}
