package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/platform/provenance"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
)

// CoreContextProvider reads curated core identity documents for prompt
// assembly.
type CoreContextProvider struct {
	mu sync.RWMutex

	logger             *slog.Logger
	egoFile            string
	provenanceStore    *provenance.Store
	injectFiles        []string
	injectFileVerifier func(context.Context, string, string) error
	now                func() time.Time
}

type corePromptSection struct {
	name    string
	title   string
	content string
}

// CoreContextProviderConfig holds optional wiring for
// [CoreContextProvider].
type CoreContextProviderConfig struct {
	Logger          *slog.Logger
	EgoFile         string
	ProvenanceStore *provenance.Store
	InjectFiles     []string
	Now             func() time.Time
}

// NewCoreContextProvider creates a core context provider.
func NewCoreContextProvider(cfg CoreContextProviderConfig) *CoreContextProvider {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &CoreContextProvider{
		logger:          cfg.Logger,
		egoFile:         cfg.EgoFile,
		provenanceStore: cfg.ProvenanceStore,
		injectFiles:     append([]string(nil), cfg.InjectFiles...),
		now:             cfg.Now,
	}
}

func (p *CoreContextProvider) updateEgoFile(path string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.egoFile = path
	p.mu.Unlock()
}

func (p *CoreContextProvider) updateInjectFiles(paths []string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.injectFiles = append([]string(nil), paths...)
	p.mu.Unlock()
}

func (p *CoreContextProvider) updateInjectFileVerifier(verifier func(context.Context, string, string) error) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.injectFileVerifier = verifier
	p.mu.Unlock()
}

func (p *CoreContextProvider) updateProvenanceStore(store *provenance.Store) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.provenanceStore = store
	p.mu.Unlock()
}

// TagContextBucket keeps compatibility output in continuity when a
// CoreContextProvider is explicitly registered as a context provider.
// Normal prompt assembly renders core files as first-class stable
// sections before runtime context.
func (p *CoreContextProvider) TagContextBucket() agentctx.ContextBucket {
	return agentctx.ContextBucketContinuity
}

// TagContext returns core context for compatibility with the
// context-provider interface.
func (p *CoreContextProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	sections := p.promptSections(ctx)
	if len(sections) == 0 {
		return "", nil
	}
	var sb strings.Builder
	for _, section := range sections {
		promptfmt.AppendMarkdownSection(&sb, 3, section.title, section.content)
	}
	return strings.TrimSpace(sb.String()), nil
}

func (p *CoreContextProvider) promptSections(ctx context.Context) []corePromptSection {
	if p == nil {
		return nil
	}

	p.mu.RLock()
	egoFile := p.egoFile
	prov := p.provenanceStore
	injectFiles := append([]string(nil), p.injectFiles...)
	verifier := p.injectFileVerifier
	now := p.now
	logger := p.logger
	p.mu.RUnlock()
	if logger == nil {
		logger = slog.Default()
	}
	if now == nil {
		now = time.Now
	}

	var sections []corePromptSection
	if content := p.readEgo(ctx, egoFile, prov, verifier, now, logger); content != "" {
		sections = append(sections, corePromptSection{
			name:    "EGO",
			title:   "Self-Reflection (ego.md)",
			content: content,
		})
	}
	if content := p.readInjectFiles(ctx, injectFiles, verifier, logger); content != "" {
		sections = append(sections, corePromptSection{
			name:    "INJECTED CONTEXT",
			title:   "Injected Context",
			content: content,
		})
	}
	return sections
}

func (p *CoreContextProvider) readEgo(ctx context.Context, egoFile string, prov *provenance.Store, verifier func(context.Context, string, string) error, now func() time.Time, logger *slog.Logger) string {
	if prov != nil {
		return p.readEgoFromProvenance(ctx, prov, now, logger)
	}
	if strings.TrimSpace(egoFile) == "" {
		return ""
	}
	if verifier != nil {
		if err := verifier(ctx, egoFile, "ego_file"); err != nil {
			logger.Warn("ego file blocked by verification policy", "path", egoFile, "error", err)
			return ""
		}
	}
	data, err := os.ReadFile(egoFile)
	if err != nil || len(data) == 0 {
		return ""
	}
	return truncateCoreContext(string(data), maxEgoBytes, "\n\n[ego.md truncated — exceeded 16 KB limit]")
}

func (p *CoreContextProvider) readEgoFromProvenance(ctx context.Context, prov *provenance.Store, now func() time.Time, logger *slog.Logger) string {
	content, err := prov.Read("ego.md")
	if err != nil || len(content) == 0 {
		return ""
	}

	var sb strings.Builder
	if hist, err := prov.History(ctx, "ego.md"); err == nil && hist.RevisionCount > 0 {
		sb.WriteString(fmt.Sprintf("(updated %s by %s, revision %d)\n",
			promptfmt.FormatDeltaOnly(hist.LastModified, now()), hist.LastMessage, hist.RevisionCount))
	} else if err != nil {
		logger.Debug("failed to read ego.md provenance history", "error", err)
	}
	sb.WriteString("\n")
	sb.WriteString(truncateCoreContext(content, maxEgoBytes, "\n\n[ego.md truncated — exceeded 16 KB limit]"))
	return strings.TrimSpace(sb.String())
}

func (p *CoreContextProvider) readInjectFiles(ctx context.Context, injectFiles []string, verifier func(context.Context, string, string) error, logger *slog.Logger) string {
	var ctxBuf strings.Builder
	for _, path := range injectFiles {
		if verifier != nil {
			if err := verifier(ctx, path, "inject_files"); err != nil {
				logger.Warn("inject file blocked by verification policy", "path", path, "error", err)
				continue
			}
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		if ctxBuf.Len() > 0 {
			ctxBuf.WriteString("\n\n---\n\n")
		}
		ctxBuf.Write(data)
	}
	if ctxBuf.Len() == 0 {
		return ""
	}
	return ctxBuf.String()
}

func truncateCoreContext(s string, maxBytes int, marker string) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	limit := maxBytes - len(marker)
	if limit <= 0 {
		return marker
	}
	cut := 0
	for i := range s {
		if i > limit {
			break
		}
		cut = i
	}
	if cut == 0 {
		return marker
	}
	return s[:cut] + marker
}
