package app

import (
	"fmt"
	"path/filepath"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/metacognitive"
)

func (a *App) buildLoopDefinitionBaseSpecs() ([]looppkg.Spec, error) {
	baseDefinitions := append([]looppkg.Spec(nil), a.cfg.Loops.Definitions...)
	hasMetacogDefinition := false
	for _, def := range baseDefinitions {
		if strings.TrimSpace(def.Name) == metacognitive.DefinitionName {
			hasMetacogDefinition = true
			break
		}
	}
	if a.cfg.Metacognitive.Enabled || hasMetacogDefinition {
		metacogCfg, err := metacognitive.ParseConfig(a.cfg.Metacognitive)
		if err != nil {
			return nil, fmt.Errorf("metacognitive config: %w", err)
		}
		a.metacogCfg = &metacogCfg
		if a.cfg.Metacognitive.Enabled && !hasMetacogDefinition {
			baseDefinitions = append(baseDefinitions, metacognitive.DefinitionSpec(metacogCfg))
		}
	}
	return baseDefinitions, nil
}

func (a *App) hydrateLoopDefinitionSpec(spec looppkg.Spec) (looppkg.Spec, error) {
	if a == nil {
		return spec, nil
	}
	switch strings.TrimSpace(spec.Name) {
	case metacognitive.DefinitionName:
		if a.metacogCfg == nil {
			return looppkg.Spec{}, fmt.Errorf("metacognitive definition requires metacognitive config")
		}
		stateFileName := filepath.Base(a.metacogCfg.StateFile)
		stateFilePath := filepath.Join(a.cfg.Workspace.Path, a.metacogCfg.StateFile)
		if a.provenanceStore != nil {
			stateFilePath = a.provenanceStore.FilePath(stateFileName)
		}
		runtimeSpec := metacognitive.HydrateSpec(spec, *a.metacogCfg, metacognitive.Opts{
			WorkspacePath:   a.cfg.Workspace.Path,
			StateFilePath:   stateFilePath,
			ProvenanceStore: a.provenanceStore,
			StateFileName:   stateFileName,
		})
		runtimeSpec.Setup = func(l *looppkg.Loop) {
			metacognitive.RegisterTools(a.loop.Tools(), l, *a.metacogCfg, stateFilePath, a.provenanceStore)
		}
		return runtimeSpec, nil
	default:
		return spec, nil
	}
}
