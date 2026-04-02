package app

import (
	"context"
	"sort"
	"time"

	"github.com/nugget/thane-ai-agent/internal/agent"
	"github.com/nugget/thane-ai-agent/internal/delegate"
	"github.com/nugget/thane-ai-agent/internal/forge"
	"github.com/nugget/thane-ai-agent/internal/notifications"
	"github.com/nugget/thane-ai-agent/internal/talents"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// initDelegation wires up the delegate executor, notification callback
// routing, orchestrator tool gating, capability tags, channel tags, and
// behavioral lenses.
func (a *App) initDelegation(s *newState) error {
	cfg := a.cfg
	logger := a.logger

	// --- Delegation ---
	// Register thane_delegate tool AFTER all other tools so the delegate
	// executor's parent registry snapshot includes the full tool set.
	delegateExec := delegate.NewExecutor(logger, a.llmClient, a.rtr, a.loop.Tools(), cfg.Models.Default)
	if len(cfg.Delegate.Profiles) > 0 {
		overrides := make(map[string]delegate.ProfileOverride, len(cfg.Delegate.Profiles))
		for name, pc := range cfg.Delegate.Profiles {
			overrides[name] = delegate.ProfileOverride{
				ToolTimeout: pc.ToolTimeout,
				MaxDuration: pc.MaxDuration,
				MaxIter:     pc.MaxIter,
				MaxTokens:   pc.MaxTokens,
			}
		}
		delegateExec.ApplyProfileOverrides(overrides)
	}
	delegateExec.SetTimezone(cfg.Timezone)
	if a.contentWriter != nil {
		delegateExec.SetContentWriter(a.contentWriter)
	}
	delegateExec.SetArchiver(a.archiveStore)
	delegateExec.SetUsageRecorder(a.usageStore, cfg.Pricing)
	delegateExec.SetEventBus(a.eventBus)
	delegateExec.ConfigureLoopExecution(a.loop, a.loopRegistry)
	delegateExec.ConfigureSessionLifecycle(a.archiveAdapter, a.mem)
	var alwaysActiveTags []string
	for tag, tagCfg := range cfg.CapabilityTags {
		if tagCfg.AlwaysActive {
			alwaysActiveTags = append(alwaysActiveTags, tag)
		}
	}
	if len(alwaysActiveTags) > 0 {
		delegateExec.SetAlwaysActiveTags(alwaysActiveTags)
	}
	if a.forgeMgr != nil {
		delegateExec.SetForgeContext(a.forgeMgr.Context())
	}
	if tfs := a.loop.Tools().TempFileStore(); tfs != nil {
		delegateExec.SetTempFileStore(tfs)
	}
	a.loop.Tools().Register(&tools.Tool{
		Name:        "thane_delegate",
		Description: delegate.ToolDescription,
		Parameters:  delegate.ToolDefinition(),
		Handler:     delegate.ToolHandler(delegateExec),
	})
	a.delegateExec = delegateExec
	logger.Info("delegation enabled", "profiles", delegateExec.ProfileNames())

	// --- Notification callback routing ---
	// Wire up the callback dispatcher and timeout watcher for actionable
	// notifications. Requires both the notification record store and the
	// delegate executor (for spawning responses when the session is gone).
	if a.notifRecords != nil {
		sessionInj := &notifSessionInjector{mem: a.mem, archiver: a.archiveAdapter}
		delegateSpn := &notifDelegateSpawner{exec: delegateExec}
		a.notifCallbackDispatcher = notifications.NewCallbackDispatcher(
			a.notifRecords, sessionInj, delegateSpn, cfg.MQTT.DeviceName, logger,
		)

		// Use the router for escalation so timeout_action: "escalate"
		// respects per-recipient routing preferences. Falls back to the
		// raw HA sender when the router is unavailable.
		var escalationSender notifications.EscalationSender
		if a.notifRouter != nil {
			escalationSender = a.notifRouter
		} else if a.notifSender != nil {
			escalationSender = a.notifSender
		}
		timeoutWatcher := notifications.NewTimeoutWatcher(
			a.notifRecords, a.notifCallbackDispatcher, escalationSender,
			30*time.Second, logger,
		)
		a.deferWorker("notification-timeout-watcher", func(ctx context.Context) error {
			go timeoutWatcher.Start(ctx)
			return nil
		})
		a.loop.Tools().SetCallbackDispatcher(a.notifCallbackDispatcher)

		// Synchronous escalation support — allows tools to block
		// waiting for human responses via any notification channel.
		escalationWaiter := notifications.NewResponseWaiter()
		a.notifCallbackDispatcher.SetResponseWaiter(escalationWaiter)
		a.loop.Tools().SetEscalationTools(tools.EscalationDeps{
			Router:     a.notifRouter,
			Records:    a.notifRecords,
			Dispatcher: a.notifCallbackDispatcher,
			Waiter:     escalationWaiter,
		})

		logger.Info("notification callback dispatcher and timeout watcher initialized")
	}

	// --- Orchestrator tool gating ---
	// When delegation_required is true, the agent loop only sees
	// lightweight tools (delegate + memory), steering the primary model
	// toward delegation instead of direct tool use.
	if cfg.Agent.DelegationRequired {
		a.loop.SetOrchestratorTools(cfg.Agent.OrchestratorTools)
		logger.Info("orchestrator tool gating enabled", "tools", cfg.Agent.OrchestratorTools)
	}

	// --- Capability tags ---
	// Tag-driven tool and talent filtering. When configured, tools and
	// talents are grouped into named capabilities that can be activated
	// per-conversation via activate_capability/deactivate_capability tools.
	if len(cfg.CapabilityTags) > 0 {
		// parsedTalents was loaded above; copy the slice header so the
		// manifest prepend below doesn't modify the outer variable.
		capTalents := append([]talents.Talent(nil), s.parsedTalents...)

		// Warn about tools referenced in config but not registered.
		// This catches typos, missing MCP servers, and tools gated by config
		// (e.g., shell_exec disabled). Non-fatal: skip the missing tool.
		// Tools in s.deferredTools are registered by StartWorkers and are
		// expected to be absent during New().
		for tag, tagCfg := range cfg.CapabilityTags {
			for _, toolName := range tagCfg.Tools {
				if a.loop.Tools().Get(toolName) == nil && !s.deferredTools[toolName] {
					logger.Warn("capability tag references unregistered tool",
						"tag", tag, "tool", toolName)
				}
			}
		}

		// Build the shared tag context assembler early so KB article
		// counts are available for the manifest. It merges two sources
		// per active tag: tagged KB articles (frontmatter tags: [forge])
		// and live providers.
		var kbDir string
		if s.resolver != nil {
			resolved, err := s.resolver.Resolve("kb:")
			if err == nil {
				kbDir = resolved
			}
		}

		tagCtxAssembler := agent.NewTagContextAssembler(agent.TagContextAssemblerConfig{
			CapTags:  cfg.CapabilityTags,
			KBDir:    kbDir,
			HAInject: a.loop.HAInject(),
			Logger:   logger.With("component", "tag_context"),
		})

		// Register forge as a tag context provider so its account
		// config and recent operations appear/disappear with the
		// forge capability tag.
		if a.forgeMgr != nil {
			a.loop.RegisterTagContextProvider("forge", forge.NewContextProvider(a.forgeMgr, s.forgeOpLog))
		}

		// Build manifest entries with enriched context info.
		kbCounts := tagCtxAssembler.KBArticleTags()
		liveProviders := a.loop.TagContextProviders()

		tagIndex := make(map[string][]string, len(cfg.CapabilityTags))
		descriptions := make(map[string]string, len(cfg.CapabilityTags))
		alwaysActiveMap := make(map[string]bool, len(cfg.CapabilityTags))
		for tag, tagCfg := range cfg.CapabilityTags {
			tagIndex[tag] = tagCfg.Tools
			descriptions[tag] = tagCfg.Description
			alwaysActiveMap[tag] = tagCfg.AlwaysActive
		}
		manifest := tools.BuildCapabilityManifest(tagIndex, descriptions, alwaysActiveMap)

		manifestEntries := make([]talents.ManifestEntry, len(manifest))
		for i, m := range manifest {
			manifestEntries[i] = talents.ManifestEntry{
				Tag:          m.Tag,
				Description:  m.Description,
				Tools:        m.Tools,
				AlwaysActive: m.AlwaysActive,
				KBArticles:   kbCounts[m.Tag],
				LiveContext:  liveProviders[m.Tag] != nil,
			}
		}

		// Discover ad-hoc tags from KB articles and talents that aren't
		// in the config. These can be activated at runtime to load their
		// tagged content without requiring config changes.
		configuredTags := make(map[string]bool, len(cfg.CapabilityTags))
		for tag := range cfg.CapabilityTags {
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
		for tag := range adHocTags {
			manifestEntries = append(manifestEntries, talents.ManifestEntry{
				Tag:        tag,
				AdHoc:      true,
				KBArticles: kbCounts[tag],
			})
		}

		if manifestTalent := talents.GenerateManifest(manifestEntries); manifestTalent != nil {
			capTalents = append([]talents.Talent{*manifestTalent}, capTalents...)
		}

		a.loop.SetCapabilityTags(cfg.CapabilityTags, capTalents)
		a.loop.Tools().SetCapabilityTools(a.loop, manifest)
		a.loop.SetTagContextAssembler(tagCtxAssembler)
		a.loop.SetCapabilityTagStore(agent.NewOpstateCapabilityTagStore(a.opStore))

		// Behavioral lenses are wired below (outside this block)
		// so they work even without capability_tags configured.

		// Expose the agent loop's active tags to every process loop
		// spawned through the registry so the dashboard can display
		// dynamically activated capabilities.
		a.loopRegistry.SetDefaultActiveTagsFunc(func() []string {
			tags := a.loop.LastRunTags()
			if tags == nil {
				return nil
			}
			result := make([]string, 0, len(tags))
			for t := range tags {
				result = append(result, t)
			}
			sort.Strings(result)
			return result
		})

		// Wire tag context into delegates. The closure captures both the
		// assembler and the loop so delegates always see the latest
		// registered providers at call time (not a stale snapshot from
		// construction time).
		delegateExec.SetTagContextFunc(func(ctx context.Context, activeTags map[string]bool) string {
			return tagCtxAssembler.Build(ctx, activeTags, a.loop.TagContextProviders())
		})

		var activeTagNames []string
		for tag := range a.loop.LastRunTags() {
			activeTagNames = append(activeTagNames, tag)
		}
		logger.Info("capability tags enabled",
			"tags", len(cfg.CapabilityTags),
			"always_active", activeTagNames,
			"talents", len(s.parsedTalents),
			"kb_tagged_articles", kbCounts,
		)
	}

	if len(cfg.ChannelTags) > 0 {
		a.loop.SetChannelTags(cfg.ChannelTags)
		logger.Info("channel tags configured", "channels", len(cfg.ChannelTags))
	}

	// --- Behavioral lenses ---
	// Persistent global context modes backed by opstate. Active lenses
	// are merged into every Run's capability scope (and every delegate
	// execution) so their KB articles and talents load globally.
	// Wired unconditionally — lenses work even without capability_tags.
	lensStore := tools.NewLensStore(a.opStore)
	a.loop.Tools().SetLensTools(lensStore)
	lensProviderFn := func() []string {
		lenses, err := lensStore.ActiveLenses()
		if err != nil {
			logger.Warn("failed to load active lenses", "error", err)
			return nil
		}
		return lenses
	}
	a.loop.SetLensProvider(lensProviderFn)
	delegateExec.SetLensProvider(lensProviderFn)
	if lenses, _ := lensStore.ActiveLenses(); len(lenses) > 0 {
		logger.Info("active lenses loaded from opstate", "lenses", lenses)
	}

	return nil
}
