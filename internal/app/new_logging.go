package app

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/nugget/thane-ai-agent/internal/buildinfo"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/logging"
)

// initLogging reconfigures the logger with the desired stdout policy and
// structured dataset storage. It also opens the SQLite log index, content
// writer, and registers background workers for log index pruning and content
// archival.
//
// On return, a.logger is the final configured logger for all subsequent
// initialization and runtime logging.
func (a *App) initLogging(augmentedDirs []string) error {
	cfg := a.cfg
	logger := a.logger
	stdout := a.stdout

	level, _ := config.ParseLogLevel(cfg.Logging.Level)
	stdoutLevel, _ := config.ParseLogLevel(cfg.Logging.StdoutLevelValue())

	var stdoutHandler slog.Handler
	if cfg.Logging.StdoutEnabled() {
		stdoutHandler = newHandler(stdout, stdoutLevel, cfg.Logging.StdoutFormatValue())
	}

	logRoot := cfg.Logging.RootPath()
	var datasetWriter *logging.DatasetWriter
	if logRoot != "" {
		var err error
		datasetWriter, err = logging.OpenDatasetWriter(logRoot)
		if err != nil {
			logger.Warn("failed to open logging root, using stdout only",
				"root", logRoot, "error", err)
			datasetWriter = nil
		} else {
			a.datasetWriter = datasetWriter
			a.onCloseErr("dataset-writer", datasetWriter.Close)
		}
	}

	var handler slog.Handler = logging.NewDatasetHandler(stdoutHandler, datasetWriter, logging.DatasetHandlerOptions{
		DatasetLevel:    level,
		StdoutLevel:     stdoutLevel,
		StdoutEnabled:   cfg.Logging.StdoutEnabled(),
		EventsEnabled:   cfg.Logging.DatasetEnabled(logging.DatasetEvents),
		RequestsEnabled: cfg.Logging.DatasetEnabled(logging.DatasetRequests),
		AccessEnabled:   cfg.Logging.DatasetEnabled(logging.DatasetAccess),
	})

	// Open the SQLite log index alongside the structured dataset root.
	// If filesystem logging is disabled (no logRoot) or the DB fails to
	// open, logging continues without indexing.
	if logRoot != "" {
		var err error
		a.indexDB, err = database.Open(filepath.Join(logRoot, "logs.db"))
		if err != nil {
			logger.Warn("failed to open log index database, indexing disabled",
				"error", err)
			a.indexDB = nil
		} else if err := logging.Migrate(a.indexDB); err != nil {
			logger.Warn("failed to migrate log index schema, indexing disabled",
				"error", err)
			a.indexDB.Close()
			a.indexDB = nil
		} else {
			indexHandler := logging.NewIndexHandler(handler, a.indexDB)
			a.onCloseErr("index-db", a.indexDB.Close)
			handler = indexHandler
			a.indexHandler = indexHandler
			a.onClose("index-handler", indexHandler.Close)
		}
	}

	logger = slog.New(handler).With(
		"thane_version", buildinfo.Version,
		"thane_commit", buildinfo.GitCommit,
	)
	a.logger = logger

	// Live request inspection is always available from a bounded in-memory
	// buffer so recent turns can be inspected even when archival storage
	// is disabled.
	a.liveRequestStore = logging.NewLiveRequestStore(logging.DefaultLiveRequestStoreSize, cfg.Logging.ContentMaxLength())
	a.liveRequestRecorder = a.liveRequestStore.WriteRequest
	a.requestRecorder = a.liveRequestRecorder

	// Persistent content retention — create after the final logger so
	// warnings go through the configured handler.
	if cfg.Logging.RetainContent && a.indexDB != nil {
		cw, cwErr := logging.NewContentWriter(a.indexDB, cfg.Logging.ContentMaxLength(), logger)
		if cwErr != nil {
			logger.Warn("failed to create content writer, content retention disabled", "error", cwErr)
		} else {
			a.contentWriter = cw
			a.requestRecorder = logging.CombineRequestRecorders(a.liveRequestRecorder, cw.WriteRequest)
			a.onCloseErr("content-writer", cw.Close)
			logger.Info("content retention enabled",
				"max_content_length", cfg.Logging.ContentMaxLength(),
			)
		}
	}

	// Log PATH augmentation now that the final logger is configured.
	if len(augmentedDirs) > 0 {
		logger.Debug("augmented PATH", "prepended", augmentedDirs)
	}

	// Defer background log index pruner if retention is configured and
	// the index database is available.
	if a.indexDB != nil {
		if retention := cfg.Logging.RetentionDaysDuration(); retention > 0 {
			a.deferWorker("log-index-pruner", func(ctx context.Context) error {
				go func() {
					ticker := time.NewTicker(24 * time.Hour)
					defer ticker.Stop()
					for {
						if deleted, err := logging.Prune(a.indexDB, retention, slog.LevelInfo); err != nil {
							logger.Warn("log index prune failed", "error", err, "retention", retention)
						} else if deleted > 0 {
							logger.Info("pruned log index", "deleted", deleted, "retention", retention)
						} else {
							logger.Debug("log index prune ran; nothing to delete", "retention", retention)
						}
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
						}
					}
				}()
				return nil
			})
		}

		// Defer background content archiver: exports retained request/tool
		// content older than ContentArchiveDays to monthly JSONL files, then
		// removes it from logs.db. Runs daily; disabled when archive duration
		// is 0 or content retention is off.
		if logRoot != "" {
			if archiveDur := cfg.Logging.ContentArchiveDuration(); archiveDur > 0 && a.contentWriter != nil {
				archiveDir := cfg.Logging.ContentArchiveDirPath(logRoot)
				archiver := logging.NewArchiver(a.indexDB, archiveDir, logger)
				a.deferWorker("content-archiver", func(ctx context.Context) error {
					go func() {
						ticker := time.NewTicker(24 * time.Hour)
						defer ticker.Stop()
						for {
							before := time.Now().Add(-archiveDur)
							runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
							n, err := archiver.Archive(runCtx, before)
							cancel()
							if err != nil {
								logger.Warn("content archive failed", "error", err, "before", before)
							} else if n > 0 {
								logger.Info("content archived", "requests", n, "before", before)
							} else {
								logger.Debug("content archive ran; nothing to archive", "before", before)
							}
							select {
							case <-ctx.Done():
								return
							case <-ticker.C:
							}
						}
					}()
					return nil
				})
				logger.Info("content archival enabled", "archive_after", archiveDur)
			}
		}
	}

	// Warn about deprecated config fields.
	if depLevel, depFormat := cfg.DeprecatedFieldsUsed(); depLevel || depFormat {
		logger.Warn("log_level/log_format are deprecated; use logging.level/logging.format instead")
	}

	return nil
}
