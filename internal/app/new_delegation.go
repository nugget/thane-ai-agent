package app

import (
	"context"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/notifications"
	"github.com/nugget/thane-ai-agent/internal/runtime/delegate"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// initDelegation wires up the delegate executor, notification callback
// routing, orchestrator tool gating, channel tags, and behavioral
// lenses.
//
// Capability tags are NOT resolved here. Resolution is deferred to
// [finalizeCapabilityTags] so the snapshot reflects the fully-assembled
// tool registry, including tools registered in initServers (e.g.,
// mqtt_wake_*). Anything in this phase that *depends* on resolved
// capability tags — alwaysActiveTags, SetTagContextFunc, capability
// surface, manifest prepending — lives in finalizeCapabilityTags too.
func (a *App) initDelegation(s *newState) error {
	cfg := a.cfg
	logger := a.logger

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
	delegateExec.SetArchiver(a.archiveStore)
	delegateExec.SetEventBus(a.eventBus)
	delegateExec.ConfigureLoopExecution(&loopAdapter{agentLoop: a.loop, router: a.rtr, capSurface: a.capSurfaceGetter()}, a.loopRegistry)
	delegateExec.ConfigureLoopCompletionSink(completionDispatcher.Deliver)
	delegateExec.ConfigureSessionLifecycle(a.archiveAdapter, a.mem)
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

	// --- Channel tags ---
	// Channel tags don't depend on capability-tag resolution; wire them
	// immediately.
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
