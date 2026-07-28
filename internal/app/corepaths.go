package app

import (
	"github.com/nugget/thane-ai-agent/internal/platform/config"
)

func coreFilePath(workspacePath, name string) string {
	cfg := config.Config{
		Workspace: config.WorkspaceConfig{Path: workspacePath},
	}
	return cfg.CoreFile(name)
}

func selfFilePath(workspacePath, name string) string {
	cfg := config.Config{
		Workspace: config.WorkspaceConfig{Path: workspacePath},
	}
	return cfg.SelfFile(name)
}
