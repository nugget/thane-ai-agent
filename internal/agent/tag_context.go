package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/talents"
)

// TagContextAssembler builds the Capability Context section from two
// sources for each active tag:
//
//  1. Tagged KB articles — markdown files with tags: frontmatter in the
//     knowledge base directory (same pattern as talents)
//  2. Live providers — [TagContextProvider] implementations producing
//     fresh context each turn
//
// Both the main agent loop and delegate executor share a single assembler
// to avoid duplicating the assembly logic. The assembler is safe for
// concurrent use after construction.
type TagContextAssembler struct {
	capTags    map[string]config.CapabilityTagConfig
	kbArticles []kbArticle                // pre-scanned, sorted
	haInject   homeassistant.StateFetcher // nil-safe — delegates pass nil
	logger     *slog.Logger
}

// kbArticle is a knowledge base file with tag affinity parsed from
// frontmatter. Reuses the talent frontmatter format: tags: [a, b].
type kbArticle struct {
	Path     string   // absolute file path
	Tags     []string // from frontmatter
	Kind     string   // frontmatter kind: decision_tree or empty/article
	Teaser   string   // short menu teaser for decision-tree docs
	NextTags []string // suggested next tags from a decision tree
	Name     string   // filename without .md
}

// KBMenuHint captures decision-tree metadata that can be surfaced in
// the capability menu before a tag is activated.
type KBMenuHint struct {
	Teaser   string
	NextTags []string
}

// TagContextAssemblerConfig holds the construction parameters for a
// TagContextAssembler.
type TagContextAssemblerConfig struct {
	CapTags  map[string]config.CapabilityTagConfig
	KBDir    string                     // resolved kb: directory; empty skips scanning
	HAInject homeassistant.StateFetcher // nil-safe
	Logger   *slog.Logger
}

// NewTagContextAssembler creates an assembler, scanning the KB directory
// for tagged articles at construction time. KB scan errors are logged
// but do not prevent construction.
func NewTagContextAssembler(cfg TagContextAssemblerConfig) *TagContextAssembler {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	var articles []kbArticle
	if cfg.KBDir != "" {
		var err error
		articles, err = scanKBArticles(cfg.KBDir)
		if err != nil {
			cfg.Logger.Warn("failed to scan KB directory for tagged articles",
				"dir", cfg.KBDir, "error", err)
		}
	}

	return &TagContextAssembler{
		capTags:    cfg.CapTags,
		kbArticles: articles,
		haInject:   cfg.HAInject,
		logger:     cfg.Logger,
	}
}

// Build assembles tag context for the given active tags. Providers
// supply live-computed context and must be passed per-call (typically
// a snapshot from [Loop.TagContextProviders]) to avoid data races.
// The ctx should carry any timeout (e.g., the 2-second HA deadline).
// Returns empty string when no content is produced.
func (a *TagContextAssembler) Build(ctx context.Context, activeTags map[string]bool, providers map[string]TagContextProvider) string {
	if a == nil {
		return ""
	}

	seen := make(map[string]bool)
	var buf strings.Builder

	// Phase 1: Tagged KB articles (re-read each turn for freshness).
	// KB articles declare their tag affinity via frontmatter
	// (tags: [forge, ha]) and auto-load when matching tags are active.
	for _, article := range a.kbArticles {
		if !articleMatchesTags(article, activeTags) {
			continue
		}
		if seen[article.Path] {
			continue
		}
		seen[article.Path] = true
		data, err := os.ReadFile(article.Path)
		if err != nil {
			a.logger.Warn("failed to read tagged KB article",
				"path", article.Path, "error", err)
			continue
		}
		// Strip frontmatter before injection — the model doesn't need
		// the YAML metadata, just the knowledge content.
		_, content := talents.ParseFrontmatterMetadata(string(data))
		data = homeassistant.ResolveInject(ctx, []byte(content), a.haInject, a.logger)
		a.appendContent(&buf, data)
		if buf.Len() >= maxTagContextBytes {
			a.logger.Warn("tag context aggregate limit reached",
				"source", "kb_article", "limit_bytes", maxTagContextBytes)
			return buf.String()
		}
	}

	// Phase 2: Live providers.
	for tag, active := range activeTags {
		if !active {
			continue
		}
		p, ok := providers[tag]
		if !ok {
			continue
		}
		content, err := p.TagContext(ctx)
		if err != nil {
			a.logger.Warn("tag context provider failed",
				"tag", tag, "error", err)
			continue
		}
		if content == "" {
			continue
		}
		a.appendContent(&buf, []byte(content))
		if buf.Len() >= maxTagContextBytes {
			a.logger.Warn("tag context aggregate limit reached",
				"tag", tag, "source", "provider", "limit_bytes", maxTagContextBytes)
			return buf.String()
		}
	}

	return buf.String()
}

const truncationMarker = "\n\n[tag context truncated — exceeded aggregate 64 KB limit]"

// appendContent adds data to buf with a separator, respecting the
// aggregate size limit. Truncates data if it would exceed the cap,
// reserving space for the truncation marker so the buffer never
// exceeds maxTagContextBytes.
func (a *TagContextAssembler) appendContent(buf *strings.Builder, data []byte) {
	if len(data) == 0 {
		return
	}
	if buf.Len() > 0 {
		buf.WriteString("\n\n---\n\n")
	}
	remaining := maxTagContextBytes - buf.Len()
	if remaining <= 0 {
		return
	}
	if len(data) > remaining {
		// Reserve space for the truncation marker.
		dataCap := remaining - len(truncationMarker)
		if dataCap > 0 {
			buf.Write(data[:dataCap])
		}
		buf.WriteString(truncationMarker)
	} else {
		buf.Write(data)
	}
}

// KBArticleTags returns the tag→article count index, useful for
// enriching the capability manifest with KB article counts.
func (a *TagContextAssembler) KBArticleTags() map[string]int {
	if a == nil {
		return nil
	}
	counts := make(map[string]int)
	for _, article := range a.kbArticles {
		for _, tag := range article.Tags {
			counts[tag]++
		}
	}
	return counts
}

// KBMenuHints returns one root-menu hint per tag, sourced from tagged
// KB decision-tree documents. The first teaser encountered for a tag
// wins, with deterministic ordering provided by scanKBArticles.
func (a *TagContextAssembler) KBMenuHints() map[string]KBMenuHint {
	if a == nil {
		return nil
	}
	hints := make(map[string]KBMenuHint)
	for _, article := range a.kbArticles {
		if article.Kind != "decision_tree" {
			continue
		}
		if strings.TrimSpace(article.Teaser) == "" && len(article.NextTags) == 0 {
			continue
		}
		for _, tag := range article.Tags {
			if _, exists := hints[tag]; exists {
				continue
			}
			hints[tag] = KBMenuHint{
				Teaser:   strings.TrimSpace(article.Teaser),
				NextTags: append([]string(nil), article.NextTags...),
			}
		}
	}
	return hints
}

// scanKBArticles walks the KB directory for .md files with tags:
// frontmatter. Only top-level and one-level-deep files are scanned
// (matching typical KB layouts like kb:dossiers/foo.md).
func scanKBArticles(dir string) ([]kbArticle, error) {
	var articles []kbArticle

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			// Allow root and one level of subdirectories.
			rel, _ := filepath.Rel(dir, path)
			if rel != "." && strings.Count(rel, string(filepath.Separator)) > 0 {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable files
		}

		meta, _ := talents.ParseFrontmatterMetadata(string(data))
		if len(meta.Tags) == 0 {
			return nil // untagged KB articles are not auto-loaded
		}

		articles = append(articles, kbArticle{
			Path:     path,
			Tags:     meta.Tags,
			Kind:     strings.TrimSpace(meta.Kind),
			Teaser:   strings.TrimSpace(meta.Teaser),
			NextTags: append([]string(nil), meta.NextTags...),
			Name:     strings.TrimSuffix(d.Name(), ".md"),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Sort for deterministic ordering.
	sort.Slice(articles, func(i, j int) bool {
		if articles[i].Kind == "decision_tree" && articles[j].Kind != "decision_tree" {
			return true
		}
		if articles[i].Kind != "decision_tree" && articles[j].Kind == "decision_tree" {
			return false
		}
		return articles[i].Path < articles[j].Path
	})

	return articles, nil
}

// articleMatchesTags returns true if any of the article's tags are in
// the active set.
func articleMatchesTags(a kbArticle, activeTags map[string]bool) bool {
	for _, tag := range a.Tags {
		if activeTags[tag] {
			return true
		}
	}
	return false
}
