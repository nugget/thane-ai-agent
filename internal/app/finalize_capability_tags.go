package app

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/integrations/forge"
	"github.com/nugget/thane-ai-agent/internal/model/talents"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
)

// finalizeCapabilityTags resolves capability-tag membership from the
// fully-assembled tool registry and wires up all downstream state that
// depends on that snapshot: always-active tags on the delegate executor,
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
// until Bind is called. Those tools ARE in the snapshot and do NOT
// belong in s.deferredTools — the registry sees them from initChannels
// onwards.
//
// s.deferredTools is a narrow remaining exemption for tool families
// whose handler is still registered inside a deferWorker closure
// (today: macos_calendar_events). See the doc comment in new.go for
// the ordering rules.
func (a *App) finalizeCapabilityTags(s *newState) error {
	cfg := a.cfg
	logger := a.logger

	resolvedCapTags := resolveCapabilityTags(a.loop.Tools(), cfg.CapabilityTags)
	if len(resolvedCapTags) == 0 {
		return nil
	}

	delegateExec := a.delegateExec

	// parsedTalents was loaded earlier in startup; copy the slice
	// header so the manifest prepend below doesn't modify the outer
	// variable.
	capTalents := append([]talents.Talent(nil), s.parsedTalents...)

	// Always-active tags on the delegate executor. Moved here (from
	// initDelegation) so the tag set is taken from the finalized
	// snapshot, not the mid-init snapshot that preceded initServers.
	var alwaysActiveTags []string
	for tag, tagCfg := range resolvedCapTags {
		if tagCfg.AlwaysActive {
			alwaysActiveTags = append(alwaysActiveTags, tag)
		}
	}
	if len(alwaysActiveTags) > 0 && delegateExec != nil {
		delegateExec.SetAlwaysActiveTags(alwaysActiveTags)
	}

	// Warn about tools referenced in config but not registered.
	// This catches typos, missing MCP servers, and tools gated by
	// config (e.g., shell_exec disabled). Non-fatal: skip the missing
	// tool.
	//
	// Tools in s.deferredTools are registered by a deferWorker closure
	// that runs after New() completes — today only macos_calendar_events.
	// Provider-migrated subsystems (Signal, watchlist, mqtt_wake) are
	// declared up front and never appear in deferredTools; their tools
	// are visible here and only invocation would surface
	// tools.ErrUnavailable until Bind supplies the runtime.
	for tag, tagCfg := range resolvedCapTags {
		for _, toolName := range tagCfg.Tools {
			if a.loop.Tools().Get(toolName) == nil && !s.deferredTools[toolName] {
				logger.Warn("capability tag references unregistered tool",
					"tag", tag, "tool", toolName)
			}
		}
	}

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

	// Register forge as a tag context provider so its account config
	// and recent operations appear/disappear with the forge capability
	// tag.
	if a.forgeMgr != nil {
		a.loop.RegisterTagContextProvider("forge", forge.NewContextProvider(a.forgeMgr, s.forgeOpLog))
	}

	// Build manifest entries with enriched context info.
	kbCounts := tagCtxAssembler.KBArticleTags()
	menuHints := tagCtxAssembler.KBMenuHints()
	liveProviders := a.loop.TagContextProviders()

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

	capSurface := buildCapabilitySurface(resolvedCapTags, kbCounts, menuHints, liveTags, adHocTags)
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
		"always_active", activeTagNames,
		"talents", len(s.parsedTalents),
		"kb_tagged_articles", kbCounts,
	)

	return nil
}
