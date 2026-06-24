package app

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/integrations/forge"
	"github.com/nugget/thane-ai-agent/internal/model/talents"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
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

	tagCtxAssembler := agent.NewTagContextAssembler(agent.TagContextAssemblerConfig{
		CapTags:  resolvedCapTags,
		KBDir:    kbDir,
		Resolver: s.resolver,
		Verifier: contextVerifier,
		HAInject: a.loop.HAInject(),
		Logger:   logger.With("component", "tag_context"),
	})

	// Wire the assembler before tag context providers register so the
	// forge registration below (and any pending always/tag providers
	// staged during initAwareness) flush directly into the assembler
	// instead of staying in the pending bucket.
	a.loop.SetTagContextAssembler(tagCtxAssembler)

	// Register forge as a tag context provider so its account config
	// and recent operations appear/disappear with the forge capability
	// tag.
	if a.forgeMgr != nil {
		a.loop.RegisterTagContextProvider("forge", forge.NewContextProvider(a.forgeMgr, s.forgeOpLog))
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
