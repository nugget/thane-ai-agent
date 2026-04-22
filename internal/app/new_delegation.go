package app

import (
	"context"
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
	resolvedCapTags := resolveCapabilityTags(a.loop.Tools(), cfg.CapabilityTags)

	// --- Delegation ---
	// Register thane_delegate tool AFTER all other tools so the delegate
	// executor's parent registry snapshot includes the full tool set.
	delegateExec := delegate.NewExecutor(logger, a.llmClient, a.rtr, a.loop.Tools(), a.modelCatalog.DefaultModel)
	conversationInjector := &conversationSystemInjector{mem: a.mem, archiver: a.archiveAdapter}
	completionDispatcher := a.ensureLoopCompletionDispatcher()
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
	if a.liveRequestRecorder != nil {
		delegateExec.UseLiveRequestRecorder(a.liveRequestRecorder)
	}
	if a.requestRecorder != nil {
		delegateExec.SetRequestRecorder(a.requestRecorder)
	}
	delegateExec.SetArchiver(a.archiveStore)
	delegateExec.SetUsageRecorder(a.usageStore, cfg.Pricing, a.modelCatalog)
	delegateExec.UseModelRegistry(a.modelRegistry)
	delegateExec.SetEventBus(a.eventBus)
	delegateExec.ConfigureLoopExecution(&loopAdapter{agentLoop: a.loop, router: a.rtr, capSurface: a.capSurface}, a.loopRegistry)
	delegateExec.ConfigureLoopCompletionSink(completionDispatcher.Deliver)
	delegateExec.ConfigureSessionLifecycle(a.archiveAdapter, a.mem)
	var alwaysActiveTags []string
	for tag, tagCfg := range resolvedCapTags {
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
		delegateSpn := &notifDelegateSpawner{exec: delegateExec}
		a.notifCallbackDispatcher = notifications.NewCallbackDispatcher(
			a.notifRecords, conversationInjector, delegateSpn, cfg.MQTT.DeviceName, logger,
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
	// Tag-driven tool and talent filtering. Tools contribute a compiled-in
	// baseline of default tags/toolsets, then config overlays descriptions,
	// membership overrides, and custom tags. The final tags can be activated
	// per-conversation via activate_capability/deactivate_capability tools.
	if len(resolvedCapTags) > 0 {
		// parsedTalents was loaded above; copy the slice header so the
		// manifest prepend below doesn't modify the outer variable.
		capTalents := append([]talents.Talent(nil), s.parsedTalents...)

		// Warn about tools referenced in config but not registered.
		// This catches typos, missing MCP servers, and tools gated by config
		// (e.g., shell_exec disabled). Non-fatal: skip the missing tool.
		//
		// Tools in s.deferredTools are legitimately absent at snapshot
		// time. Two reasons:
		//
		//   1. Registered by a deferWorker closure that runs after New()
		//      completes (e.g., Signal tools, which require the signal-cli
		//      client to start).
		//   2. Registered in initServers, which must run after this phase
		//      because it depends on delegation outputs (capSurface,
		//      notifCallbackDispatcher) — e.g., mqtt_wake_* tools.
		//
		// Both classes are tracked in new_channels.go so the warning
		// suppressed here corresponds to a known late-registration path,
		// not a missing tool.
		for tag, tagCfg := range resolvedCapTags {
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
			CapTags:  resolvedCapTags,
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
		menuHints := tagCtxAssembler.KBMenuHints()
		liveProviders := a.loop.TagContextProviders()

		// Discover ad-hoc tags from KB articles and talents that aren't
		// in the config. These can be activated at runtime to load their
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

		a.loop.SetCapabilityTags(resolvedCapTags, capTalents)
		a.loop.UseCapabilitySurface(capSurface)
		a.loop.Tools().SetCapabilityTools(a.loop, capSurface)
		a.loop.SetTagContextAssembler(tagCtxAssembler)
		a.loop.SetCapabilityTagStore(agent.NewOpstateCapabilityTagStore(a.opStore))

		// Behavioral lenses are wired below (outside this block)
		// so they work even without capability_tags configured.

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
			"tags", len(resolvedCapTags),
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
