package tools

import (
	"context"
	"fmt"
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
// or text.
//
// For temp: references, resolution failures are treated as errors — these
// are always intentional and a missing label likely indicates a typo or
// stale reference. For path prefixes (kb:, scratchpad:, etc.), missing
// files pass through silently since a nonexistent file is valid state.
//
// The method modifies args in place.
func (cr *ContentResolver) ResolveArgs(ctx context.Context, args map[string]any) error {
	if cr == nil {
		return nil
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
		resolved, err := cr.resolveToContent(convID, s)
		if err != nil {
			return fmt.Errorf("resolve %q in parameter %q: %w", s, key, err)
		}
		if resolved != "" {
			args[key] = resolved
		}
	}
	return nil
}

// resolveToContent attempts to resolve a bare prefix reference to file
// content.
//
// For temp: prefixes, failures return an error (label not found or file
// unreadable). For path prefixes, failures pass through silently (returns
// empty string, nil error). Returns ("", nil) for strings that don't
// match any registered prefix.
func (cr *ContentResolver) resolveToContent(convID, value string) (string, error) {
	// Try temp: first (most specific, per-conversation).
	// Failures here are errors — temp labels are intentional references.
	if strings.HasPrefix(value, "temp:") && cr.tempFileStore != nil {
		label := strings.TrimPrefix(value, "temp:")
		if label == "" {
			return "", nil
		}
		path := cr.tempFileStore.Resolve(convID, label)
		if path == "" {
			return "", fmt.Errorf("unknown temp label %q", label)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read temp file %q: %w", label, err)
		}
		cr.logger.Debug("resolved temp: prefix to content",
			"label", label,
			"bytes", len(data),
		)
		return string(data), nil
	}

	// Try path prefixes (kb:, scratchpad:, etc.).
	// Failures here pass through — a missing file is valid state.
	if cr.pathResolver != nil && cr.pathResolver.HasPrefix(value) {
		absPath, err := cr.pathResolver.Resolve(value)
		if err != nil {
			cr.logger.Debug("path prefix resolution failed, passing through",
				"value", value,
				"error", err,
			)
			return "", nil
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			cr.logger.Debug("prefixed file unreadable, passing through",
				"value", value,
				"path", absPath,
				"error", err,
			)
			return "", nil
		}
		cr.logger.Debug("resolved path prefix to content",
			"value", value,
			"path", absPath,
			"bytes", len(data),
		)
		return string(data), nil
	}

	return "", nil
}
