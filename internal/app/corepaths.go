package app

import (
	"github.com/nugget/thane-ai-agent/internal/config"
)

func coreRootPath(workspacePath string) string {
	cfg := config.Config{
		Workspace: config.WorkspaceConfig{Path: workspacePath},
	}
	return cfg.CoreRoot()
}

func coreFilePath(workspacePath, name string) string {
	cfg := config.Config{
		Workspace: config.WorkspaceConfig{Path: workspacePath},
	}
	return cfg.CoreFile(name)
}
