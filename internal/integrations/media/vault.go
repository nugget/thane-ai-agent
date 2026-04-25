package media

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// AnalysisPage holds the data for a single media analysis markdown file.
// The Content field is the agent-generated analysis body; everything
// else becomes YAML frontmatter.
type AnalysisPage struct {
	Title        string
	Channel      string
	URL          string
	Published    string // YYYY-MM-DD or empty (falls back to today)
	Topics       []string
	TrustZone    string
	QualityScore float64
	AnalyzedAt   time.Time
	Content      string // Markdown body written by the agent
}

// VaultWriter writes structured analysis markdown files to an
// Obsidian-compatible vault directory. Each channel gets a
// subdirectory under Channels/ with an auto-maintained _channel.md
// index file.
type VaultWriter struct {
	logger *slog.Logger
}

// NewVaultWriter creates a vault writer.
func NewVaultWriter(logger *slog.Logger) *VaultWriter {
	if logger == nil {
		logger = slog.Default()
	}
	return &VaultWriter{logger: logger}
}

// WriteAnalysis writes an analysis page to the vault and updates the
// channel index. Returns the path of the written file (absolute if
// outputPath is absolute).
//
// Directory structure:
//
//	{outputPath}/Channels/{channel-slug}/{date}-{title-slug}-{hash}.md
//	{outputPath}/Channels/{channel-slug}/_channel.md
func (w *VaultWriter) WriteAnalysis(outputPath string, page *AnalysisPage) (string, error) {
	if outputPath == "" {
		return "", fmt.Errorf("output path is required")
	}
	if page.Title == "" {
		return "", fmt.Errorf("title is required")
	}
	if page.Channel == "" {
		return "", fmt.Errorf("channel is required")
	}

	channelSlug := slugify(page.Channel)
	channelDir := filepath.Join(outputPath, "Channels", channelSlug)

	if err := os.MkdirAll(channelDir, 0o755); err != nil {
		return "", fmt.Errorf("create channel directory: %w", err)
	}

	// Determine date prefix from Published or today. Validate the date
	// to prevent path traversal or malformed filenames.
	datePrefix := page.Published
	if datePrefix != "" {
		if _, err := time.Parse("2006-01-02", datePrefix); err != nil {
			w.logger.Warn("invalid published date, falling back to today",
				"published", datePrefix,
				"error", err,
			)
			datePrefix = ""
		}
	}
	if datePrefix == "" {
		datePrefix = time.Now().UTC().Format("2006-01-02")
	}

	// Generate filename with short URL hash for uniqueness.
	titleSlug := slugify(page.Title)
	if len(titleSlug) > 60 {
		titleSlug = titleSlug[:60]
		titleSlug = strings.TrimRight(titleSlug, "-")
	}
	urlHash := shortHash(page.URL)
	filename := fmt.Sprintf("%s-%s-%s.md", datePrefix, titleSlug, urlHash)

	filePath := filepath.Join(channelDir, filename)

	// Build the markdown content.
	content := w.buildMarkdown(page)

	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write analysis file: %w", err)
	}

	w.logger.Info("analysis written to vault",
		"path", filePath,
		"channel", page.Channel,
		"title", page.Title,
	)

	// Update channel index (best-effort).
	if err := w.updateChannelIndex(channelDir, page.Channel, page.TrustZone); err != nil {
		w.logger.Warn("failed to update channel index",
			"channel_dir", channelDir,
			"error", err,
		)
	}

	return filePath, nil
}

// buildMarkdown assembles the YAML frontmatter and body.
func (w *VaultWriter) buildMarkdown(page *AnalysisPage) string {
	var sb strings.Builder

	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("title: %q\n", page.Title))
	sb.WriteString(fmt.Sprintf("channel: %q\n", page.Channel))
	sb.WriteString(fmt.Sprintf("url: %q\n", page.URL))

	if page.Published != "" {
		sb.WriteString(fmt.Sprintf("published: %s\n", page.Published))
	}

	// Topics as YAML array.
	sb.WriteString("topics:\n")
	for _, t := range page.Topics {
		sb.WriteString(fmt.Sprintf("  - %q\n", t))
	}

	if page.TrustZone != "" {
		sb.WriteString(fmt.Sprintf("trust_zone: %q\n", page.TrustZone))
	}
	if page.QualityScore > 0 {
		sb.WriteString(fmt.Sprintf("quality_score: %.2f\n", page.QualityScore))
	}

	analyzedAt := page.AnalyzedAt
	if analyzedAt.IsZero() {
		analyzedAt = time.Now().UTC()
	}
	sb.WriteString(fmt.Sprintf("analyzed: %s\n", analyzedAt.Format(time.RFC3339)))

	sb.WriteString("---\n\n")

	sb.WriteString(fmt.Sprintf("# %s\n\n", page.Title))
	sb.WriteString(page.Content)

	// Ensure trailing newline.
	if !strings.HasSuffix(page.Content, "\n") {
		sb.WriteString("\n")
	}

	return sb.String()
}

// updateChannelIndex rebuilds the _channel.md index file by scanning
// the channel directory for analysis files. The index lists all
// analyses with Obsidian wiki-links.
func (w *VaultWriter) updateChannelIndex(channelDir, channelName, trustZone string) error {
	entries, err := os.ReadDir(channelDir)
	if err != nil {
		return fmt.Errorf("read channel dir: %w", err)
	}

	type indexEntry struct {
		filename string
		title    string
	}

	var analyses []indexEntry
	for _, entry := range entries {
		name := entry.Name()
		if name == "_channel.md" || entry.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		// Extract title from the file (first # heading).
		title := extractTitle(filepath.Join(channelDir, name))
		if title == "" {
			title = strings.TrimSuffix(name, ".md")
		}
		analyses = append(analyses, indexEntry{
			filename: strings.TrimSuffix(name, ".md"),
			title:    title,
		})
	}

	// Sort by filename descending (newest first, since filenames start with date).
	sort.Slice(analyses, func(i, j int) bool {
		return analyses[i].filename > analyses[j].filename
	})

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("channel: %q\n", channelName))
	if trustZone != "" {
		sb.WriteString(fmt.Sprintf("trust_zone: %q\n", trustZone))
	}
	sb.WriteString(fmt.Sprintf("updated: %s\n", time.Now().UTC().Format(time.RFC3339)))
	sb.WriteString("---\n\n")
	sb.WriteString(fmt.Sprintf("# %s\n\n", channelName))
	sb.WriteString("## Analyses\n\n")

	for _, a := range analyses {
		sb.WriteString(fmt.Sprintf("- [[%s|%s]]\n", a.filename, a.title))
	}

	indexPath := filepath.Join(channelDir, "_channel.md")
	return os.WriteFile(indexPath, []byte(sb.String()), 0o644)
}

// extractTitle reads the first markdown heading from a file.
func extractTitle(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimPrefix(line, "# ")
		}
	}
	return ""
}

// slugRe matches one or more non-alphanumeric/non-hyphen characters.
var slugRe = regexp.MustCompile(`[^a-z0-9-]+`)

// slugify converts a string to a URL/filesystem-safe slug.
// Lowercases, replaces non-alphanumeric runs with hyphens, and trims
// leading/trailing hyphens.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "untitled"
	}
	return s
}

// shortHash returns the first 8 hex characters of the SHA-256 hash of s.
func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:4]) // 8 hex chars
}
