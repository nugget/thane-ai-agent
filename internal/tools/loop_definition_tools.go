package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
)

const (
	defaultLoopDefinitionListLimit = 25
	maxLoopDefinitionListLimit     = 200
)

// LoopDefinitionToolDeps wires the live loop-definition registry into the
// tool registry so the model can inspect and mutate the persistent loops-ng
// definition overlay.
type LoopDefinitionToolDeps struct {
	Registry    *looppkg.DefinitionRegistry
	PersistSpec func(looppkg.Spec, time.Time) error
	DeleteSpec  func(string) error
}

// ConfigureLoopDefinitionTools stores the runtime dependencies needed by
// the loop-definition tool family and registers the tools.
func (r *Registry) ConfigureLoopDefinitionTools(deps LoopDefinitionToolDeps) {
	r.loopDefinitionRegistry = deps.Registry
	r.persistLoopDefinition = deps.PersistSpec
	r.deletePersistedLoopDefinition = deps.DeleteSpec
	r.registerLoopDefinitionTools()
}

func (r *Registry) registerLoopDefinitionTools() {
	if r.loopDefinitionRegistry == nil {
		return
	}

	r.Register(&Tool{
		Name:        "loop_definition_summary",
		Description: "Return a compact structured summary of the persistent loops-ng definition registry: generation, counts by source, operation, and completion, plus the known loop definition names.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Handler: r.handleLoopDefinitionSummary,
	})

	r.Register(&Tool{
		Name:        "loop_definition_list",
		Description: "List persistent loop definitions with compact structured fields and optional filters. Use this to discover available service, background_task, and request_reply definitions before modifying them.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Optional case-insensitive substring match against loop name, task text, mission, and metadata values.",
				},
				"source": map[string]any{
					"type":        "string",
					"enum":        []string{"config", "overlay"},
					"description": "Optional exact source filter.",
				},
				"operation": map[string]any{
					"type":        "string",
					"enum":        []string{"request_reply", "background_task", "service"},
					"description": "Optional operation filter.",
				},
				"completion": map[string]any{
					"type":        "string",
					"enum":        []string{"return", "conversation", "channel", "none"},
					"description": "Optional completion filter.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": fmt.Sprintf("Maximum results to return (default %d, max %d).", defaultLoopDefinitionListLimit, maxLoopDefinitionListLimit),
				},
			},
		},
		Handler: r.handleLoopDefinitionList,
	})

	r.Register(&Tool{
		Name:        "loop_definition_get",
		Description: "Get one deep loop definition object from the persistent loops-ng definition registry by name.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Loop definition name.",
				},
			},
			"required": []string{"name"},
		},
		Handler: r.handleLoopDefinitionGet,
	})

	r.Register(&Tool{
		Name:        "loop_definition_set",
		Description: "Create or replace one dynamic loop definition in the persistent loops-ng overlay. This cannot modify config-owned definitions. The spec uses human-facing strings for durations and retrigger mode.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"spec": map[string]any{
					"type":        "object",
					"description": "Loop definition spec to persist into the overlay.",
				},
			},
			"required": []string{"spec"},
		},
		Handler: r.handleLoopDefinitionSet,
	})

	r.Register(&Tool{
		Name:        "loop_definition_delete",
		Description: "Delete one dynamic loop definition from the persistent loops-ng overlay. Config-owned definitions are immutable and cannot be deleted.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Loop definition name to delete from the overlay.",
				},
			},
			"required": []string{"name"},
		},
		Handler: r.handleLoopDefinitionDelete,
	})
}

func ldStringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func ldIntArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return 0
	}
}

func ldMarshalToolJSON(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeLoopSpecArg(args map[string]any, key string) (looppkg.Spec, error) {
	raw, ok := args[key]
	if !ok {
		return looppkg.Spec{}, fmt.Errorf("%s is required", key)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return looppkg.Spec{}, err
	}
	var spec looppkg.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return looppkg.Spec{}, err
	}
	return spec, nil
}

func findLoopDefinition(snapshot *looppkg.DefinitionRegistrySnapshot, name string) (looppkg.DefinitionSnapshot, bool) {
	if snapshot == nil {
		return looppkg.DefinitionSnapshot{}, false
	}
	for _, def := range snapshot.Definitions {
		if def.Name == name {
			return def, true
		}
	}
	return looppkg.DefinitionSnapshot{}, false
}

func currentLoopDefinitionSnapshot(r *Registry) (*looppkg.DefinitionRegistrySnapshot, error) {
	if r.loopDefinitionRegistry == nil {
		return nil, fmt.Errorf("loop definition registry not configured")
	}
	snapshot := r.loopDefinitionRegistry.Snapshot()
	if snapshot == nil {
		return nil, fmt.Errorf("loop definition registry snapshot unavailable")
	}
	return snapshot, nil
}

func (r *Registry) handleLoopDefinitionSummary(_ context.Context, _ map[string]any) (string, error) {
	snapshot, err := currentLoopDefinitionSnapshot(r)
	if err != nil {
		return "", err
	}
	bySource := map[string]int{}
	byOperation := map[string]int{}
	byCompletion := map[string]int{}
	names := make([]string, 0, len(snapshot.Definitions))
	for _, def := range snapshot.Definitions {
		bySource[string(def.Source)]++
		byOperation[string(def.Spec.Operation)]++
		byCompletion[string(def.Spec.Completion)]++
		names = append(names, def.Name)
	}
	return ldMarshalToolJSON(map[string]any{
		"generation":          snapshot.Generation,
		"definition_count":    len(snapshot.Definitions),
		"config_definitions":  snapshot.ConfigDefinitions,
		"overlay_definitions": snapshot.OverlayDefinitions,
		"by_source":           bySource,
		"by_operation":        byOperation,
		"by_completion":       byCompletion,
		"names":               names,
	})
}

func (r *Registry) handleLoopDefinitionList(_ context.Context, args map[string]any) (string, error) {
	snapshot, err := currentLoopDefinitionSnapshot(r)
	if err != nil {
		return "", err
	}
	query := strings.ToLower(ldStringArg(args, "query"))
	source := ldStringArg(args, "source")
	operation := ldStringArg(args, "operation")
	completion := ldStringArg(args, "completion")
	limit := ldIntArg(args, "limit")
	if limit <= 0 {
		limit = defaultLoopDefinitionListLimit
	}
	if limit > maxLoopDefinitionListLimit {
		limit = maxLoopDefinitionListLimit
	}

	items := make([]looppkg.DefinitionSnapshot, 0, len(snapshot.Definitions))
	for _, def := range snapshot.Definitions {
		if source != "" && string(def.Source) != source {
			continue
		}
		if operation != "" && string(def.Spec.Operation) != operation {
			continue
		}
		if completion != "" && string(def.Spec.Completion) != completion {
			continue
		}
		if query != "" && !loopDefinitionMatchesQuery(def, query) {
			continue
		}
		items = append(items, def)
		if len(items) >= limit {
			break
		}
	}

	return ldMarshalToolJSON(map[string]any{
		"generation": snapshot.Generation,
		"count":      len(items),
		"items":      items,
	})
}

func loopDefinitionMatchesQuery(def looppkg.DefinitionSnapshot, query string) bool {
	if strings.Contains(strings.ToLower(def.Name), query) {
		return true
	}
	if strings.Contains(strings.ToLower(def.Spec.Task), query) {
		return true
	}
	if strings.Contains(strings.ToLower(def.Spec.Profile.Mission), query) {
		return true
	}
	for key, value := range def.Spec.Metadata {
		if strings.Contains(strings.ToLower(key), query) || strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func (r *Registry) handleLoopDefinitionGet(_ context.Context, args map[string]any) (string, error) {
	snapshot, err := currentLoopDefinitionSnapshot(r)
	if err != nil {
		return "", err
	}
	name := ldStringArg(args, "name")
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	def, ok := findLoopDefinition(snapshot, name)
	if !ok {
		return "", (&looppkg.UnknownDefinitionError{Name: name})
	}
	return ldMarshalToolJSON(map[string]any{
		"generation": snapshot.Generation,
		"definition": def,
	})
}

func (r *Registry) handleLoopDefinitionSet(_ context.Context, args map[string]any) (string, error) {
	if r.loopDefinitionRegistry == nil {
		return "", fmt.Errorf("loop definition registry not configured")
	}
	spec, err := decodeLoopSpecArg(args, "spec")
	if err != nil {
		return "", err
	}
	snapshot, err := currentLoopDefinitionSnapshot(r)
	if err != nil {
		return "", err
	}
	if existing, ok := findLoopDefinition(snapshot, spec.Name); ok && existing.Source == looppkg.DefinitionSourceConfig {
		return "", (&looppkg.ImmutableDefinitionError{Name: spec.Name})
	}
	updatedAt := time.Now().UTC()
	if r.persistLoopDefinition != nil {
		if err := r.persistLoopDefinition(spec, updatedAt); err != nil {
			return "", fmt.Errorf("persist loop definition: %w", err)
		}
	}
	if err := r.loopDefinitionRegistry.Upsert(spec, updatedAt); err != nil {
		return "", err
	}
	snapshot, err = currentLoopDefinitionSnapshot(r)
	if err != nil {
		return "", err
	}
	def, ok := findLoopDefinition(snapshot, spec.Name)
	if !ok {
		return "", fmt.Errorf("loop definition stored but snapshot is unavailable")
	}
	return ldMarshalToolJSON(map[string]any{
		"status":     "ok",
		"generation": snapshot.Generation,
		"definition": def,
	})
}

func (r *Registry) handleLoopDefinitionDelete(_ context.Context, args map[string]any) (string, error) {
	if r.loopDefinitionRegistry == nil {
		return "", fmt.Errorf("loop definition registry not configured")
	}
	name := ldStringArg(args, "name")
	if name == "" {
		return "", fmt.Errorf("name is required")
	}
	snapshot, err := currentLoopDefinitionSnapshot(r)
	if err != nil {
		return "", err
	}
	if existing, ok := findLoopDefinition(snapshot, name); ok && existing.Source == looppkg.DefinitionSourceConfig {
		return "", (&looppkg.ImmutableDefinitionError{Name: name})
	} else if !ok {
		return "", (&looppkg.UnknownDefinitionError{Name: name})
	}
	if r.deletePersistedLoopDefinition != nil {
		if err := r.deletePersistedLoopDefinition(name); err != nil {
			return "", fmt.Errorf("delete persisted loop definition: %w", err)
		}
	}
	if err := r.loopDefinitionRegistry.Delete(name, time.Now().UTC()); err != nil {
		return "", err
	}
	snapshot, err = currentLoopDefinitionSnapshot(r)
	if err != nil {
		return "", err
	}
	return ldMarshalToolJSON(map[string]any{
		"status":     "ok",
		"generation": snapshot.Generation,
		"name":       name,
	})
}
