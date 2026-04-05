package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/nugget/thane-ai-agent/internal/buildinfo"
	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/database"
	"github.com/nugget/thane-ai-agent/internal/logging"
)

// initLogging reconfigures the logger with the desired level, format, and
// output destination (rotated files + stdout). It also opens the SQLite
// log index, content writer, and registers background workers for log
// index pruning and content archival.
//
// On return, a.logger is the final configured logger for all subsequent
// initialization and runtime logging.
func (a *App) initLogging(augmentedDirs []string) error {
	cfg := a.cfg
	logger := a.logger
	stdout := a.stdout

	level, _ := config.ParseLogLevel(cfg.Logging.Level)

	// Open the log rotator for file output. Logs go to both
	// stdout (for launchd/systemd capture) and the rotated file.
	// When Dir is empty, file logging is disabled (stdout only).
	logWriter := stdout
	var rotator *logging.Rotator

	if logDir := cfg.Logging.DirPath(); logDir != "" {
		var err error
		rotator, err = logging.Open(logDir, cfg.Logging.CompressEnabled())
		if err != nil {
			// File logging failed — fall back to stdout only.
			logger.Warn("failed to open log directory, using stdout only",
				"dir", logDir, "error", err)
		} else {
			a.rotator = rotator
			a.onCloseErr("rotator", rotator.Close)
			logWriter = io.MultiWriter(stdout, rotator)
		}
	}

	handler := newHandler(logWriter, level, cfg.Logging.Format)

	// Open the SQLite log index alongside the raw log files.
	// If file logging is disabled (no logDir) or the DB fails to
	// open, logging continues without indexing.
	if logDir := cfg.Logging.DirPath(); logDir != "" {
		var err error
		a.indexDB, err = database.Open(filepath.Join(logDir, "logs.db"))
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
			indexHandler := logging.NewIndexHandler(handler, a.indexDB, rotator)
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
	a.requestRecorder = a.liveRequestStore.WriteRequest

	// Persistent content retention — create after the final logger so
	// warnings go through the configured handler.
	if cfg.Logging.RetainContent && a.indexDB != nil {
		cw, cwErr := logging.NewContentWriter(a.indexDB, cfg.Logging.ContentMaxLength(), logger)
		if cwErr != nil {
			logger.Warn("failed to create content writer, content retention disabled", "error", cwErr)
		} else {
			a.contentWriter = cw
			a.requestRecorder = logging.CombineRequestRecorders(a.liveRequestStore.WriteRequest, cw.WriteRequest)
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
		if logDir := cfg.Logging.DirPath(); logDir != "" {
			if archiveDur := cfg.Logging.ContentArchiveDuration(); archiveDur > 0 && a.contentWriter != nil {
				archiveDir := cfg.Logging.ContentArchiveDirPath(logDir)
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
