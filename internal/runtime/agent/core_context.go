package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
	axiomsFile         string
	personaFile        string
	missionFile        string
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
	AxiomsFile      string
	PersonaFile     string
	MissionFile     string
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
		axiomsFile:      cfg.AxiomsFile,
		personaFile:     cfg.PersonaFile,
		missionFile:     cfg.MissionFile,
		egoFile:         cfg.EgoFile,
		provenanceStore: cfg.ProvenanceStore,
		injectFiles:     append([]string(nil), cfg.InjectFiles...),
		now:             cfg.Now,
	}
}

func (p *CoreContextProvider) updateAxiomsFile(path string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.axiomsFile = path
	p.mu.Unlock()
}

func (p *CoreContextProvider) updatePersonaFile(path string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.personaFile = path
	p.mu.Unlock()
}

func (p *CoreContextProvider) updateMissionFile(path string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.missionFile = path
	p.mu.Unlock()
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

func (p *CoreContextProvider) preambleSections(ctx context.Context) []corePromptSection {
	if p == nil {
		return nil
	}

	p.mu.RLock()
	axiomsFile := p.axiomsFile
	verifier := p.injectFileVerifier
	logger := p.logger
	p.mu.RUnlock()
	if logger == nil {
		logger = slog.Default()
	}

	if section, ok := p.readPlainCoreSection(ctx, corePromptFileSpec{
		name:     "AXIOMS",
		title:    "Axioms (axioms.md)",
		path:     axiomsFile,
		consumer: "axioms_file",
		maxBytes: maxAxiomsBytes,
		marker:   "\n\n[axioms.md truncated — exceeded 16 KB limit]",
	}, verifier, logger); ok {
		return []corePromptSection{section}
	}
	return nil
}

func (p *CoreContextProvider) personaContent(ctx context.Context) string {
	if p == nil {
		return ""
	}

	p.mu.RLock()
	personaFile := p.personaFile
	verifier := p.injectFileVerifier
	logger := p.logger
	p.mu.RUnlock()
	if logger == nil {
		logger = slog.Default()
	}

	return p.readPlainCoreFile(ctx, personaFile, verifier, "persona_file", maxPersonaBytes, "\n\n[persona.md truncated — exceeded 16 KB limit]", logger)
}

func (p *CoreContextProvider) promptSections(ctx context.Context) []corePromptSection {
	if p == nil {
		return nil
	}

	p.mu.RLock()
	missionFile := p.missionFile
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
	if section, ok := p.readPlainCoreSection(ctx, corePromptFileSpec{
		name:     "MISSION",
		title:    "Mission (mission.md)",
		path:     missionFile,
		consumer: "mission_file",
		maxBytes: maxMissionBytes,
		marker:   "\n\n[mission.md truncated — exceeded 16 KB limit]",
	}, verifier, logger); ok {
		sections = append(sections, section)
	}
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

type corePromptFileSpec struct {
	name     string
	title    string
	path     string
	consumer string
	maxBytes int
	marker   string
}

func (p *CoreContextProvider) readPlainCoreSection(ctx context.Context, spec corePromptFileSpec, verifier func(context.Context, string, string) error, logger *slog.Logger) (corePromptSection, bool) {
	content := p.readPlainCoreFile(ctx, spec.path, verifier, spec.consumer, spec.maxBytes, spec.marker, logger)
	if content == "" {
		return corePromptSection{}, false
	}
	return corePromptSection{
		name:    spec.name,
		title:   spec.title,
		content: content,
	}, true
}

func (p *CoreContextProvider) readEgo(ctx context.Context, egoFile string, prov *provenance.Store, verifier func(context.Context, string, string) error, now func() time.Time, logger *slog.Logger) string {
	if prov != nil {
		return p.readEgoFromProvenance(ctx, prov, now, logger)
	}
	return p.readPlainCoreFile(ctx, egoFile, verifier, "ego_file", maxEgoBytes, "\n\n[ego.md truncated — exceeded 16 KB limit]", logger)
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
		content := p.readPlainCoreFile(ctx, path, verifier, "inject_files", maxInjectFileBytes,
			fmt.Sprintf("\n\n[%s truncated — exceeded 16 KB limit]", filepath.Base(path)), logger)
		if content == "" {
			continue
		}
		if ctxBuf.Len() > 0 {
			ctxBuf.WriteString("\n\n---\n\n")
		}
		ctxBuf.WriteString(content)
	}
	if ctxBuf.Len() == 0 {
		return ""
	}
	return ctxBuf.String()
}

func (p *CoreContextProvider) readPlainCoreFile(ctx context.Context, path string, verifier func(context.Context, string, string) error, consumer string, maxBytes int, marker string, logger *slog.Logger) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if logger == nil {
		logger = slog.Default()
	}
	if verifier != nil {
		if err := verifier(ctx, path, consumer); err != nil {
			logger.Warn("core prompt file blocked by verification policy", "path", path, "consumer", consumer, "error", err)
			return ""
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("core prompt file unreadable", "path", path, "consumer", consumer, "error", err)
		}
		return ""
	}
	if len(data) == 0 {
		return ""
	}
	return truncateCoreContext(string(data), maxBytes, marker)
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
