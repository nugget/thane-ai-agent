package app

import (
	"fmt"
	"path/filepath"

	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/state/attachments"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func (a *App) initAttachmentRuntime() error {
	if a.cfg.Attachments.StoreDir != "" {
		storeDir := paths.ExpandHome(a.cfg.Attachments.StoreDir)
		attachDBPath := filepath.Join(a.cfg.DataDir, "attachments.db")
		var err error
		a.attachmentStore, err = attachments.NewStore(attachDBPath, storeDir, a.logger)
		if err != nil {
			return fmt.Errorf("init attachment store: %w", err)
		}
		a.onCloseErr("attachments", a.attachmentStore.Close)
		a.logger.Info("attachment store initialized",
			"db", attachDBPath,
			"store_dir", storeDir,
		)
	}

	if a.attachmentStore != nil && a.cfg.Attachments.Vision.Enabled {
		a.visionAnalyzer = attachments.NewAnalyzer(a.attachmentStore, attachments.AnalyzerConfig{
			Client:  a.llmClient,
			Model:   a.cfg.Attachments.Vision.Model,
			Prompt:  a.cfg.Attachments.Vision.Prompt,
			Timeout: a.cfg.Attachments.Vision.ParsedTimeout(),
			Logger:  a.logger,
		})
		a.logger.Info("vision analyzer enabled",
			"model", a.cfg.Attachments.Vision.Model,
			"timeout", a.cfg.Attachments.Vision.ParsedTimeout(),
		)
	}

	if a.attachmentStore != nil {
		attachmentTools := attachments.NewTools(a.attachmentStore, a.visionAnalyzer)
		a.loop.Tools().SetAttachmentTools(attachmentTools)
		a.logger.Info("attachment tools registered")
	}

	return nil
}

func (a *App) initFileTools(s *newState) {
	if a.cfg.Workspace.Path != "" {
		fileTools := tools.NewFileTools(a.cfg.Workspace.Path, a.cfg.Workspace.ReadOnlyDirs)
		if s.resolver != nil {
			fileTools.SetResolver(s.resolver)
		}
		a.loop.Tools().SetFileTools(fileTools)
		a.logger.Info("file tools enabled", "workspace", a.cfg.Workspace.Path)
		return
	}

	a.logger.Info("file tools disabled (no workspace path configured)")
}
