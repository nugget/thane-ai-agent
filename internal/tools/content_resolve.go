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
// resolver and temp file store. The path resolver may be nil — resolution
// for path-based prefixes is silently skipped. If the temp file store is
// nil, any temp: references will return an error (they are always
// intentional). Returns nil if both dependencies are nil (no resolution
// possible).
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

// ResolveArgs recursively walks all values in args and replaces bare prefix
// references with file content. A "bare" reference is one where the entire
// string value is the prefix reference — no surrounding whitespace or text.
// Nested maps and arrays are traversed.
//
// For temp: references, resolution failures are always treated as errors —
// these are intentional and a missing label (or unconfigured store) likely
// indicates a typo, stale reference, or misconfiguration. For path prefixes
// (kb:, scratchpad:, etc.), missing files pass through silently since a
// nonexistent file is valid state.
//
// The method modifies args in place.
func (cr *ContentResolver) ResolveArgs(ctx context.Context, args map[string]any) error {
	if cr == nil {
		return nil
	}
	convID := ConversationIDFromContext(ctx)
	for key, val := range args {
		resolved, err := cr.resolveArgValue(convID, val)
		if err != nil {
			return fmt.Errorf("resolve parameter %q: %w", key, err)
		}
		args[key] = resolved
	}
	return nil
}

// resolveArgValue recursively resolves prefix references in a single
// argument value. Strings are checked for bare prefix references; maps
// and slices are traversed recursively; other types pass through.
func (cr *ContentResolver) resolveArgValue(convID string, val any) (any, error) {
	switch v := val.(type) {
	case string:
		if v == "" || strings.ContainsAny(v, " \t\n\r") {
			return v, nil
		}
		resolved, didResolve, err := cr.resolveToContent(convID, v)
		if err != nil {
			return nil, err
		}
		if didResolve {
			return resolved, nil
		}
		return v, nil
	case map[string]any:
		for key, elem := range v {
			resolved, err := cr.resolveArgValue(convID, elem)
			if err != nil {
				return nil, fmt.Errorf("key %q: %w", key, err)
			}
			v[key] = resolved
		}
		return v, nil
	case []any:
		for i, elem := range v {
			resolved, err := cr.resolveArgValue(convID, elem)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
			v[i] = resolved
		}
		return v, nil
	default:
		return v, nil
	}
}

// resolveToContent attempts to resolve a bare prefix reference to file
// content. Returns (content, true, nil) on successful resolution,
// ("", false, nil) for non-matching strings, and ("", false, error)
// on failures.
//
// For temp: prefixes, failures always return an error — even if no
// TempFileStore is configured, since the caller clearly intended a
// temp: reference. For path prefixes, failures pass through silently
// (returns empty string, false, nil).
func (cr *ContentResolver) resolveToContent(convID, value string) (string, bool, error) {
	// Try temp: first (most specific, per-conversation).
	// Failures here are always errors — temp labels are intentional references.
	if strings.HasPrefix(value, "temp:") {
		label := strings.TrimPrefix(value, "temp:")
		if label == "" {
			return "", false, nil
		}
		if cr.tempFileStore == nil {
			return "", false, fmt.Errorf("unknown temp label %q (temp file store not configured)", label)
		}
		path := cr.tempFileStore.Resolve(convID, label)
		if path == "" {
			return "", false, fmt.Errorf("unknown temp label %q", label)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", false, fmt.Errorf("read temp file %q: %w", label, err)
		}
		cr.logger.Debug("resolved temp: prefix to content",
			"label", label,
			"bytes", len(data),
		)
		return string(data), true, nil
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
			return "", false, nil
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			cr.logger.Debug("prefixed file unreadable, passing through",
				"value", value,
				"path", absPath,
				"error", err,
			)
			return "", false, nil
		}
		cr.logger.Debug("resolved path prefix to content",
			"value", value,
			"path", absPath,
			"bytes", len(data),
		)
		return string(data), true, nil
	}

	return "", false, nil
}
