package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/opstate"
)

// labelPattern restricts labels to safe filesystem characters: alphanumeric
// start, followed by alphanumeric, underscore, or hyphen, up to 63 chars.
var labelPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

// TempFileStore manages temporary files created for orchestrator-delegate
// data passing. Files are written to a workspace subdirectory and tracked
// via opstate so labels can be expanded to paths and cleaned up when the
// conversation ends.
type TempFileStore struct {
	baseDir string
	state   *opstate.Store
	logger  *slog.Logger
}

// NewTempFileStore creates a TempFileStore rooted at baseDir. The directory
// is created on first write, not at construction time.
func NewTempFileStore(baseDir string, state *opstate.Store, logger *slog.Logger) *TempFileStore {
	if logger == nil {
		logger = slog.Default()
	}
	return &TempFileStore{
		baseDir: baseDir,
		state:   state,
		logger:  logger,
	}
}

// tempfileNamespace returns the opstate namespace for a conversation's
// temp file mappings.
func tempfileNamespace(convID string) string {
	return "tempfile:" + convID
}

// Create writes content to a temp file and maps the label to its path.
// The returned string is the label itself (not the path). If a label
// already exists for this conversation, the old file is removed and the
// mapping updated.
func (s *TempFileStore) Create(ctx context.Context, convID, label, content string) (string, error) {
	if !labelPattern.MatchString(label) {
		return "", fmt.Errorf("invalid label %q: must be 1-63 alphanumeric/underscore/hyphen characters starting with alphanumeric", label)
	}

	suffix, err := randomSuffix()
	if err != nil {
		return "", fmt.Errorf("generate random suffix: %w", err)
	}

	safeConvID := sanitizeForFilesystem(convID)
	filename := fmt.Sprintf("%s_%s_%s.md", safeConvID, label, suffix)
	absPath := filepath.Join(s.baseDir, filename)

	// Ensure base directory exists.
	if err := os.MkdirAll(s.baseDir, 0o755); err != nil {
		return "", fmt.Errorf("create temp directory: %w", err)
	}

	// If label already exists, remove the old file first.
	ns := tempfileNamespace(convID)
	if existing, _ := s.state.Get(ns, label); existing != "" {
		_ = os.Remove(existing) // best-effort
	}

	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write temp file: %w", err)
	}

	if err := s.state.Set(ns, label, absPath); err != nil {
		_ = os.Remove(absPath) // rollback on mapping failure
		return "", fmt.Errorf("store label mapping: %w", err)
	}

	s.logger.Info("temp file created",
		"conversation", convID,
		"label", label,
		"path", absPath,
		"bytes", len(content),
	)

	return label, nil
}

// Resolve returns the filesystem path for a label in the given
// conversation. Returns empty string if the label does not exist.
func (s *TempFileStore) Resolve(convID, label string) string {
	ns := tempfileNamespace(convID)
	path, _ := s.state.Get(ns, label)
	return path
}

// ExpandLabels replaces all occurrences of "temp:LABEL" in text with
// the corresponding file path for the given conversation. Unknown labels
// are left as-is.
func (s *TempFileStore) ExpandLabels(convID, text string) string {
	ns := tempfileNamespace(convID)
	mappings, err := s.state.List(ns)
	if err != nil {
		s.logger.Warn("failed to list temp file labels",
			"conversation", convID,
			"error", err,
		)
		return text
	}
	if len(mappings) == 0 {
		return text
	}

	// Sort labels by descending length so longer labels are replaced
	// first. This prevents a short label from matching a prefix of a
	// longer one (e.g., "temp:a" matching inside "temp:ab").
	labels := make([]string, 0, len(mappings))
	for label := range mappings {
		labels = append(labels, label)
	}
	sort.Slice(labels, func(i, j int) bool {
		return len(labels[i]) > len(labels[j])
	})

	for _, label := range labels {
		text = strings.ReplaceAll(text, "temp:"+label, mappings[label])
	}
	return text
}

// Cleanup removes all temp files and opstate entries for a conversation.
// Errors on individual file removals are logged but do not prevent
// cleanup of remaining files.
func (s *TempFileStore) Cleanup(convID string) error {
	ns := tempfileNamespace(convID)
	mappings, err := s.state.List(ns)
	if err != nil {
		return fmt.Errorf("list temp files for cleanup: %w", err)
	}

	if len(mappings) == 0 {
		return nil
	}

	for label, path := range mappings {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			s.logger.Warn("failed to remove temp file",
				"conversation", convID,
				"label", label,
				"path", path,
				"error", err,
			)
		}
	}

	if err := s.state.DeleteNamespace(ns); err != nil {
		return fmt.Errorf("delete temp file namespace: %w", err)
	}

	s.logger.Info("temp files cleaned up",
		"conversation", convID,
		"count", len(mappings),
	)
	return nil
}

// randomSuffix generates a 4-byte (8 hex char) random string.
func randomSuffix() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// sanitizeForFilesystem replaces characters that are not alphanumeric,
// underscore, or hyphen with underscores. Used for embedding conversation
// IDs in filenames.
func sanitizeForFilesystem(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('_')
		}
	}
	result := sb.String()
	if len(result) > 64 {
		result = result[:64]
	}
	return result
}
