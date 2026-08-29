package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/model/talents"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
)

// TagContextAssembler builds typed prompt context sections from three
// sources, walked in one ordered pass per call:
//
//  1. Tagged KB articles — markdown files in the knowledge base
//     directory with `tags:` (any-of) and/or `tags_all:` (all-of)
//     frontmatter, same pattern as talents. Filtered by ActiveTags.
//  2. Tagged live providers — [TagContextProvider] implementations
//     registered against a specific capability tag via
//     [Loop.RegisterTagContextProvider]. Filtered by ActiveTags.
//  3. Gated providers — [TagContextProvider] implementations that
//     render every turn their gate admits, held in one list in
//     registration order. Always-on registrations
//     ([Loop.RegisterAlwaysContextProvider]) carry the ambient
//     experiential context and render under
//     ContextRequest.IncludeAlways; loop-scoped registrations
//     ([Loop.RegisterLoopScopedContextProvider]) carry the running
//     loop's own subscriptions and self view and render under
//     ContextRequest.IncludeLoopScoped. The two gates are independent:
//     full-mode loop turns set both, task-mode turns set only
//     loop-scoped, delegate runs set neither.
//
// Providers may instead implement [ContextAdvertiser]. Those providers offer
// cheap request-relative candidates during the same walk; one deterministic
// discriminator ranks, filters, and limits all offers before materializing
// selected projections. Legacy eager providers keep rendering directly while
// they migrate.
//
// Each rendered bucket has its own 64 KB cap and truncation marker.
// A provider's class is encoded as how it registered, not as a
// separate code path — one interleaved list preserves registration
// order across classes, so a prompt's within-bucket ordering never
// depends on which gates happen to be open. KB articles and explicit
// context refs flow through the optional managed-root signature
// verifier. Providers that read disk-managed material are responsible
// for applying their own verification before returning model-facing
// content.
//
// Both the main agent loop and delegate executor share a single
// assembler. The assembler is safe for concurrent use after
// construction.
type TagContextAssembler struct {
	capTags map[string]config.CapabilityTagConfig
	kbDir   string
	// injectRoots is the declared injection-eligible root set. Nil means
	// no policy was declared and the legacy single-kbDir scan applies.
	injectRoots map[string]InjectRoot
	resolver    *paths.Resolver
	verifier    interface {
		VerifyRef(ctx context.Context, ref string, consumer string) error
		VerifyPath(ctx context.Context, path string, consumer string) error
	}
	haInject homeassistant.StateFetcher // nil-safe — delegates pass nil
	logger   *slog.Logger
	// loc is the zone temporal templates expand against; see
	// [TagContextAssemblerConfig.Timezone] for why it matters.
	loc *time.Location
	// nowFunc returns the current time. Tests override it to pin
	// template expansion to a fixed instant.
	nowFunc func() time.Time

	mu           sync.Mutex
	tagProviders map[string]TagContextProvider
	// gatedProviders holds always-on and loop-scoped providers in one
	// list, in registration order. One list rather than two because
	// order is prompt order: within a bucket, providers render in the
	// sequence they registered, whichever class they belong to.
	gatedProviders []gatedContextProvider
}

// gatedContextProvider pairs a provider with the request gate that
// admits it: loop-scoped providers render under IncludeLoopScoped,
// everything else under IncludeAlways.
type gatedContextProvider struct {
	provider   TagContextProvider
	loopScoped bool
}

// TagContextBucketer lets a context provider choose the prompt bucket
// that should contain its output. Providers that do not implement it
// are assigned by registration path: tagged providers default to
// Tagged Guidance and always-on providers default to Continuity
// Context.
type TagContextBucketer interface {
	TagContextBucket() agentctx.ContextBucket
}

// kbArticle is a knowledge base file with tag affinity parsed from
// frontmatter. Reuses the talent frontmatter format: `tags: [a, b]`
// activates on any (OR), `tags_all: [a, b]` requires all (AND).
// When both are set, the article injects only when the OR check on
// Tags AND the AND check on TagsAll both pass — useful for articles
// that should fire for several trailhead tags but only when paired
// with a runtime-asserted gate (e.g., owner + signal).
type kbArticle struct {
	Path     string   // absolute file path
	Tags     []string // any-of activation set, from frontmatter `tags:`
	TagsAll  []string // all-of activation set, from frontmatter `tags_all:`
	Kind     string   // canonical frontmatter kind ([talents.KindTrailhead] or empty/article)
	Teaser   string   // short menu teaser for trailhead docs
	NextTags []string // suggested next tags from a trailhead
	Name     string   // filename without .md
}

// KBMenuHint captures trailhead metadata that can be surfaced in
// the capability menu before a tag is activated.
type KBMenuHint struct {
	Teaser   string
	NextTags []string
}

// TagContextAssemblerConfig holds the construction parameters for a
// TagContextAssembler.
type TagContextAssemblerConfig struct {
	CapTags map[string]config.CapabilityTagConfig
	KBDir   string // resolved kb: directory; empty skips scanning
	// InjectRoots are the roots whose tagged documents may inject, as
	// resolved directories keyed by root name. When non-nil it replaces
	// KBDir entirely: injection eligibility is declared per root in
	// config rather than fixed to the kb root, so a corpus reaches the
	// prompt only because policy says it may.
	InjectRoots map[string]InjectRoot
	Resolver    *paths.Resolver // managed document root resolver; nil falls back to KBDir for kb: refs
	// Verifier is an optional managed-root verifier for context refs and
	// tagged articles.
	Verifier interface {
		VerifyRef(ctx context.Context, ref string, consumer string) error
		VerifyPath(ctx context.Context, path string, consumer string) error
	}
	HAInject homeassistant.StateFetcher // nil-safe
	Logger   *slog.Logger
	// Timezone is the household IANA timezone (e.g. "America/Chicago")
	// that temporal templates in injected documents expand against.
	// [promptfmt.FormatDayDelta] compares calendar days, so the zone
	// decides which day "today" is — a household evening must not read
	// as "tomorrow" because the process clock runs in UTC. Empty or
	// unloadable falls back to the process-local zone.
	Timezone string
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
	// Config validation already rejects a bad household timezone, but
	// this constructor is also reached directly (tests, the loop's
	// pending-provider fallback), so degrade to the process-local zone
	// with a warning rather than failing construction over a
	// presentation concern.
	loc := time.Local
	if cfg.Timezone != "" {
		if parsed, err := time.LoadLocation(cfg.Timezone); err == nil {
			loc = parsed
		} else {
			cfg.Logger.Warn("invalid timezone for temporal template expansion; using process-local zone",
				"timezone", cfg.Timezone, "error", err)
		}
	}
	return &TagContextAssembler{
		capTags:     cfg.CapTags,
		kbDir:       cfg.KBDir,
		injectRoots: cfg.InjectRoots,
		resolver:    cfg.Resolver,
		verifier:    cfg.Verifier,
		haInject:    cfg.HAInject,
		logger:      cfg.Logger,
		loc:         loc,
		nowFunc:     time.Now,
	}
}

// templateNow is the instant temporal templates in injected documents
// expand against: the current time in the household zone when one was
// configured, the process-local zone otherwise. Day-word rendering
// compares calendar days, so the zone decides which day "today" is.
func (a *TagContextAssembler) templateNow() time.Time {
	now := time.Now()
	if a.nowFunc != nil {
		now = a.nowFunc()
	}
	if a.loc != nil {
		now = now.In(a.loc)
	}
	return now
}

// InjectRoot is one injection-eligible document root: the directory to
// scan, and the optional capability tag that must be active before any
// of its documents are considered at all.
type InjectRoot struct {
	// Untagged is what a tagless document means here: [documents.RootUntaggedIgnore]
	// skips it, [documents.RootUntaggedRefuse] surfaces it as a fault.
	Untagged    string
	Dir         string
	RequiresTag string
}

// CheckUnclassifiedDocuments reports the first root that refuses tagless
// documents and contains one, so startup can decline rather than discover it
// a turn at a time.
//
// The per-turn scan surfaces the same fault, but only as a log line on a root
// whose articles were then dropped — which is guidance going missing with a
// warning, the shape this policy exists to end.
func CheckUnclassifiedDocuments(roots map[string]InjectRoot) error {
	names := make([]string, 0, len(roots))
	for name := range roots {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		root := roots[name]
		if root.Dir == "" || root.Untagged != documents.RootUntaggedRefuse {
			continue
		}
		if _, err := scanKBArticles(root.Dir, root.Untagged); err != nil {
			return fmt.Errorf("roots.%s: %w", name, err)
		}
	}
	return nil
}

// loadKBArticles returns the current list of tag-aware articles from
// every injection-eligible root, scanned fresh. Scan errors are logged
// per root and skipped rather than failing the whole assembly, so one
// unreadable corpus cannot silence the others. Callers that need a
// stable snapshot within a single operation (e.g., Build) call this once
// and iterate the result locally.
//
// When no injection policy is declared, this falls back to the single
// legacy kbDir scan so an existing config keeps assembling context
// exactly as it did before roots could declare eligibility.
func (a *TagContextAssembler) loadKBArticles() []kbArticle {
	if a.injectRoots == nil {
		if a.kbDir == "" {
			return nil
		}
		articles, err := scanKBArticles(a.kbDir, documents.RootUntaggedIgnore)
		if err != nil {
			a.logger.Warn("failed to scan KB directory for tagged articles",
				"dir", a.kbDir, "error", err)
			return nil
		}
		return articles
	}

	names := make([]string, 0, len(a.injectRoots))
	for name := range a.injectRoots {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic order across turns

	var all []kbArticle
	for _, name := range names {
		root := a.injectRoots[name]
		if root.Dir == "" {
			continue
		}
		articles, err := scanKBArticles(root.Dir, root.Untagged)
		if err != nil {
			a.logger.Warn("failed to scan injection-eligible root for tagged articles",
				"root", name, "dir", root.Dir, "error", err)
			continue
		}
		if root.RequiresTag != "" {
			// A root-level gate is coarser than per-document tags: the
			// whole corpus stays invisible until the capability is
			// active, which is expressed by stamping the requirement
			// onto each article's all-of set.
			for i := range articles {
				articles[i].TagsAll = append(articles[i].TagsAll, root.RequiresTag)
			}
		}
		all = append(all, articles...)
	}
	return all
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
	a.gatedProviders = append(a.gatedProviders, gatedContextProvider{provider: p})
}

// RegisterLoopScopedProvider adds a provider to the loop-scoped class:
// context that belongs to the running loop itself — its declared entity
// subscriptions, its own self view — rather than to the ambient
// experience the always-on class carries. The split exists because the
// two are gated differently (ContextRequest.IncludeLoopScoped vs
// IncludeAlways): a task-mode worker keeps its own operational context
// while shedding the ambient self. Registration order is preserved
// across both classes — it is prompt order within a bucket.
func (a *TagContextAssembler) RegisterLoopScopedProvider(p TagContextProvider) {
	if a == nil || p == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gatedProviders = append(a.gatedProviders, gatedContextProvider{provider: p, loopScoped: true})
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

// Build assembles tag context for the request as one compatibility string.
// Production prompt assembly uses [TagContextAssembler.BuildSections] so typed
// buckets remain visible to the model and request forensics, but tests and
// older callers still use this flattened view.
func (a *TagContextAssembler) Build(ctx context.Context, req agentctx.ContextRequest) string {
	sections := a.BuildSections(ctx, req)
	if len(sections) == 0 {
		return ""
	}
	var buf strings.Builder
	for _, section := range sections {
		appendContextContent(&buf, []byte(section.Content), maxTagContextBytes, contextBucketTruncationMarker(section.Bucket))
	}
	return buf.String()
}

// BuildSections assembles typed context sections for the request. The
// single internal pipeline walks three sources in order — KB articles,
// tagged providers, then the gated providers (always-on and
// loop-scoped interleaved in registration order, each admitted by its
// own gate: req.IncludeAlways for ambient context, req.IncludeLoopScoped
// for the running loop's own subscriptions and self view). Returns nil
// when no source produces content.
// slowContextSourceThreshold flags a context source consuming an
// outsized share of the assembler's shared budget. The 2026-08-12
// production incident was undiagnosable from logs precisely because a
// slow-but-successful source is silent: the shared budget died
// upstream and the failure surfaced downstream under the wrong name.
const slowContextSourceThreshold = 250 * time.Millisecond

// warnSlowContextSource emits the budget-accounting WARN for a context
// source that ran long. detail is a tag name or provider type; empty
// is fine for block-level sources.
func warnSlowContextSource(logger *slog.Logger, source, detail string, start time.Time) {
	elapsed := time.Since(start)
	if elapsed <= slowContextSourceThreshold {
		return
	}
	args := []any{"source", source, "elapsed", elapsed.Round(time.Millisecond).String()}
	if detail != "" {
		args = append(args, "detail", detail)
	}
	logger.Warn("context source ran long against the shared assembly budget", args...)
}

func (a *TagContextAssembler) BuildSections(ctx context.Context, req agentctx.ContextRequest) []agentctx.ContextSection {
	if a == nil {
		return nil
	}

	a.mu.Lock()
	tagProviders := make(map[string]TagContextProvider, len(a.tagProviders))
	for k, v := range a.tagProviders {
		tagProviders[k] = v
	}
	gatedProviders := append([]gatedContextProvider(nil), a.gatedProviders...)
	a.mu.Unlock()

	seen := make(map[string]bool)
	acc := newContextAccumulator()
	var advertisements []contextAdvertisementCandidate

	// Source 1: Tagged KB articles. Re-scanned and re-read each turn
	// so frontmatter edits, additions, and deletions propagate
	// without a restart. Articles declare tag affinity via
	// frontmatter: `tags:` for any-of activation, `tags_all:` for
	// all-of activation. Both compose; see [articleMatchesTags].
	kbStart := time.Now()
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
		// Temporal templates expand before ha-inject resolution so
		// only the curated prose is expanded, never entity state
		// fetched a moment ago. This is a reader surface: the model
		// sees "+20d" here while doc_read, the publish tools, and git
		// keep the raw {{delta:...}} so the author round-trip stays
		// byte-exact.
		content = promptfmt.ExpandTemporalTemplates(content, a.templateNow())
		data = homeassistant.ResolveInject(ctx, []byte(content), a.haInject, a.logger)
		bucket := agentctx.ContextBucketTaggedGuidance
		if acc.append(bucket, data) {
			a.logger.Warn("tag context bucket limit reached",
				"bucket", string(bucket), "bucket_title", bucket.Title(),
				"source", "kb_article", "limit_bytes", maxTagContextBytes)
		}
	}

	warnSlowContextSource(a.logger, "tagged_kb_articles", "", kbStart)

	// Source 2: Tagged live providers, filtered by ActiveTags.
	for _, tag := range sortedActiveTags(req.ActiveTags) {
		p, ok := tagProviders[tag]
		if !ok {
			continue
		}
		pStart := time.Now()
		content, advertised, err := a.contextFromProvider(ctx, req, p, &advertisements)
		warnSlowContextSource(a.logger, "tagged_provider", tag, pStart)
		if err != nil {
			a.logger.Warn("tag context provider failed",
				"tag", tag, "error", err)
			continue
		}
		if advertised {
			continue
		}
		if content == "" {
			continue
		}
		bucket := providerContextBucket(p, agentctx.ContextBucketTaggedGuidance)
		if acc.append(bucket, []byte(content)) {
			a.logger.Warn("tag context bucket limit reached",
				"bucket", string(bucket), "bucket_title", bucket.Title(),
				"tag", tag, "source", "tagged_provider", "limit_bytes", maxTagContextBytes)
		}
	}

	// Source 3: Gated providers — always-on and loop-scoped — walked
	// as one list in registration order, each entry admitted by its
	// own gate. Always-on entries carry ambient experiential context
	// (presence, episodic memory, notification history, etc.) and
	// render under IncludeAlways: full-mode loop turns only.
	// Loop-scoped entries carry the running loop's own operational
	// context (declared subscriptions, the "This loop" self view) and
	// render under IncludeLoopScoped: every loop turn regardless of
	// prompt mode — a task-mode worker sheds the ambient self, not its
	// eyes. Delegate runs open neither gate. The single interleaved
	// walk keeps full-mode prompts byte-identical to the pre-split
	// ordering and keeps bucket-cap priority a property of
	// registration order, not of class.
	for _, gp := range gatedProviders {
		if gp.loopScoped {
			if !req.IncludeLoopScoped {
				continue
			}
		} else if !req.IncludeAlways {
			continue
		}
		defaultBucket, source := agentctx.ContextBucketContinuity, "always_provider"
		if gp.loopScoped {
			defaultBucket, source = agentctx.ContextBucketLiveState, "loop_scoped_provider"
		}
		pStart := time.Now()
		content, advertised, err := a.contextFromProvider(ctx, req, gp.provider, &advertisements)
		warnSlowContextSource(a.logger, source, fmt.Sprintf("%T", gp.provider), pStart)
		if err != nil {
			a.logger.Warn("gated context provider failed", "source", source, "error", err)
			continue
		}
		if advertised {
			continue
		}
		if content == "" {
			continue
		}
		bucket := providerContextBucket(gp.provider, defaultBucket)
		if acc.append(bucket, []byte(content)) {
			a.logger.Warn("tag context bucket limit reached",
				"bucket", string(bucket), "bucket_title", bucket.Title(),
				"source", source, "limit_bytes", maxTagContextBytes)
		}
	}

	// Advertised context wins the front of its bucket after the full offer
	// set has competed. That keeps final rank independent of registration
	// order and prevents a selected compact projection from being starved by
	// an earlier legacy provider that filled the outer bucket cap.
	for bucket, content := range a.materializeContextAdvertisements(ctx, req, advertisements) {
		if acc.prepend(bucket, []byte(content)) {
			a.logger.Warn("tag context bucket limit reached after advertised context selection",
				"bucket", string(bucket), "bucket_title", bucket.Title(),
				"source", "context_discriminator", "limit_bytes", maxTagContextBytes)
		}
	}

	return acc.sections()
}

// contextFromProvider takes the advertisement path when a provider supports
// it, otherwise preserving the legacy eager render contract. Invalid offers
// are isolated to the producer that emitted them; other candidates still
// compete normally.
func (a *TagContextAssembler) contextFromProvider(ctx context.Context, req agentctx.ContextRequest, provider TagContextProvider, candidates *[]contextAdvertisementCandidate) (string, bool, error) {
	advertiser, ok := provider.(ContextAdvertiser)
	if !ok {
		content, err := provider.TagContext(ctx, req)
		return content, false, err
	}
	advertisements, err := advertiser.ContextAdvertisements(ctx, req)
	if err != nil {
		return "", true, err
	}
	for _, advertisement := range advertisements {
		if err := advertisement.Validate(); err != nil {
			a.logger.Warn("context advertisement rejected",
				"provider", fmt.Sprintf("%T", provider),
				"source", advertisement.Source,
				"id", advertisement.ID,
				"error", err)
			continue
		}
		*candidates = append(*candidates, contextAdvertisementCandidate{
			advertiser:    advertiser,
			advertisement: advertisement,
		})
	}
	return "", true, nil
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
		if appendContextContent(&buf, []byte(entry.String()), maxTagContextBytes, contextRefTruncationMarker) {
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
		path, matchedRoot, matched := a.resolver.ResolveRoot(ref)
		if !matched {
			a.logger.Warn("failed to resolve session origin context ref", "ref", ref)
			return "", false
		}
		if matchedRoot.Kind == paths.RootKindRepository {
			a.logger.Warn("repository root cannot be used as managed session origin context", "ref", ref, "root", matchedRoot.Name)
			return "", false
		}
		return safeManagedRefPath(matchedRoot.Path, path)
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

const contextRefTruncationMarker = "\n\n[session origin context truncated: exceeded 64 KB aggregate limit]"

func contextBucketTruncationMarker(bucket agentctx.ContextBucket) string {
	bucket = bucket.OrDefault(agentctx.ContextBucketContinuity)
	return "\n\n[" + bucket.Title() + " truncated: exceeded 64 KB bucket limit]"
}

type contextAccumulator struct {
	order   []agentctx.ContextBucket
	buffers map[agentctx.ContextBucket]*strings.Builder
	capped  map[agentctx.ContextBucket]bool
}

func newContextAccumulator() *contextAccumulator {
	return &contextAccumulator{
		buffers: make(map[agentctx.ContextBucket]*strings.Builder),
		capped:  make(map[agentctx.ContextBucket]bool),
	}
}

func (a *contextAccumulator) append(bucket agentctx.ContextBucket, data []byte) bool {
	bucket = bucket.OrDefault(agentctx.ContextBucketContinuity)
	if len(data) == 0 || a.capped[bucket] {
		return false
	}
	buf := a.buffers[bucket]
	if buf == nil {
		buf = &strings.Builder{}
		a.buffers[bucket] = buf
		a.order = append(a.order, bucket)
	}
	if appendContextContent(buf, data, maxTagContextBytes, contextBucketTruncationMarker(bucket)) {
		a.capped[bucket] = true
		return true
	}
	return false
}

func (a *contextAccumulator) prepend(bucket agentctx.ContextBucket, data []byte) bool {
	bucket = bucket.OrDefault(agentctx.ContextBucketContinuity)
	if len(data) == 0 {
		return false
	}
	old := a.buffers[bucket]
	buf := &strings.Builder{}
	truncated := appendContextContent(buf, data, maxTagContextBytes, contextBucketTruncationMarker(bucket))
	if !truncated && old != nil && old.Len() > 0 {
		truncated = appendContextContent(buf, []byte(old.String()), maxTagContextBytes, contextBucketTruncationMarker(bucket))
	}
	if old == nil {
		a.order = append(a.order, bucket)
	}
	a.buffers[bucket] = buf
	a.capped[bucket] = truncated || a.capped[bucket]
	return truncated
}

func (a *contextAccumulator) sections() []agentctx.ContextSection {
	if len(a.order) == 0 {
		return nil
	}
	sections := make([]agentctx.ContextSection, 0, len(a.order))
	for _, bucket := range orderedContextBuckets(a.order) {
		buf := a.buffers[bucket]
		if buf == nil || buf.Len() == 0 {
			continue
		}
		sections = append(sections, agentctx.ContextSection{
			Bucket:  bucket,
			Content: buf.String(),
		})
	}
	return sections
}

func orderedContextBuckets(seen []agentctx.ContextBucket) []agentctx.ContextBucket {
	seenSet := make(map[agentctx.ContextBucket]bool, len(seen))
	for _, bucket := range seen {
		seenSet[bucket] = true
	}
	order := []agentctx.ContextBucket{
		agentctx.ContextBucketTaggedGuidance,
		agentctx.ContextBucketContinuity,
		agentctx.ContextBucketRelated,
		agentctx.ContextBucketLiveState,
	}
	out := make([]agentctx.ContextBucket, 0, len(seen))
	for _, bucket := range order {
		if seenSet[bucket] {
			out = append(out, bucket)
			delete(seenSet, bucket)
		}
	}
	for _, bucket := range seen {
		if seenSet[bucket] {
			out = append(out, bucket)
			delete(seenSet, bucket)
		}
	}
	return out
}

func providerContextBucket(p TagContextProvider, fallback agentctx.ContextBucket) agentctx.ContextBucket {
	if bucketer, ok := p.(TagContextBucketer); ok {
		if bucket := bucketer.TagContextBucket(); bucket.Valid() {
			return bucket
		}
	}
	if fallback.Valid() {
		return fallback
	}
	return agentctx.ContextBucketContinuity
}

func sortedActiveTags(activeTags map[string]bool) []string {
	if len(activeTags) == 0 {
		return nil
	}
	tags := make([]string, 0, len(activeTags))
	for tag, active := range activeTags {
		if active {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return tags
}

// appendContextContent adds data to buf with a separator, respecting
// limit. Truncates data if it would exceed the cap, reserving space for
// marker so the buffer never exceeds the limit. It reports whether any
// requested data was truncated or skipped because the cap was reached.
func appendContextContent(buf *strings.Builder, data []byte, limit int, marker string) bool {
	if len(data) == 0 {
		return false
	}
	if limit <= 0 || buf.Len() >= limit {
		return true
	}
	remaining := limit - buf.Len()
	separator := ""
	if buf.Len() > 0 {
		separator = "\n\n---\n\n"
	}
	if len(separator)+len(data) <= remaining {
		buf.WriteString(separator)
		buf.Write(data)
		return false
	}

	if marker == "" {
		if remaining <= len(separator) {
			return true
		}
		buf.WriteString(separator)
		writeUTF8Prefix(buf, data, remaining-len(separator))
		return true
	}

	if remaining < len(marker) {
		return true
	}
	dataCap := remaining - len(marker)
	if dataCap >= len(separator) {
		buf.WriteString(separator)
		writeUTF8Prefix(buf, data, dataCap-len(separator))
	}
	buf.WriteString(marker)
	return true
}

func writeUTF8Prefix(buf *strings.Builder, data []byte, limit int) {
	if limit <= 0 {
		return
	}
	if limit >= len(data) {
		buf.Write(data)
		return
	}
	n := limit
	for n > 0 && !utf8.Valid(data[:n]) {
		n--
	}
	if n > 0 {
		buf.Write(data[:n])
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
// KB trailhead documents. The first teaser encountered for a tag
// wins, with deterministic ordering provided by scanKBArticles.
func (a *TagContextAssembler) KBMenuHints() map[string]KBMenuHint {
	if a == nil {
		return nil
	}
	hints := make(map[string]KBMenuHint)
	for _, article := range a.loadKBArticles() {
		if !isTrailheadKind(article.Kind) {
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

func isTrailheadKind(kind string) bool {
	return strings.TrimSpace(kind) == talents.KindTrailhead
}

// scanKBArticles walks the KB directory for .md files with tags:
// frontmatter. Only top-level and one-level-deep files are scanned
// (matching typical KB layouts like kb:dossiers/foo.md).
//
// untagged decides what a tagless file means here. Skipping is right for a
// corpus where most documents are reference material and only some are
// injectable. Refusing is right where every document is meant to be
// classified: there, a missing frontmatter line is guidance silently going
// absent, and the root would rather name the file than guess.
func scanKBArticles(dir, untagged string) ([]kbArticle, error) {
	var articles []kbArticle
	var unclassified []string

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error { //nolint:revive // walk closure
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
			if untagged == documents.RootUntaggedRefuse {
				if rel, relErr := filepath.Rel(dir, path); relErr == nil {
					unclassified = append(unclassified, rel)
				} else {
					unclassified = append(unclassified, path)
				}
			}
			return nil // untagged documents are not auto-loaded
		}
		if strings.EqualFold(strings.TrimSpace(meta.Audience), "internal") {
			// The #1250 audience contract: an internal-audience document
			// is a private working surface (loop working notes, process
			// logs). Tags on it must not turn it into injected guidance —
			// doc_search already excludes it, and this scanner is the
			// other injection path.
			return nil
		}

		canonicalKind, _ := talents.CanonicalKind(meta.Kind)
		talents.WarnIfKindAlias(path, meta.Kind)
		articles = append(articles, kbArticle{
			Path:     path,
			Tags:     meta.Tags,
			TagsAll:  append([]string(nil), meta.TagsAll...),
			Kind:     canonicalKind,
			Teaser:   strings.TrimSpace(meta.Teaser),
			NextTags: append([]string(nil), meta.NextTags...),
			Name:     strings.TrimSuffix(d.Name(), ".md"),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		return nil, fmt.Errorf("%d document(s) in %s carry no tags and this root refuses unclassified content: %s",
			len(unclassified), dir, strings.Join(unclassified, ", "))
	}

	// Sort for deterministic ordering.
	sort.Slice(articles, func(i, j int) bool {
		if isTrailheadKind(articles[i].Kind) && !isTrailheadKind(articles[j].Kind) {
			return true
		}
		if !isTrailheadKind(articles[i].Kind) && isTrailheadKind(articles[j].Kind) {
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
