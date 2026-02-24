package tools

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/paths"
)

// ContentResolver resolves bare prefix references (temp:LABEL, kb:file.md,
// etc.) in tool argument values to file content. It is nil-safe: calling
// methods on a nil *ContentResolver is a no-op.
type ContentResolver struct {
	pathResolver  *paths.Resolver
	tempFileStore *TempFileStore
	logger        *slog.Logger
}

// NewContentResolver creates a ContentResolver backed by the given path
// resolver and temp file store. Either dependency may be nil — resolution
// for that prefix type is silently skipped. Returns nil if both
// dependencies are nil (no resolution possible).
func NewContentResolver(pr *paths.Resolver, tfs *TempFileStore, logger *slog.Logger) *ContentResolver {
	if pr == nil && tfs == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ContentResolver{
		pathResolver:  pr,
		tempFileStore: tfs,
		logger:        logger,
	}
}

// ResolveArgs walks all top-level string values in args and replaces bare
// prefix references with file content. A "bare" reference is one where the
// entire string value is the prefix reference — no surrounding whitespace
// or text. If the referenced file does not exist or cannot be read, the
// original string is kept unchanged. The method modifies args in place.
func (cr *ContentResolver) ResolveArgs(ctx context.Context, args map[string]any) {
	if cr == nil {
		return
	}
	convID := ConversationIDFromContext(ctx)
	for key, val := range args {
		s, ok := val.(string)
		if !ok || s == "" {
			continue
		}
		// Only resolve bare references — the entire string must be the
		// prefix reference with no whitespace (spaces, tabs, newlines).
		if strings.ContainsAny(s, " \t\n\r") {
			continue
		}
		if resolved, ok := cr.resolveToContent(convID, s); ok {
			args[key] = resolved
		}
	}
}

// resolveToContent attempts to resolve a bare prefix reference to file
// content. Returns (content, true) on success, or ("", false) if the
// string is not a prefix reference or the file cannot be read.
func (cr *ContentResolver) resolveToContent(convID, value string) (string, bool) {
	// Try temp: first (most specific, per-conversation).
	if strings.HasPrefix(value, "temp:") && cr.tempFileStore != nil {
		label := strings.TrimPrefix(value, "temp:")
		if label == "" {
			return "", false
		}
		path := cr.tempFileStore.Resolve(convID, label)
		if path == "" {
			cr.logger.Debug("temp label not found, passing through",
				"label", label,
				"conversation", convID,
			)
			return "", false
		}
		data, err := os.ReadFile(path)
		if err != nil {
			cr.logger.Debug("temp file unreadable, passing through",
				"label", label,
				"path", path,
				"error", err,
			)
			return "", false
		}
		cr.logger.Debug("resolved temp: prefix to content",
			"label", label,
			"bytes", len(data),
		)
		return string(data), true
	}

	// Try path prefixes (kb:, scratchpad:, etc.).
	if cr.pathResolver != nil && cr.pathResolver.HasPrefix(value) {
		absPath, err := cr.pathResolver.Resolve(value)
		if err != nil {
			cr.logger.Debug("path prefix resolution failed, passing through",
				"value", value,
				"error", err,
			)
			return "", false
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			cr.logger.Debug("prefixed file unreadable, passing through",
				"value", value,
				"path", absPath,
				"error", err,
			)
			return "", false
		}
		cr.logger.Debug("resolved path prefix to content",
			"value", value,
			"path", absPath,
			"bytes", len(data),
		)
		return string(data), true
	}

	return "", false
}
