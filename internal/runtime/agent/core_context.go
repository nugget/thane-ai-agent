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

// CoreContextProvider publishes curated core identity and continuity
// documents through the normal context-provider pipeline.
type CoreContextProvider struct {
	mu sync.RWMutex

	logger             *slog.Logger
	egoFile            string
	provenanceStore    *provenance.Store
	injectFiles        []string
	injectFileVerifier func(context.Context, string, string) error
	now                func() time.Time
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

// TagContext returns core continuity context for always-on injection.
func (p *CoreContextProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	if p == nil {
		return "", nil
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

	var sb strings.Builder
	p.appendEgo(ctx, &sb, egoFile, prov, verifier, now, logger)
	p.appendInjectFiles(ctx, &sb, injectFiles, verifier, logger)
	return strings.TrimSpace(sb.String()), nil
}

func (p *CoreContextProvider) appendEgo(ctx context.Context, sb *strings.Builder, egoFile string, prov *provenance.Store, verifier func(context.Context, string, string) error, now func() time.Time, logger *slog.Logger) {
	if prov != nil {
		p.appendEgoFromProvenance(ctx, sb, prov, now, logger)
		return
	}
	if strings.TrimSpace(egoFile) == "" {
		return
	}
	if verifier != nil {
		if err := verifier(ctx, egoFile, "ego_file"); err != nil {
			logger.Warn("ego file blocked by verification policy", "path", egoFile, "error", err)
			return
		}
	}
	data, err := os.ReadFile(egoFile)
	if err != nil || len(data) == 0 {
		return
	}
	appendProviderSeparator(sb)
	sb.WriteString("### Self-Reflection (ego.md)\n\n")
	sb.WriteString(truncateCoreContext(string(data), maxEgoBytes, "\n\n[ego.md truncated — exceeded 16 KB limit]"))
}

func (p *CoreContextProvider) appendEgoFromProvenance(ctx context.Context, sb *strings.Builder, prov *provenance.Store, now func() time.Time, logger *slog.Logger) {
	content, err := prov.Read("ego.md")
	if err != nil || len(content) == 0 {
		return
	}

	appendProviderSeparator(sb)
	sb.WriteString("### Self-Reflection (ego.md)\n")
	if hist, err := prov.History(ctx, "ego.md"); err == nil && hist.RevisionCount > 0 {
		sb.WriteString(fmt.Sprintf("(updated %s by %s, revision %d)\n",
			promptfmt.FormatDeltaOnly(hist.LastModified, now()), hist.LastMessage, hist.RevisionCount))
	} else if err != nil {
		logger.Debug("failed to read ego.md provenance history", "error", err)
	}
	sb.WriteString("\n")
	sb.WriteString(truncateCoreContext(content, maxEgoBytes, "\n\n[ego.md truncated — exceeded 16 KB limit]"))
}

func (p *CoreContextProvider) appendInjectFiles(ctx context.Context, sb *strings.Builder, injectFiles []string, verifier func(context.Context, string, string) error, logger *slog.Logger) {
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
		return
	}
	appendProviderSeparator(sb)
	sb.WriteString("### Injected Context\n\n")
	sb.WriteString(ctxBuf.String())
}

func appendProviderSeparator(sb *strings.Builder) {
	if sb.Len() > 0 {
		sb.WriteString("\n\n---\n\n")
	}
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
