package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/attachments"
	"github.com/nugget/thane-ai-agent/internal/channels/email"
	sigcli "github.com/nugget/thane-ai-agent/internal/channels/signal"
	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/contacts"
	"github.com/nugget/thane-ai-agent/internal/forge"
	"github.com/nugget/thane-ai-agent/internal/knowledge"
	"github.com/nugget/thane-ai-agent/internal/llm"
	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/mcp"
	"github.com/nugget/thane-ai-agent/internal/media"
	"github.com/nugget/thane-ai-agent/internal/memory"
	"github.com/nugget/thane-ai-agent/internal/notifications"
	"github.com/nugget/thane-ai-agent/internal/paths"
	"github.com/nugget/thane-ai-agent/internal/prompts"
	"github.com/nugget/thane-ai-agent/internal/provenance"
	"github.com/nugget/thane-ai-agent/internal/router"
	"github.com/nugget/thane-ai-agent/internal/scheduler"
	"github.com/nugget/thane-ai-agent/internal/search"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// initChannels wires tools and external channels into the agent loop.
// Sections include fact store, contact directory, notifications, email,
// forge, working memory, fact extraction, anticipation, provenance,
// attachments, file tools, content resolution, usage, shell exec, web
// search/fetch, media, archive, embeddings, MCP servers, and Signal.
func (a *App) initChannels(s *newState) error {
	// --- Fact store ---
	// Long-term memory backed by SQLite. Facts are discrete pieces of
	// knowledge that persist across conversations and restarts.
	factStore, err := knowledge.NewStore(a.cfg.DataDir+"/knowledge.db", a.logger)
	if err != nil {
		return fmt.Errorf("open fact store: %w", err)
	}
	a.factStore = factStore
	a.onCloseErr("facts", factStore.Close)

	factTools := knowledge.NewTools(factStore)
	a.loop.Tools().SetFactTools(factTools)
	a.logger.Info("fact store initialized", "path", a.cfg.DataDir+"/knowledge.db")

	// --- Contact directory ---
	// Structured storage for people and organizations. Separate database
	// from facts to keep concerns isolated.
	contactStore, err := contacts.NewStore(a.cfg.DataDir+"/contacts.db", a.logger)
	if err != nil {
		return fmt.Errorf("open contact store: %w", err)
	}
	a.contactStore = contactStore
	a.onCloseErr("contacts", contactStore.Close)

	// Wire summarizer → contact interaction tracking now that the
	// contact store is available. Register the callback before Start()
	// to avoid a race where the startup scan reads the field concurrently.
	a.summaryWorker.SetInteractionCallback(func(conversationID, sessionID string, endedAt time.Time, topics []string) {
		updateContactInteraction(contactStore, a.logger, conversationID, sessionID, endedAt, topics)
	})
	a.deferWorker("summary-worker", func(ctx context.Context) error {
		a.summaryWorker.Start(ctx)
		a.onClose("summary-worker", a.summaryWorker.Stop)
		return nil
	})

	contactTools := contacts.NewTools(contactStore)
	if a.cfg.Identity.ContactName != "" {
		contactTools.SetSelfContactName(a.cfg.Identity.ContactName)
	}
	a.loop.Tools().SetContactTools(contactTools)
	a.logger.Info("contact store initialized", "path", a.cfg.DataDir+"/contacts.db")

	// --- Notifications ---
	// Push notifications via HA companion app. Requires both the HA client
	// and the contact store for recipient → device resolution.
	if a.ha != nil {
		a.notifSender = notifications.NewSender(a.ha, contactStore, a.opStore, a.cfg.MQTT.DeviceName, a.logger)
		a.loop.Tools().SetHANotifier(a.notifSender)
		a.logger.Info("HA notification sender initialized")

		var nrErr error
		a.notifRecords, nrErr = notifications.NewRecordStore(a.mem.DB(), a.logger)
		if nrErr != nil {
			return fmt.Errorf("initialize notification record store: %w", nrErr)
		}
		a.loop.Tools().SetNotificationRecords(a.notifRecords)
		a.logger.Info("notification record store initialized")

		// Provider-agnostic notification router — wraps the HA push sender
		// behind a routing layer that selects delivery channel per recipient.
		a.notifRouter = notifications.NewNotificationRouter(contactStore, a.notifRecords, a.logger)
		a.notifRouter.RegisterProvider(notifications.NewHAPushProvider(a.notifSender))
		a.notifRouter.SetActivitySource(&channelActivityAdapter{
			loops: &channelLoopAdapter{registry: a.loopRegistry},
			store: contactStore,
		})
		a.notifRouter.SetSourceFunc(tools.NotificationSource)
		a.loop.Tools().SetNotificationRouter(a.notifRouter)
		a.logger.Info("notification router initialized", "providers", "ha_push")
	}

	// --- Email ---
	// Native IMAP/SMTP email. Replaces the MCP email server approach
	// with direct IMAP connections for reading and SMTP for sending,
	// supporting multiple accounts with trust zone gating.
	if a.cfg.Email.Configured() {
		emailMgr := email.NewManager(a.cfg.Email, a.logger)
		a.emailMgr = emailMgr
		a.onClose("email", emailMgr.Close)

		emailTools := email.NewTools(emailMgr, &emailContactResolver{store: contactStore})
		a.loop.Tools().SetEmailTools(emailTools)

		// Register each account with connwatch for health monitoring.
		for _, name := range emailMgr.AccountNames() {
			acctName := name // capture for closure
			acct, _ := emailMgr.Account(acctName)
			a.connMgr.Watch(s.ctx, connwatch.WatcherConfig{
				Name:    "email-" + acctName,
				Probe:   func(pCtx context.Context) error { return acct.Ping(pCtx) },
				Backoff: connwatch.DefaultBackoffConfig(),
				Logger:  a.logger,
			})
		}

		// --- Email polling ---
		// Periodic IMAP check for new messages via the loop infrastructure.
		// The handler checks UIDs against a high-water mark and dispatches
		// an agent conversation only when new mail is detected.
		if a.cfg.Email.PollIntervalSec > 0 {
			poller := email.NewPoller(emailMgr, a.opStore, a.logger)
			pollInterval := time.Duration(a.cfg.Email.PollIntervalSec) * time.Second
			loopCfg := looppkg.Config{
				Name:         "email-poller",
				SleepMin:     pollInterval,
				SleepMax:     pollInterval,
				SleepDefault: pollInterval,
				Jitter:       looppkg.Float64Ptr(0),
				Handler:      emailPollHandler(poller, a.loop, a.logger),
				Metadata: map[string]string{
					"subsystem": "email",
				},
			}
			loopDeps := looppkg.Deps{
				Logger:   a.logger,
				EventBus: a.eventBus,
			}
			a.deferWorker("email-poller", func(ctx context.Context) error {
				if _, err := a.loopRegistry.SpawnLoop(ctx, loopCfg, loopDeps); err != nil {
					return fmt.Errorf("spawn email poller loop: %w", err)
				}
				return nil
			})
		}

		a.logger.Info("email enabled", "accounts", emailMgr.AccountNames(), "poll_interval", a.cfg.Email.PollIntervalSec)
	} else {
		a.logger.Info("email disabled (not configured)")
	}

	// --- Forge integration ---
	// Native GitHub (and future Gitea/GitLab) integration. Replaces the
	// MCP github server with direct API calls via go-github.
	var forgeOpLog *forge.OperationLog
	if a.cfg.Forge.Configured() {
		var err error
		a.forgeMgr, err = forge.NewManager(a.cfg.Forge, a.logger)
		if err != nil {
			return fmt.Errorf("create forge manager: %w", err)
		}

		forgeOpLog = forge.NewOperationLog()
		forgeTools := forge.NewTools(a.forgeMgr, forgeOpLog, a.logger)
		a.loop.Tools().SetForgeTools(forgeTools)

		a.logger.Info("forge enabled", "accounts", len(a.cfg.Forge.Accounts))
	} else {
		a.logger.Info("forge disabled (not configured)")
	}
	s.forgeOpLog = forgeOpLog

	// --- Working memory tool ---
	// Gives the agent a read/write scratchpad for experiential context
	// that survives compaction. Auto-injected via context provider below.
	a.loop.Tools().SetWorkingMemoryStore(a.wmStore)

	// --- Fact extraction ---
	// Automatic extraction of facts from conversations. Runs async after
	// each interaction using a local model. Opt-in via config.
	if a.cfg.Extraction.Enabled {
		extractionModel := a.cfg.Extraction.Model
		a.logger.Info("fact extraction enabled", "model", extractionModel)

		// FactSetter adapter with confidence reinforcement: if a fact already
		// exists, bump its confidence rather than overwriting.
		factSetterAdapter := &factSetterFunc{store: factStore, logger: a.logger}

		extractor := memory.NewExtractor(factSetterAdapter, a.logger, a.cfg.Extraction.MinMessages)
		extractor.SetTimeout(time.Duration(a.cfg.Extraction.TimeoutSeconds) * time.Second)
		extractor.SetExtractFunc(func(ctx context.Context, userMsg, assistantResp string, history []memory.Message) (*memory.ExtractionResult, error) {
			// Build transcript from recent history (only complete messages).
			var transcript strings.Builder
			for _, m := range history {
				line := fmt.Sprintf("[%s] %s\n", m.Role, m.Content)
				if transcript.Len()+len(line) > 4000 {
					break
				}
				transcript.WriteString(line)
			}

			prompt := prompts.FactExtractionPrompt(userMsg, assistantResp, transcript.String())
			msgs := []llm.Message{{Role: "user", Content: prompt}}

			start := time.Now()
			resp, err := a.llmClient.Chat(ctx, extractionModel, msgs, nil)
			if err != nil {
				a.logger.Warn("fact extraction LLM call failed",
					"model", extractionModel,
					"elapsed_ms", time.Since(start).Milliseconds(),
					"error", err)
				return nil, err
			}
			a.logger.Debug("fact extraction LLM call complete",
				"model", extractionModel,
				"elapsed_ms", time.Since(start).Milliseconds(),
				"response_len", len(resp.Message.Content))

			// Parse JSON (strip code fences, same pattern as metadata gen)
			content := resp.Message.Content
			content = strings.TrimPrefix(content, "```json\n")
			content = strings.TrimPrefix(content, "```\n")
			content = strings.TrimSuffix(content, "\n```")
			content = strings.TrimSpace(content)

			var result memory.ExtractionResult
			if err := json.Unmarshal([]byte(content), &result); err != nil {
				preview := content
				if len(preview) > 500 {
					preview = preview[:500]
				}
				a.logger.Debug("extraction JSON parse failed",
					"raw_response", preview)
				return nil, fmt.Errorf("parse extraction result: %w", err)
			}
			return &result, nil
		})

		a.loop.SetExtractor(extractor)
	}

	// --- Anticipation store ---
	// Bridges intent to action. The agent can set anticipations ("I expect
	// X to happen") that trigger context injection when they're fulfilled.
	// Shares the main thane.db connection.
	anticipationStore, err := scheduler.NewAnticipationStore(a.mem.DB())
	if err != nil {
		return fmt.Errorf("create anticipation store: %w", err)
	}
	s.anticipationStore = anticipationStore

	anticipationTools := scheduler.NewAnticipationTools(anticipationStore)
	a.loop.Tools().SetAnticipationTools(anticipationTools)
	a.logger.Info("anticipation store initialized")

	// --- Provenance store ---
	// Git-backed file storage with SSH signature enforcement. When
	// configured, identity files (ego.md, metacognitive.md) are
	// auto-committed with cryptographic signatures on every write.
	if a.cfg.Provenance.Configured() {
		keyPath := paths.ExpandHome(a.cfg.Provenance.SigningKey)
		signer, err := provenance.NewSSHFileSigner(keyPath)
		if err != nil {
			return fmt.Errorf("load provenance signing key %s: %w", keyPath, err)
		}
		storePath := paths.ExpandHome(a.cfg.Provenance.Path)
		a.provenanceStore, err = provenance.New(storePath, signer, a.logger)
		if err != nil {
			return fmt.Errorf("init provenance store at %s: %w", storePath, err)
		}
		a.logger.Info("provenance store initialized",
			"path", storePath,
			"public_key", signer.PublicKey(),
		)
	}

	// --- Attachment store ---
	// Content-addressed file storage with SHA-256 deduplication.
	// When configured, channels (Signal, email) store attachments
	// by content hash with a SQLite metadata index.
	if a.cfg.Attachments.StoreDir != "" {
		storeDir := paths.ExpandHome(a.cfg.Attachments.StoreDir)
		attachDbPath := filepath.Join(a.cfg.DataDir, "attachments.db")
		var err error
		a.attachmentStore, err = attachments.NewStore(attachDbPath, storeDir, a.logger)
		if err != nil {
			return fmt.Errorf("init attachment store: %w", err)
		}
		a.onCloseErr("attachments", a.attachmentStore.Close)
		a.logger.Info("attachment store initialized",
			"db", attachDbPath,
			"store_dir", storeDir,
		)
	}

	// --- Vision analyzer ---
	// When both the attachment store and vision config are enabled,
	// images are automatically analyzed on ingest using a vision-capable
	// LLM. Results are cached in the attachment metadata index.
	if a.attachmentStore != nil && a.cfg.Attachments.Vision.Enabled {
		a.visionAnalyzer = attachments.NewAnalyzer(a.attachmentStore, attachments.AnalyzerConfig{
			Client:  a.llmClient,
			Model:   a.cfg.Attachments.Vision.Model,
			Prompt:  a.cfg.Attachments.Vision.Prompt,
			Timeout: a.cfg.Attachments.Vision.ParsedTimeout(),
			Logger:  a.logger,
		})
		a.logger.Info("vision analyzer enabled",
			"model", a.cfg.Attachments.Vision.Model,
			"timeout", a.cfg.Attachments.Vision.ParsedTimeout(),
		)
	}

	// --- Attachment tools ---
	// When the attachment store is configured, the agent can list,
	// search, and describe attachments. Vision analysis is available
	// when the analyzer is also configured.
	if a.attachmentStore != nil {
		attachmentTools := attachments.NewTools(a.attachmentStore, a.visionAnalyzer)
		a.loop.Tools().SetAttachmentTools(attachmentTools)
		a.logger.Info("attachment tools registered")
	}

	// --- File tools ---
	// When a workspace path is configured, the agent can read and write
	// files within that directory. All paths are sandboxed.
	if a.cfg.Workspace.Path != "" {
		fileTools := tools.NewFileTools(a.cfg.Workspace.Path, a.cfg.Workspace.ReadOnlyDirs)
		if s.resolver != nil {
			fileTools.SetResolver(s.resolver)
		}
		a.loop.Tools().SetFileTools(fileTools)

		// Ego file: prefer provenance store path, fall back to workspace.
		if a.provenanceStore != nil {
			a.loop.SetEgoFile(a.provenanceStore.FilePath("ego.md"))
			a.loop.SetProvenanceStore(a.provenanceStore)
			a.logger.Info("ego.md backed by provenance store")
		} else {
			egoPath := filepath.Join(a.cfg.Workspace.Path, "ego.md")
			if s.resolver != nil {
				if resolved, err := s.resolver.Resolve("core:ego.md"); err != nil {
					a.logger.Warn("failed to resolve core:ego.md, using default",
						"error", err,
						"default_path", egoPath,
					)
				} else {
					egoPath = resolved
				}
			}
			a.loop.SetEgoFile(egoPath)
		}
		a.logger.Info("file tools enabled", "workspace", a.cfg.Workspace.Path)
	} else {
		a.logger.Info("file tools disabled (no workspace path configured)")
	}

	// --- Temp file store ---
	// Provides create_temp_file tool for orchestrator-delegate data passing.
	// Files are stored in the workspace's .tmp subdirectory and cleaned up
	// when conversations end. Requires both workspace and opstate.
	if a.cfg.Workspace.Path != "" {
		tempFileStore := tools.NewTempFileStore(
			filepath.Join(a.cfg.Workspace.Path, ".tmp"),
			a.opStore,
			a.logger,
		)
		a.loop.Tools().SetTempFileStore(tempFileStore)
		a.logger.Info("temp file store enabled",
			"base_dir", filepath.Join(a.cfg.Workspace.Path, ".tmp"),
		)
	}

	// --- Universal content resolution ---
	// Wire prefix-to-content resolution into the tool registry so that bare
	// prefix references (temp:LABEL, kb:file.md, etc.) in any tool's string
	// arguments are automatically replaced with file content before the
	// handler runs. File tools opt out via SkipContentResolve (they need
	// the path, not the content).
	cr := tools.NewContentResolver(s.resolver, a.loop.Tools().TempFileStore(), a.logger)
	if cr != nil {
		a.loop.Tools().SetContentResolver(cr)
		a.logger.Info("content resolver enabled for tool arguments")
	}

	// --- Usage recording ---
	// Wire persistent token usage recording into the agent loop and
	// register the cost_summary tool so the agent can query its own spend.
	a.loop.SetUsageRecorder(a.usageStore, a.cfg.Pricing)
	a.loop.Tools().SetUsageStore(a.usageStore)

	// --- Log index query ---
	// Expose the structured log index so the agent can query its own
	// logs for self-diagnostics and forensics.
	if a.indexDB != nil {
		a.loop.Tools().SetLogIndexDB(a.indexDB)
	}

	// --- Shell exec ---
	// Optional and disabled by default. When enabled, the agent can
	// execute shell commands on the host, subject to allow/deny lists.
	if a.cfg.ShellExec.Enabled {
		shellCfg := tools.ShellExecConfig{
			Enabled:        true,
			WorkingDir:     a.cfg.ShellExec.WorkingDir,
			AllowedCmds:    a.cfg.ShellExec.AllowedPrefixes,
			DeniedCmds:     a.cfg.ShellExec.DeniedPatterns,
			DefaultTimeout: time.Duration(a.cfg.ShellExec.DefaultTimeoutSec) * time.Second,
		}
		if len(shellCfg.DeniedCmds) == 0 {
			shellCfg.DeniedCmds = tools.DefaultShellExecConfig().DeniedCmds
		}
		shellExec := tools.NewShellExec(shellCfg)
		a.loop.Tools().SetShellExec(shellExec)
		a.logger.Info("shell exec enabled", "working_dir", a.cfg.ShellExec.WorkingDir)
	} else {
		a.logger.Info("shell exec disabled")
	}

	// --- Web Search ---
	// Optional web search tool. Supports multiple providers; the first
	// configured provider becomes the default if none is specified.
	if a.cfg.Search.Configured() {
		primary := a.cfg.Search.Default
		mgr := search.NewManager(primary)

		if a.cfg.Search.SearXNG.Configured() {
			mgr.Register(search.NewSearXNG(a.cfg.Search.SearXNG.URL))
			if primary == "" {
				primary = "searxng"
			}
		}
		if a.cfg.Search.Brave.Configured() {
			mgr.Register(search.NewBrave(a.cfg.Search.Brave.APIKey))
			if primary == "" {
				primary = "brave"
			}
		}

		// Re-create manager with resolved primary if it was empty.
		if a.cfg.Search.Default == "" && primary != "" {
			mgr = search.NewManager(primary)
			if a.cfg.Search.SearXNG.Configured() {
				mgr.Register(search.NewSearXNG(a.cfg.Search.SearXNG.URL))
			}
			if a.cfg.Search.Brave.Configured() {
				mgr.Register(search.NewBrave(a.cfg.Search.Brave.APIKey))
			}
		}

		a.loop.Tools().SetSearchManager(mgr)
		a.logger.Info("web search enabled", "primary", primary, "providers", mgr.Providers())
	} else {
		a.logger.Warn("web search disabled (no providers configured)")
	}

	// --- Web Fetch ---
	// Always available — no configuration needed. Fetches web pages and
	// extracts readable text content.
	a.loop.Tools().SetFetcher(search.NewFetcher())

	// --- Media transcript ---
	// Wraps yt-dlp for on-demand transcript retrieval from YouTube,
	// Vimeo, podcasts, and other supported sources.
	ytdlpPath := a.cfg.Media.YtDlpPath
	if ytdlpPath == "" {
		ytdlpPath, _ = exec.LookPath("yt-dlp")
	}
	if ytdlpPath != "" {
		mc := media.New(media.Config{
			YtDlpPath:          ytdlpPath,
			CookiesFile:        a.cfg.Media.CookiesFile,
			CookiesFromBrowser: a.cfg.Media.CookiesFromBrowser,
			SubtitleLanguage:   a.cfg.Media.SubtitleLanguage,
			MaxTranscriptChars: a.cfg.Media.MaxTranscriptChars,
			WhisperModel:       a.cfg.Media.WhisperModel,
			TranscriptDir:      a.cfg.Media.TranscriptDir,
			OllamaURL:          a.cfg.Models.OllamaURL,
		}, a.logger)

		// Wire up LLM summarization for map-reduce transcript processing.
		// Uses a local model via router for chunk summarization.
		mc.SetSummarizer(func(ctx context.Context, prompt string) (string, error) {
			hints := map[string]string{
				router.HintMission:      "background",
				router.HintLocalOnly:    "true",
				router.HintQualityFloor: "3",
				router.HintPreferSpeed:  "true",
			}
			if a.cfg.Media.SummarizeModel != "" {
				hints[router.HintModelPreference] = a.cfg.Media.SummarizeModel
			}
			model, _ := a.rtr.Route(ctx, router.Request{
				Query:    "transcript summarization",
				Priority: router.PriorityBackground,
				Hints:    hints,
			})
			msgs := []llm.Message{{Role: "user", Content: prompt}}
			resp, err := a.llmClient.Chat(ctx, model, msgs, nil)
			if err != nil {
				return "", err
			}
			return resp.Message.Content, nil
		})

		a.loop.Tools().SetMediaClient(mc)
		a.logger.Info("media_transcript enabled", "yt_dlp", ytdlpPath)
	} else {
		a.logger.Warn("media_transcript disabled (yt-dlp not found)")
	}

	// --- Media feed tools ---
	// Feed management tools (media_follow, media_unfollow, media_feeds)
	// are always registered so the agent can manage feeds. Feed polling
	// is a separate concern controlled by FeedCheckInterval.
	feedTools := media.NewFeedTools(a.opStore, a.logger, a.cfg.Media.MaxFeeds)
	a.loop.Tools().SetMediaFeedTools(feedTools)

	// --- Media analysis tools ---
	// The media_save_analysis tool lets the agent persist structured
	// analysis to an Obsidian-compatible vault and track engagement.
	// It requires either a per-feed output_path or the global default.
	// If the engagement store fails to open, the tool is still registered
	// without engagement tracking (vault writes still work).
	a.mediaStore, err = media.NewMediaStore(a.cfg.Media.Analysis.DatabasePath, a.logger)
	if err != nil {
		a.logger.Warn("media engagement store unavailable; analysis will persist to vault only", "error", err)
	} else if a.mediaStore != nil {
		a.onCloseErr("media", a.mediaStore.Close)
	}
	vaultWriter := media.NewVaultWriter(a.logger)
	analysisTools := media.NewAnalysisTools(
		a.opStore, a.mediaStore, vaultWriter,
		a.cfg.Media.Analysis.DefaultOutputPath, a.logger,
	)
	a.loop.Tools().SetMediaAnalysisTools(analysisTools)

	// --- Media feed polling ---
	// Periodic RSS/Atom check for new entries via the loop infrastructure.
	// The handler checks feeds against high-water marks and dispatches an
	// agent conversation only when new content is detected.
	if a.cfg.Media.FeedCheckInterval > 0 {
		feedPoller := media.NewFeedPoller(a.opStore, a.logger)
		pollInterval := time.Duration(a.cfg.Media.FeedCheckInterval) * time.Second
		loopCfg := looppkg.Config{
			Name:         "media-feed-poller",
			SleepMin:     pollInterval,
			SleepMax:     pollInterval,
			SleepDefault: pollInterval,
			Jitter:       looppkg.Float64Ptr(0),
			Handler:      mediaFeedHandler(feedPoller, a.loop, a.logger),
			Metadata: map[string]string{
				"subsystem": "media",
			},
		}
		loopDeps := looppkg.Deps{
			Logger:   a.logger,
			EventBus: a.eventBus,
		}
		a.deferWorker("media-feed-poller", func(ctx context.Context) error {
			if _, err := a.loopRegistry.SpawnLoop(ctx, loopCfg, loopDeps); err != nil {
				return fmt.Errorf("spawn media feed poller loop: %w", err)
			}
			return nil
		})

		a.logger.Info("media feed polling enabled",
			"interval", pollInterval,
			"max_feeds", a.cfg.Media.MaxFeeds,
		)
	}

	// --- Archive tools ---
	// Gives the agent the ability to search and recall past conversations.
	a.loop.Tools().SetArchiveStore(a.archiveStore)
	a.loop.Tools().SetConversationResetter(a.loop)
	a.loop.Tools().SetSessionManager(a.loop)

	// --- Embeddings ---
	// Optional semantic search over fact and contact stores. When enabled,
	// records are indexed with vector embeddings generated by a local model.
	// The client is declared here so it's available to context providers below.
	if a.cfg.Embeddings.Enabled {
		embClient := knowledge.New(knowledge.Config{
			BaseURL: a.cfg.Embeddings.BaseURL,
			Model:   a.cfg.Embeddings.Model,
		})
		factTools.SetEmbeddingClient(embClient)
		contactTools.SetEmbeddingClient(embClient)
		s.embClient = embClient
		a.logger.Info("embeddings enabled", "model", a.cfg.Embeddings.Model, "url", a.cfg.Embeddings.BaseURL)
	}

	// --- MCP servers ---
	// Connect to configured MCP servers and bridge their tools into the
	// registry. This must happen before delegate executor creation so
	// delegates have access to MCP tools.
	for _, serverCfg := range a.cfg.MCP.Servers {
		var transport mcp.Transport
		switch serverCfg.Transport {
		case "stdio":
			transport = mcp.NewStdioTransport(mcp.StdioConfig{
				Command: serverCfg.Command,
				Args:    serverCfg.Args,
				Env:     serverCfg.Env,
				Logger:  a.logger,
			})
		case "http":
			transport = mcp.NewHTTPTransport(mcp.HTTPConfig{
				URL:     serverCfg.URL,
				Headers: serverCfg.Headers,
				Logger:  a.logger,
			})
		}

		client := mcp.NewClient(serverCfg.Name, transport, a.logger)

		initCtx, initCancel := context.WithTimeout(s.ctx, 30*time.Second)
		err := client.Initialize(initCtx)
		initCancel()
		if err != nil {
			a.logger.Error("MCP server initialization failed",
				"server", serverCfg.Name,
				"error", err,
			)
			client.Close()
			continue
		}

		bridgeCtx, bridgeCancel := context.WithTimeout(s.ctx, 30*time.Second)
		count, err := mcp.BridgeTools(
			bridgeCtx,
			client, serverCfg.Name, a.loop.Tools(),
			serverCfg.IncludeTools, serverCfg.ExcludeTools,
			a.logger,
		)
		bridgeCancel()
		if err != nil {
			a.logger.Error("MCP tool bridge failed",
				"server", serverCfg.Name,
				"error", err,
			)
			client.Close()
			continue
		}

		a.mcpClients = append(a.mcpClients, client)
		mcpName := serverCfg.Name // capture for closure
		a.onCloseErr("mcp-"+mcpName, client.Close)

		a.connMgr.Watch(s.ctx, connwatch.WatcherConfig{
			Name:    "mcp-" + serverCfg.Name,
			Probe:   func(pCtx context.Context) error { return client.Ping(pCtx) },
			Backoff: connwatch.DefaultBackoffConfig(),
			Logger:  a.logger,
		})

		a.logger.Info("MCP server connected",
			"server", serverCfg.Name,
			"tools", count,
		)
	}

	// --- Signal message bridge ---
	// Launches a native signal-cli jsonRpc subprocess and receives
	// messages event-driven, routing them through the agent loop.
	// Deferred to StartWorkers because Start() spawns a subprocess and
	// the entire tool/bridge/notification wiring depends on it running.
	//
	// deferredTools tracks tool names that will be registered by deferred
	// workers. The capability-tag validation in initDelegation skips these
	// names so it doesn't emit misleading "unregistered tool" warnings for
	// tools that are simply not yet started.
	s.deferredTools = make(map[string]bool)
	if a.cfg.Signal.Configured() {
		s.deferredTools["signal_send_message"] = true
		s.deferredTools["signal_send_reaction"] = true

		signalArgs := append([]string{"-a", a.cfg.Signal.Account, "jsonRpc"}, a.cfg.Signal.Args...)
		signalClient := sigcli.NewClient(a.cfg.Signal.Command, signalArgs, a.logger)

		a.deferWorker("signal", func(ctx context.Context) error {
			if err := signalClient.Start(ctx); err != nil {
				a.logger.Error("signal-cli start failed", "error", err)
				return nil // non-fatal: system works without Signal
			}
			a.signalClient = signalClient
			a.onCloseErr("signal", signalClient.Close)

			// Register signal_send_message tool so the agent can
			// send messages during its tool loop.
			a.loop.Tools().Register(&tools.Tool{
				Name:        "signal_send_message",
				Description: "Send a Signal message to a phone number. Use this to reply to the user's Signal message or initiate a new Signal conversation.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"recipient": map[string]any{
							"type":        "string",
							"description": "Phone number including country code (e.g., +15551234567)",
						},
						"message": map[string]any{
							"type":        "string",
							"description": "Message text to send",
						},
					},
					"required": []string{"recipient", "message"},
				},
				Handler: func(toolCtx context.Context, args map[string]any) (string, error) {
					recipient, _ := args["recipient"].(string)
					message, _ := args["message"].(string)
					if recipient == "" || message == "" {
						return "", fmt.Errorf("recipient and message are required")
					}
					_, err := signalClient.Send(toolCtx, recipient, message)
					if err != nil {
						return "", err
					}
					return fmt.Sprintf("Message sent to %s", recipient), nil
				},
			})

			idleTimeout := time.Duration(a.cfg.Signal.SessionIdleMinutes) * time.Minute
			var signalRotator sigcli.SessionRotator
			if idleTimeout > 0 {
				signalRotator = &signalSessionRotator{
					loop:      a.loop,
					llmClient: a.llmClient,
					router:    a.rtr,
					sender:    &signalChannelSender{client: signalClient},
					archiver:  a.archiveAdapter,
					logger:    a.logger,
				}
			}

			bridge := sigcli.NewBridge(sigcli.BridgeConfig{
				Client:        signalClient,
				Runner:        a.loop,
				Logger:        a.logger,
				RateLimit:     a.cfg.Signal.RateLimitPerMinute,
				HandleTimeout: a.cfg.Signal.HandleTimeout,
				Routing:       a.cfg.Signal.Routing,
				Rotator:       signalRotator,
				IdleTimeout:   idleTimeout,
				Resolver:      &contactPhoneResolver{store: contactStore},
				Attachments: sigcli.AttachmentConfig{
					SourceDir: a.cfg.Signal.AttachmentSourceDir,
					DestDir:   a.cfg.Signal.AttachmentDir,
					MaxSize:   a.cfg.Signal.MaxAttachmentSize,
				},
				AttachmentStore: a.attachmentStore,
				VisionAnalyzer:  a.visionAnalyzer,
				Registry:        a.loopRegistry,
				EventBus:        a.eventBus,
			})
			if err := bridge.Register(ctx); err != nil {
				a.logger.Error("signal bridge registration failed", "error", err)
			}
			a.signalBridge = bridge

			// Register signal_send_reaction tool so the agent can
			// react to Signal messages with emoji.
			a.loop.Tools().Register(&tools.Tool{
				Name:        "signal_send_reaction",
				Description: "React to a Signal message with an emoji. Use this to acknowledge messages or express reactions. The target_timestamp identifies which message to react to — use the [ts:...] value from the message, or \"latest\" to react to the most recent message from the recipient.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"recipient": map[string]any{
							"type":        "string",
							"description": "Phone number including country code (e.g., +15551234567)",
						},
						"emoji": map[string]any{
							"type":        "string",
							"description": "Reaction emoji (e.g., 👍, ❤️, 😂)",
						},
						"target_author": map[string]any{
							"type":        "string",
							"description": "Phone number of the message author to react to",
						},
						"target_timestamp": map[string]any{
							"type":        "string",
							"description": "Timestamp of the message to react to (from [ts:...] tag) as a numeric string, or \"latest\" for the most recent inbound message from the recipient",
						},
					},
					"required": []string{"recipient", "emoji", "target_author", "target_timestamp"},
				},
				Handler: func(toolCtx context.Context, args map[string]any) (string, error) {
					recipient, _ := args["recipient"].(string)
					emoji, _ := args["emoji"].(string)
					targetAuthor, _ := args["target_author"].(string)

					if recipient == "" || emoji == "" || targetAuthor == "" {
						return "", fmt.Errorf("recipient, emoji, and target_author are required")
					}

					var targetTS int64
					switch v := args["target_timestamp"].(type) {
					case string:
						if v == "latest" {
							ts, ok := bridge.LastInboundTimestamp(recipient)
							if !ok {
								return "", fmt.Errorf("no recent inbound message from %s to react to", recipient)
							}
							targetTS = ts
						} else {
							// Accept numeric strings (LLMs often serialize large ints as strings).
							n, err := strconv.ParseInt(v, 10, 64)
							if err != nil {
								return "", fmt.Errorf("target_timestamp must be a numeric string or \"latest\", got %q", v)
							}
							targetTS = n
						}
					case float64:
						targetTS = int64(v)
					default:
						return "", fmt.Errorf("target_timestamp must be a string (numeric or \"latest\")")
					}

					if err := signalClient.SendReaction(toolCtx, recipient, emoji, targetAuthor, targetTS, false); err != nil {
						return "", err
					}
					return fmt.Sprintf("Reacted with %s to message from %s", emoji, targetAuthor), nil
				},
			})

			a.connMgr.Watch(ctx, connwatch.WatcherConfig{
				Name:    "signal",
				Probe:   func(pCtx context.Context) error { return signalClient.Ping(pCtx) },
				Backoff: connwatch.DefaultBackoffConfig(),
				Logger:  a.logger,
			})

			// Register Signal as a notification delivery channel so the
			// notification router can route to Signal when the contact
			// has an active Signal session.
			if a.notifRouter != nil {
				sp := notifications.NewSignalProvider(
					signalClient, contactStore, a.logger,
				)
				sp.SetRecorder(&signalMemoryRecorder{mem: a.mem})
				a.notifRouter.RegisterProvider(sp)
				a.logger.Info("signal notification provider registered")
			}

			a.logger.Info("signal bridge started",
				"command", a.cfg.Signal.Command,
				"account", a.cfg.Signal.Account,
				"rate_limit", a.cfg.Signal.RateLimitPerMinute,
				"session_idle_timeout", idleTimeout,
			)
			return nil
		})
	}

	return nil
}
