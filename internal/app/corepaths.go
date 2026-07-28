package app

import (
	"github.com/nugget/thane-ai-agent/internal/platform/config"
)

// coreDocumentRoot is the reserved name of the core document root, whose path
// is always derived from the workspace rather than configured.
const coreDocumentRoot = "core"

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
