package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/model/talents"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// finalizeCapabilityTags resolves capability-tag membership from the
// fully-assembled tool registry and wires up all downstream state that
// depends on that snapshot: core tags on the delegate executor,
// tag context assembler, capability surface, manifest talent
// prepending, capability tools on the registry, and the delegate's
// tag-context closure.
//
// It runs AFTER every other init phase — including [initServers], which
// registers mqtt_wake_* tools. That timing is the whole point of
// separating this step out of [initDelegation]: it closes the
// init-order drift described in #733, because tools registered by any
// earlier phase are present in the registry when the snapshot is
// taken.
//
// Subsystems whose backing runtime binds asynchronously (Signal is
// the canonical example) declare their tools up front via
// tools.Provider and return tools.ErrUnavailable from the handler
// until Bind is called. Those tools ARE in the snapshot — the registry
// sees them from initChannels onwards.
//
// Tools registered synchronously by an earlier init phase (the
// macos_calendar_events companion tool and mqtt_wake_* in initServers,
// watchlist tools in initAwareness) are likewise present here, because
// the finalizer runs last. See the doc comment in new.go for the
// ordering rules.
func (a *App) finalizeCapabilityTags(s *newState) error {
	cfg := a.cfg
	logger := a.logger

	if cfg.Unverified() {
		// Withheld here, once the registry is fully assembled, so the
		// denial covers every tool any later path can reach.
		a.loop.Tools().WithholdDirectHumanEgress()
		logger.Warn("withholding direct human egress tools: config is unverified",
			"tools", tools.DirectHumanEgressToolNames())
	}

	resolved := resolveCapabilityTags(a.loop.Tools(), cfg.CapabilityTags)
	resolvedCapTags := resolved.Configs
	if len(resolvedCapTags) == 0 {
		return nil
	}

	delegateExec := a.delegateExec

	// parsedTalents was loaded earlier in startup; copy the slice
	// header so the manifest prepend below doesn't modify the outer
	// variable.
	capTalents := append([]talents.Talent(nil), s.parsedTalents...)

	// Core tags on the delegate executor. Moved here (from
	// initDelegation) so the tag set is taken from the finalized
	// snapshot, not the mid-init snapshot that preceded initServers.
	var coreTags []string
	for tag, tagCfg := range resolvedCapTags {
		if tagCfg.Core {
			coreTags = append(coreTags, tag)
		}
	}
	if len(coreTags) > 0 && delegateExec != nil {
		delegateExec.SetCoreTags(coreTags)
	}

	// Warn about tools referenced in config but not registered.
	// This catches typos, missing MCP servers, and tools gated by
	// config (e.g., shell_exec disabled). Non-fatal: skip the missing
	// tool.
	//
	// Every tool a config tag can reference is registered by an init
	// phase before this finalizer runs: synchronously (e.g.
	// macos_calendar_events and mqtt_wake_* in initServers) or declared
	// up front via tools.Provider (Signal, watchlist). Provider tools
	// whose runtime has not bound yet are still present here; only
	// invocation surfaces tools.ErrUnavailable until Bind supplies the
	// runtime.
	for tag, tagCfg := range resolvedCapTags {
		for _, toolName := range tagCfg.Tools {
			if a.loop.Tools().Get(toolName) == nil {
				logger.Warn("capability tag references unregistered tool",
					"tag", tag, "tool", toolName)
			}
		}
	}

	// The complementary check: a native tool registered but missing from the
	// tool catalog carries no capability tag and is silently never offered to
	// the model. This runs against the fully-assembled registry, so it catches
	// the gap for every tool from every package (see uncataloguedNativeTools).
	if missing := uncataloguedNativeTools(a.loop.Tools()); len(missing) > 0 {
		logger.Error("native tools registered but absent from the tool catalog; they carry no capability tag and will NOT be offered to the model — add them to internal/model/toolcatalog/catalog.go with the right tags",
			"tools", missing, "count", len(missing))
	}

	// Audit operator-excluded tools that downstream wiring expects to
	// be available. Personas/talents and the orchestrator allowlist
	// are the most common silent-breakage paths when an exclude turns
	// off a tool another subsystem assumed was present.
	auditExcludedToolReferences(logger, resolved, cfg.Agent.OrchestratorTools, s.parsedTalents)

	// Build the shared tag context assembler. KB article counts feed
	// the manifest; live providers (registered during initAwareness
	// and here for forge) feed the liveTags map.
	var kbDir string
	if s.resolver != nil {
		resolved, err := s.resolver.Resolve("kb:")
		if err == nil {
			kbDir = resolved
		}
	}
	var contextVerifier interface {
		VerifyRef(ctx context.Context, ref string, consumer string) error
		VerifyPath(ctx context.Context, path string, consumer string) error
	}
	if a.documentStore != nil {
		contextVerifier = a.documentStore
	}

	injectRoots := injectionEligibleRoots(cfg, s.resolver, logger)
	// Enforce the refusal here rather than only at scan time. A root that
	// refuses unclassified content is saying its documents are load-bearing,
	// and a per-turn warning about one of them going missing is the quiet
	// failure the policy exists to prevent — the instance should decline to
	// run and name the file, the way it declines an uncommitted one.
	if err := agent.CheckUnclassifiedDocuments(injectRoots); err != nil {
		return fmt.Errorf("document root refuses unclassified content: %w", err)
	}

	tagCtxAssembler := agent.NewTagContextAssembler(agent.TagContextAssemblerConfig{
		CapTags:     resolvedCapTags,
		KBDir:       kbDir,
		InjectRoots: injectRoots,
		Resolver:    s.resolver,
		Verifier:    contextVerifier,
		HAInject:    a.loop.HAInject(),
		Logger:      logger.With("component", "tag_context"),
	})

	// Wire the assembler before tag context providers register so the
	// forge registration below (and any pending always/tag providers
	// staged during initAwareness) flush directly into the assembler
	// instead of staying in the pending bucket.
	a.loop.SetTagContextAssembler(tagCtxAssembler)

	// Register forge as a tag context provider so its account config
	// and recent operations appear/disappear with the forge capability
	// tag.
	if a.forgeService != nil {
		a.loop.RegisterTagContextProvider("forge", a.forgeService.ContextProvider())
	}

	// Build manifest entries with enriched context info.
	kbCounts := tagCtxAssembler.KBArticleTags()
	menuHints := mergeTalentMenuHints(tagCtxAssembler.KBMenuHints(), capTalents)
	liveProviders := tagCtxAssembler.TaggedProviders()

	// Discover ad-hoc tags from KB articles and talents that aren't in
	// the config. These can be activated at runtime to load their
	// tagged content without requiring config changes.
	configuredTags := make(map[string]bool, len(resolvedCapTags))
	for tag := range resolvedCapTags {
		configuredTags[tag] = true
	}
	adHocTags := make(map[string]bool)
	for tag := range kbCounts {
		if !configuredTags[tag] {
			adHocTags[tag] = true
		}
	}
	for _, t := range capTalents {
		for _, tag := range t.Tags {
			if !configuredTags[tag] {
				adHocTags[tag] = true
			}
		}
	}

	liveTags := make(map[string]bool, len(liveProviders))
	for tag := range liveProviders {
		liveTags[tag] = true
	}

	capSurface := buildCapabilitySurface(resolved, kbCounts, menuHints, liveTags, adHocTags)
	a.capSurface = capSurface

	if manifestTalent := talents.GenerateManifest(capSurface); manifestTalent != nil {
		capTalents = append([]talents.Talent{*manifestTalent}, capTalents...)
	}

	a.loop.ConfigureCapabilityWiring(agent.CapabilityWiring{
		Tags:             resolvedCapTags,
		ParsedTalents:    capTalents,
		Surface:          capSurface,
		Store:            agent.NewOpstateCapabilityTagStore(a.opStore),
		ContextAssembler: tagCtxAssembler,
	})
	a.loop.Tools().SetCapabilityTools(a.loop, capSurface)

	var activeTagNames []string
	for tag := range a.loop.LastRunTags() {
		activeTagNames = append(activeTagNames, tag)
	}
	logger.Info("capability tags enabled",
		"tags", len(resolvedCapTags),
		"core_tags", activeTagNames,
		"talents", len(s.parsedTalents),
		"kb_tagged_articles", kbCounts,
	)

	return nil
}

// injectionEligibleRoots resolves the roots whose tagged documents may
// be injected into a prompt. It returns nil when no root declares a
// context policy, which keeps the legacy single-kb scan in force so an
// existing config assembles context exactly as before.
//
// Injection is declared, never inferred: a corpus can place text into a
// system prompt without anyone asking for it, so a root reaches the
// prompt only because policy says it may. That matters most for a root
// synced from a remote Thane does not control, where the frontmatter a
// document carries is not evidence of anything.
func injectionEligibleRoots(cfg *config.Config, resolver *paths.Resolver, logger *slog.Logger) map[string]agent.InjectRoot {
	if cfg == nil || resolver == nil {
		return nil
	}
	declared := false
	for _, policy := range cfg.DocRoots {
		if policy.Context.Declared() {
			declared = true
			break
		}
	}
	if !declared {
		return nil
	}

	eligible := make(map[string]agent.InjectRoot)
	for name, policy := range cfg.DocRoots {
		if policy.Context.EffectiveInject() != config.RootInjectTagged {
			continue
		}
		dir, err := resolver.Resolve(name + ":")
		if err != nil {
			logger.Warn("root declares tagged injection but does not resolve to a directory",
				"root", name, "error", err)
			continue
		}
		eligible[name] = agent.InjectRoot{
			Dir:         dir,
			RequiresTag: policy.Context.RequiresTag,
			Untagged:    policy.Context.EffectiveUntagged(),
		}
	}
	logger.Info("context injection policy resolved",
		"injection_eligible_roots", len(eligible))
	return eligible
}
