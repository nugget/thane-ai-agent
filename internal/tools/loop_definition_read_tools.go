package tools

import (
	"context"
	"fmt"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
)

func (r *Registry) handleLoopDefinitionSummary(_ context.Context, _ map[string]any) (string, error) {
	snapshot, err := currentLoopDefinitionSnapshot(r)
	if err != nil {
		return "", err
	}
	bySource := map[string]int{}
	byOperation := map[string]int{}
	byCompletion := map[string]int{}
	byPolicyState := map[string]int{}
	names := make([]string, 0, len(snapshot.Definitions))
	for _, def := range snapshot.Definitions {
		bySource[string(def.Source)]++
		byOperation[string(def.Spec.Operation)]++
		byCompletion[string(def.Spec.Completion)]++
		byPolicyState[string(def.PolicyState)]++
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
		"by_policy_state":     byPolicyState,
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
	policyState := ldStringArg(args, "policy_state")
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
		if policyState != "" && string(def.PolicyState) != policyState {
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
