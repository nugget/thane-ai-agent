package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/talents"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// TagContextAssembler builds the Capability Context section from
// three sources, walked in one ordered pass per call:
//
//  1. Tagged KB articles — markdown files in the knowledge base
//     directory with `tags:` (any-of) and/or `tags_all:` (all-of)
//     frontmatter, same pattern as talents. Filtered by ActiveTags.
//  2. Tagged live providers — [TagContextProvider] implementations
//     registered against a specific capability tag via
//     [Loop.RegisterTagContextProvider]. Filtered by ActiveTags.
//  3. Always-on providers — [TagContextProvider] implementations
//     registered via [Loop.RegisterAlwaysContextProvider]. Gated by
//     ContextRequest.IncludeAlways: main loop runs include them,
//     delegate runs do not.
//
// All three sources share one 64 KB aggregate cap, one truncation
// marker, and one unified [Build] method — there is no parallel
// always-bucket walker. Tagged vs always is encoded as where each
// source registered, not as a separate code path. KB articles and
// explicit context refs flow through the optional managed-root
// signature verifier. Providers that read disk-managed material are
// responsible for applying their own verification before returning
// model-facing content.
//
// Both the main agent loop and delegate executor share a single
// assembler. The assembler is safe for concurrent use after
// construction.
type TagContextAssembler struct {
	capTags  map[string]config.CapabilityTagConfig
	kbDir    string
	resolver *paths.Resolver
	verifier interface {
		VerifyRef(ctx context.Context, ref string, consumer string) error
		VerifyPath(ctx context.Context, path string, consumer string) error
	}
	haInject homeassistant.StateFetcher // nil-safe — delegates pass nil
	logger   *slog.Logger

	mu              sync.Mutex
	tagProviders    map[string]TagContextProvider
	alwaysProviders []TagContextProvider
}

// kbArticle is a knowledge base file with tag affinity parsed from
// frontmatter. Reuses the talent frontmatter format: `tags: [a, b]`
// activates on any (OR), `tags_all: [a, b]` requires all (AND).
// When both are set, the article injects only when the OR check on
// Tags AND the AND check on TagsAll both pass — useful for articles
// that should fire for several entry-point tags but only when paired
// with a runtime-asserted gate (e.g., owner + signal).
type kbArticle struct {
	Path     string   // absolute file path
	Tags     []string // any-of activation set, from frontmatter `tags:`
	TagsAll  []string // all-of activation set, from frontmatter `tags_all:`
	Kind     string   // frontmatter kind: entry_point or empty/article
	Teaser   string   // short menu teaser for entry-point docs
	NextTags []string // suggested next tags from an entry point
	Name     string   // filename without .md
}

// KBMenuHint captures entry-point metadata that can be surfaced in
// the capability menu before a tag is activated.
type KBMenuHint struct {
	Teaser   string
	NextTags []string
}

// TagContextAssemblerConfig holds the construction parameters for a
// TagContextAssembler.
type TagContextAssemblerConfig struct {
	CapTags  map[string]config.CapabilityTagConfig
	KBDir    string          // resolved kb: directory; empty skips scanning
	Resolver *paths.Resolver // managed document root resolver; nil falls back to KBDir for kb: refs
	// Verifier is an optional managed-root verifier for context refs and
	// tagged articles.
	Verifier interface {
		VerifyRef(ctx context.Context, ref string, consumer string) error
		VerifyPath(ctx context.Context, path string, consumer string) error
	}
	HAInject homeassistant.StateFetcher // nil-safe
	Logger   *slog.Logger
}

// NewTagContextAssembler creates an assembler. The KB directory is
// scanned lazily — the article list (paths and tag affinity) is
// re-read from disk on every consumer call (Build, KBArticleTags,
// KBMenuHints), so frontmatter edits, additions, and deletions
// propagate without a process restart. Scans are cheap (a directory
// walk plus a frontmatter parse per .md file) and run once per
// consumer call, not once per article.
func NewTagContextAssembler(cfg TagContextAssemblerConfig) *TagContextAssembler {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &TagContextAssembler{
		capTags:  cfg.CapTags,
		kbDir:    cfg.KBDir,
		resolver: cfg.Resolver,
		verifier: cfg.Verifier,
		haInject: cfg.HAInject,
		logger:   cfg.Logger,
	}
}

// loadKBArticles returns the current list of tag-aware KB articles by
// scanning kbDir fresh. Scan errors are logged and the call returns an
// empty slice, matching the prior constructor behavior. Callers that
// need a stable snapshot within a single operation (e.g., Build) call
// this once and iterate the result locally.
func (a *TagContextAssembler) loadKBArticles() []kbArticle {
	if a.kbDir == "" {
		return nil
	}
	articles, err := scanKBArticles(a.kbDir)
	if err != nil {
		a.logger.Warn("failed to scan KB directory for tagged articles",
			"dir", a.kbDir, "error", err)
		return nil
	}
	return articles
}

// RegisterTaggedProvider associates a provider with one capability
// tag. The provider fires when that tag is active in a Build call.
// Idempotent on tag — last registration wins.
func (a *TagContextAssembler) RegisterTaggedProvider(tag string, p TagContextProvider) {
	if a == nil || p == nil {
		return
	}
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tagProviders == nil {
		a.tagProviders = make(map[string]TagContextProvider)
	}
	a.tagProviders[tag] = p
}

// RegisterAlwaysProvider adds a provider to the always-on bucket.
// Always-providers fire on every main-loop run but are suppressed for
// delegate runs that pass IncludeAlways=false. Order is preserved
// across registrations.
func (a *TagContextAssembler) RegisterAlwaysProvider(p TagContextProvider) {
	if a == nil || p == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.alwaysProviders = append(a.alwaysProviders, p)
}

// TaggedProviders returns a snapshot of the registered tag→provider
// map. Used by callers that need to inspect what's wired (e.g., the
// capability surface builder).
func (a *TagContextAssembler) TaggedProviders() map[string]TagContextProvider {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.tagProviders) == 0 {
		return nil
	}
	out := make(map[string]TagContextProvider, len(a.tagProviders))
	for k, v := range a.tagProviders {
		out[k] = v
	}
	return out
}

// Build assembles tag context for the request. The single internal
// pipeline walks three sources in order — KB articles, tagged
// providers, always-on providers — sharing one 64 KB aggregate cap
// and one truncation marker. Always-on providers are gated by
// req.IncludeAlways; main loop runs include them, delegate runs do
// not. Returns empty string when no source produces content.
func (a *TagContextAssembler) Build(ctx context.Context, req agentctx.ContextRequest) string {
	if a == nil {
		return ""
	}

	a.mu.Lock()
	tagProviders := make(map[string]TagContextProvider, len(a.tagProviders))
	for k, v := range a.tagProviders {
		tagProviders[k] = v
	}
	alwaysProviders := append([]TagContextProvider(nil), a.alwaysProviders...)
	a.mu.Unlock()

	seen := make(map[string]bool)
	var buf strings.Builder

	// Source 1: Tagged KB articles. Re-scanned and re-read each turn
	// so frontmatter edits, additions, and deletions propagate
	// without a restart. Articles declare tag affinity via
	// frontmatter: `tags:` for any-of activation, `tags_all:` for
	// all-of activation. Both compose; see [articleMatchesTags].
	articles := a.loadKBArticles()
	for _, article := range articles {
		if !articleMatchesTags(article, req.ActiveTags) {
			continue
		}
		if seen[article.Path] {
			continue
		}
		seen[article.Path] = true
		if err := a.verifyPath(ctx, article.Path, "tagged_kb_article"); err != nil {
			a.logger.Warn("tagged KB article blocked by document root signature policy",
				"path", article.Path, "error", err)
			continue
		}
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

	// Source 2: Tagged live providers, filtered by ActiveTags.
	for tag, active := range req.ActiveTags {
		if !active {
			continue
		}
		p, ok := tagProviders[tag]
		if !ok {
			continue
		}
		content, err := p.TagContext(ctx, req)
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
				"tag", tag, "source", "tagged_provider", "limit_bytes", maxTagContextBytes)
			return buf.String()
		}
	}

	// Source 3: Always-on providers, gated by IncludeAlways. Delegate
	// runs pass IncludeAlways=false to skip ambient context (presence,
	// episodic memory, working memory, notification history, etc.)
	// that the bounded child task does not need.
	if req.IncludeAlways {
		for _, p := range alwaysProviders {
			content, err := p.TagContext(ctx, req)
			if err != nil {
				a.logger.Warn("always context provider failed", "error", err)
				continue
			}
			if content == "" {
				continue
			}
			a.appendContent(&buf, []byte(content))
			if buf.Len() >= maxTagContextBytes {
				a.logger.Warn("tag context aggregate limit reached",
					"source", "always_provider", "limit_bytes", maxTagContextBytes)
				return buf.String()
			}
		}
	}

	return buf.String()
}

// BuildRefs assembles exact managed document refs for origin-derived
// context. Refs are read fresh each turn, frontmatter is stripped, and
// each document is labeled by its semantic ref.
func (a *TagContextAssembler) BuildRefs(ctx context.Context, refs []string) string {
	if a == nil || len(refs) == 0 {
		return ""
	}

	seen := make(map[string]bool, len(refs))
	var buf strings.Builder
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true

		path, ok := a.resolveContextRef(ref)
		if !ok {
			continue
		}
		if err := a.verifyRef(ctx, ref, "session_origin_context_ref"); err != nil {
			a.logger.Warn("session origin context ref blocked by document root signature policy",
				"ref", ref, "path", path, "error", err)
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			a.logger.Warn("failed to read session origin context ref",
				"ref", ref, "path", path, "error", err)
			continue
		}
		_, content := talents.ParseFrontmatterMetadata(string(data))
		content = strings.TrimSpace(content)
		if content == "" {
			continue
		}
		resolved := homeassistant.ResolveInject(ctx, []byte(content), a.haInject, a.logger)
		var entry strings.Builder
		entry.WriteString("#### ")
		entry.WriteString(ref)
		entry.WriteString("\n\n")
		entry.Write(resolved)
		a.appendContent(&buf, []byte(entry.String()))
		if buf.Len() >= maxTagContextBytes {
			a.logger.Warn("session origin context aggregate limit reached",
				"ref", ref, "limit_bytes", maxTagContextBytes)
			return buf.String()
		}
	}
	return buf.String()
}

func (a *TagContextAssembler) verifyRef(ctx context.Context, ref string, consumer string) error {
	if a == nil || a.verifier == nil {
		return nil
	}
	return a.verifier.VerifyRef(ctx, ref, consumer)
}

func (a *TagContextAssembler) verifyPath(ctx context.Context, path string, consumer string) error {
	if a == nil || a.verifier == nil {
		return nil
	}
	return a.verifier.VerifyPath(ctx, path, consumer)
}

func (a *TagContextAssembler) resolveContextRef(ref string) (string, bool) {
	prefix, _, ok := strings.Cut(ref, ":")
	if !ok || strings.TrimSpace(prefix) == "" {
		a.logger.Warn("session origin context ref is not semantic", "ref", ref)
		return "", false
	}
	rootRef := prefix + ":"
	if a.resolver != nil && a.resolver.HasPrefix(ref) {
		path, err := a.resolver.Resolve(ref)
		if err != nil {
			a.logger.Warn("failed to resolve session origin context ref", "ref", ref, "error", err)
			return "", false
		}
		root, err := a.resolver.Resolve(rootRef)
		if err != nil {
			a.logger.Warn("failed to resolve session origin context root", "ref", ref, "root", rootRef, "error", err)
			return "", false
		}
		return safeManagedRefPath(root, path)
	}
	if rootRef == "kb:" && a.kbDir != "" {
		path := filepath.Join(a.kbDir, strings.TrimPrefix(ref, "kb:"))
		return safeManagedRefPath(a.kbDir, path)
	}
	a.logger.Warn("unsupported session origin context ref", "ref", ref)
	return "", false
}

func safeManagedRefPath(root, path string) (string, bool) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	pathResolved, err := filepath.EvalSymlinks(pathAbs)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(rootResolved, pathResolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return pathResolved, true
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
// enriching the capability manifest with KB article counts. Both
// `tags:` (any-of) and `tags_all:` (all-of) memberships count — a
// `tags_all`-only article would otherwise be invisible to the menu
// surface despite gating real content. Tags appearing in both lists
// of the same article count once.
func (a *TagContextAssembler) KBArticleTags() map[string]int {
	if a == nil {
		return nil
	}
	counts := make(map[string]int)
	for _, article := range a.loadKBArticles() {
		seen := make(map[string]bool, len(article.Tags)+len(article.TagsAll))
		for _, tag := range article.Tags {
			if !seen[tag] {
				seen[tag] = true
				counts[tag]++
			}
		}
		for _, tag := range article.TagsAll {
			if !seen[tag] {
				seen[tag] = true
				counts[tag]++
			}
		}
	}
	return counts
}

// KBMenuHints returns one root-menu hint per tag, sourced from tagged
// KB entry-point documents. The first teaser encountered for a tag
// wins, with deterministic ordering provided by scanKBArticles.
func (a *TagContextAssembler) KBMenuHints() map[string]KBMenuHint {
	if a == nil {
		return nil
	}
	hints := make(map[string]KBMenuHint)
	for _, article := range a.loadKBArticles() {
		if !isEntryPointKind(article.Kind) {
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

func isEntryPointKind(kind string) bool {
	return strings.TrimSpace(kind) == "entry_point"
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
		if len(meta.Tags) == 0 && len(meta.TagsAll) == 0 {
			return nil // untagged KB articles are not auto-loaded
		}

		articles = append(articles, kbArticle{
			Path:     path,
			Tags:     meta.Tags,
			TagsAll:  append([]string(nil), meta.TagsAll...),
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
		if isEntryPointKind(articles[i].Kind) && !isEntryPointKind(articles[j].Kind) {
			return true
		}
		if !isEntryPointKind(articles[i].Kind) && isEntryPointKind(articles[j].Kind) {
			return false
		}
		return articles[i].Path < articles[j].Path
	})

	return articles, nil
}

// articleMatchesTags reports whether an article should inject given
// the currently active tag set. Semantics:
//
//   - When TagsAll is non-empty, every tag in TagsAll must be active.
//     This is the AND gate for narrowly-scoped articles.
//   - When Tags is non-empty, at least one tag must be active. This
//     is the OR activation set.
//   - When both are set, the article injects only when both checks
//     pass — `(any of Tags) AND (all of TagsAll)`.
//   - When only TagsAll is set (no Tags), the AND check alone gates
//     the article.
func articleMatchesTags(a kbArticle, activeTags map[string]bool) bool {
	if len(a.TagsAll) > 0 {
		for _, tag := range a.TagsAll {
			if !activeTags[tag] {
				return false
			}
		}
		if len(a.Tags) == 0 {
			return true
		}
	}
	for _, tag := range a.Tags {
		if activeTags[tag] {
			return true
		}
	}
	return false
}
