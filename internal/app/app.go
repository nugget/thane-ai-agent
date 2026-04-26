// Package app contains the application-level wiring for the Thane server.
// It owns the full initialization sequence (databases, clients, tools,
// background loops) and the server lifecycle (start, graceful shutdown).
//
// The primary entry point is [New], which constructs an [App] from a
// validated [config.Config]. [App.Serve] then blocks until a shutdown
// signal is received and all in-flight requests drain.
package app

import (
	"database/sql"
	"io"
	"log/slog"
	"sync"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	sigcli "github.com/nugget/thane-ai-agent/internal/channels/messaging/signal"
	"github.com/nugget/thane-ai-agent/internal/channels/mqtt"
	"github.com/nugget/thane-ai-agent/internal/channels/notifications"
	"github.com/nugget/thane-ai-agent/internal/connwatch"
	"github.com/nugget/thane-ai-agent/internal/integrations/companion"
	"github.com/nugget/thane-ai-agent/internal/integrations/forge"
	"github.com/nugget/thane-ai-agent/internal/integrations/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/integrations/mcp"
	"github.com/nugget/thane-ai-agent/internal/integrations/media"
	"github.com/nugget/thane-ai-agent/internal/integrations/unifi"
	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	modelproviders "github.com/nugget/thane-ai-agent/internal/model/fleet/providers"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/model/toolcatalog"
	"github.com/nugget/thane-ai-agent/internal/platform/checkpoint"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
	"github.com/nugget/thane-ai-agent/internal/platform/scheduler"
	"github.com/nugget/thane-ai-agent/internal/platform/telemetry"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/runtime/delegate"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/runtime/metacognitive"
	"github.com/nugget/thane-ai-agent/internal/server/api"
	cdav "github.com/nugget/thane-ai-agent/internal/server/carddav"
	"github.com/nugget/thane-ai-agent/internal/state/attachments"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// Logger returns the application's configured logger. Callers that
// construct the logger before calling [New] (e.g. cmd/thane/runServe) can
// call this after [New] returns to obtain the fully-configured logger
// (file handler, index handler, level, format) for subsequent log lines.
func (a *App) Logger() *slog.Logger { return a.logger }

// capSurfaceGetter returns a closure that reads the current capability
// surface at call time. Adapters use this instead of capturing the
// slice at construction time because the surface is finalized in a
// late init phase ([finalizeCapabilityTags]) that runs after most
// adapters have already been wired.
func (a *App) capSurfaceGetter() func() []toolcatalog.CapabilitySurface {
	return func() []toolcatalog.CapabilitySurface { return a.capSurface }
}

// App holds all long-lived application state for the Thane server. It is
// constructed by [New] and run by [App.Serve]. Fields map directly to the
// subsystems initialized during startup.
type App struct {
	cfg    *config.Config
	logger *slog.Logger
	stdout io.Writer

	// LLM clients
	llmClient             llm.Client
	ollamaClients         map[string]*modelproviders.OllamaClient
	resourceHealthClients map[string]fleet.ResourceHealthClient
	modelRuntime          *fleet.Runtime
	modelCatalog          *fleet.Catalog
	modelRegistry         *fleet.Registry

	// Core subsystems
	mem                       *memory.SQLiteStore
	archiveStore              *memory.ArchiveStore
	archiveAdapter            *memory.ArchiveAdapter
	wmStore                   *memory.WorkingMemoryStore
	factStore                 *knowledge.Store
	documentStore             *documents.Store
	contactStore              *contacts.Store
	opStore                   *opstate.Store
	modelPolicyStore          *modelPolicyStore
	modelResourcePolicyStore  *modelResourcePolicyStore
	modelExperienceStore      *modelExperienceStore
	loopDefinitionStore       *loopDefinitionStore
	loopDefinitionPolicyStore *loopDefinitionPolicyStore
	usageStore                *usage.Store
	schedStore                *scheduler.Store
	sched                     *scheduler.Scheduler

	// Agent loop and router
	loop *agent.Loop
	rtr  *router.Router
	// Shared capability surface used by prompt renderers and dashboard views.
	capSurface []toolcatalog.CapabilitySurface

	// Compaction and summarization
	compactor     *memory.Compactor
	summaryWorker *memory.SummarizerWorker

	// External service clients
	ha   *homeassistant.Client
	haWS *homeassistant.WSClient

	// Companion app registry
	companionRegistry *companion.Registry

	// Connection health
	connMgr *connwatch.Manager

	// Delegated execution
	delegateExec *delegate.Executor

	// Servers
	server        *api.Server
	ollamaServer  *api.OllamaServer
	carddavServer *cdav.Server

	// MQTT
	mqttPub        *mqtt.Publisher
	mqttInstanceID string

	// Notifications
	notifSender             *notifications.Sender
	notifRecords            *notifications.RecordStore
	notifRouter             *notifications.NotificationRouter
	notifCallbackDispatcher *notifications.CallbackDispatcher

	// Forge integration
	forgeMgr *forge.Manager

	// MCP clients (closed on shutdown)
	mcpClients []*mcp.Client

	// Logging infrastructure
	indexDB             *sql.DB
	indexHandler        *logging.IndexHandler
	datasetWriter       *logging.DatasetWriter
	liveRequestStore    *logging.LiveRequestStore
	liveRequestRecorder logging.RequestRecordFunc
	requestRecorder     logging.RequestRecordFunc
	contentWriter       *logging.ContentWriter

	// Attachment and vision
	attachmentStore *attachments.Store
	visionAnalyzer  *attachments.Analyzer

	// Media
	mediaStore *media.MediaStore

	// Service loop runtimes hydrated into built-in loops-ng definitions.
	unifiPoller        *unifi.Poller
	haStateWatcher     *homeassistant.StateWatcher
	emailPoller        *email.Poller
	mediaFeedPoller    *media.FeedPoller
	telemetryPublisher *telemetry.Publisher

	// Checkpointing
	checkpointer *checkpoint.Checkpointer

	// Loop registry
	loopRegistry           *looppkg.Registry
	loopDefinitionRegistry *looppkg.DefinitionRegistry
	loopDefinitionRuntime  *loopDefinitionRuntime
	loopCompletionDelivery *detachedLoopCompletionDispatcher

	// Metacognitive config (stored for loop-definition hydration)
	metacogCfg *metacognitive.Config

	// Event bus
	eventBus *events.Bus

	// Inter-component message bus
	messageBus *messages.Bus

	// Email manager (for Close on shutdown)
	emailMgr *email.Manager

	// Signal bridge
	signalClient *sigcli.Client
	signalBridge *sigcli.Bridge

	// Deferred worker starts, populated by New(), executed by StartWorkers().
	pendingWorkers []pendingWorker

	// closers is a LIFO stack of cleanup functions registered by New()
	// (resource closers) and StartWorkers() (worker stop functions).
	// shutdown() drains it in reverse order after Phase 1 cross-cutting
	// stops (loopRegistry, connMgr).
	closers []closer

	// closeOnce ensures shutdown runs exactly once across Close and Serve.
	closeOnce sync.Once
}
