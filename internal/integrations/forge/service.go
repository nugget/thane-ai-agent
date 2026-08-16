package forge

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// ServiceDependencies supplies the runtime collaborators used by [Service].
// The operational state store persists repository subscriptions, while the
// message bus and loop resolver deliver and validate subscription wakes.
type ServiceDependencies struct {
	// State persists repository subscription configuration and cursors.
	State *opstate.Store `json:"-"`

	// MessageBus delivers repository events to subscribed loops.
	MessageBus *messages.Bus `json:"-"`

	// LoopResolver validates wake targets before subscriptions are persisted.
	LoopResolver messages.LoopResolver `json:"-"`

	// Logger receives forge account, subscription, and provider diagnostics.
	Logger *slog.Logger `json:"-"`

	// WorkspacePath anchors repository subscription checkouts. The model names
	// a root; the service derives its physical path beneath this workspace.
	WorkspacePath string `json:"-"`

	// RootResolver is the shared named-root registry used by file tools.
	RootResolver *paths.Resolver `json:"-"`
}

// Service owns the configured forge runtime: account resolution, model-facing
// tools and context, repository subscriptions, and their poller. Application
// code depends on this facade instead of assembling those pieces separately.
type Service struct {
	manager         *Manager
	logger          *slog.Logger
	tools           *Tools
	contextProvider *ContextProvider
	poller          *SubscriptionPoller
	subscriptions   *SubscriptionStore
	workspacePath   string
	rootResolver    *paths.Resolver
}

// NewService creates a complete forge runtime from configuration and shared
// application dependencies.
func NewService(cfg Config, deps ServiceDependencies) (*Service, error) {
	if deps.State == nil {
		return nil, fmt.Errorf("forge service requires operational state")
	}
	if deps.MessageBus == nil {
		return nil, fmt.Errorf("forge service requires message bus")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}

	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	manager, err := NewManager(cfg, deps.Logger)
	if err != nil {
		return nil, err
	}

	opLog := NewOperationLog()
	subscriptions := NewSubscriptionStore(deps.State, deps.Logger, cfg.MaxSubscriptions)

	service := &Service{
		manager:       manager,
		logger:        deps.Logger,
		subscriptions: subscriptions,
		workspacePath: deps.WorkspacePath,
		rootResolver:  deps.RootResolver,
	}
	if err := service.registerPersistedRepositoryRoots(); err != nil {
		return nil, err
	}
	service.tools = newTools(service, opLog, deps.Logger, subscriptions)
	service.tools.SetLoopResolver(deps.LoopResolver)
	service.contextProvider = newContextProvider(service, opLog)
	if cfg.SubscriptionCheckInterval > 0 {
		service.poller = NewSubscriptionPoller(manager, subscriptions, deps.MessageBus, deps.Logger)
	}
	return service, nil
}

// ToolProvider returns the forge-owned model tool provider.
func (s *Service) ToolProvider() *Tools {
	if s == nil {
		return nil
	}
	return s.tools
}

// ContextProvider returns the tag-gated forge context provider.
func (s *Service) ContextProvider() *ContextProvider {
	if s == nil {
		return nil
	}
	return s.contextProvider
}

// ResolveAccount selects the configured forge account used by a request while
// enforcing any forge account binding carried by ctx. An omitted account uses
// the binding when present and the primary configured account otherwise.
func (s *Service) ResolveAccount(ctx context.Context, requested string) (ResolvedAccount, error) {
	if s == nil || s.manager == nil {
		return ResolvedAccount{}, fmt.Errorf("forge service is not configured")
	}

	bound := boundAccount(ctx)
	if bound != "" {
		if requested != "" && requested != bound {
			if s.logger != nil {
				s.logger.Warn("forge account request refused by binding",
					"requested_account", requested,
					"bound_account", bound,
					"loop_id", looppkg.LoopIDFromContext(ctx))
			}
			return ResolvedAccount{}, fmt.Errorf("forge account %q is not available here: this loop is bound to account %q, and the binding is part of its definition rather than something a tool call can change. Retry with account=%q or omit the argument, and if the work genuinely requires %q, say so instead of routing around it",
				requested, bound, bound, requested)
		}
		requested = bound
	}
	return s.manager.ResolveAccount(requested)
}

// boundAccount returns the forge account this caller is scoped to, or an empty
// string when the caller is unbound.
func boundAccount(ctx context.Context) string {
	return looppkg.BindingFromContext(ctx, looppkg.BindingForgeAccount)
}

// boundRepositoryRoot returns the repository root this caller is scoped to,
// or an empty string when the caller is unbound.
func boundRepositoryRoot(ctx context.Context) string {
	return looppkg.BindingFromContext(ctx, looppkg.BindingRepositoryRoot)
}

// AccountsInConfigOrder returns the configured accounts in declaration
// order, primary first. It exists so callers that render or enumerate
// accounts do not reach through the service into the manager's
// internals: consolidating account ownership behind this type is only
// worth doing if the type is actually the way through.
func (s *Service) AccountsInConfigOrder() []AccountConfig {
	if s == nil || s.manager == nil {
		return nil
	}
	out := make([]AccountConfig, 0, len(s.manager.order))
	for _, name := range s.manager.order {
		out = append(out, s.manager.configs[name])
	}
	return out
}

// SubscriptionPollingEnabled reports whether repository polling is enabled by
// configuration. Dynamic loop definitions must honor this policy rather than
// recreating a poller when subscription_check_interval is zero.
func (s *Service) SubscriptionPollingEnabled() bool {
	return s != nil && s.poller != nil
}

// CheckSubscriptions polls followed repositories and delivers any resulting
// event wakes. Repository failures remain isolated by [SubscriptionPoller].
func (s *Service) CheckSubscriptions(ctx context.Context) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("forge service is not configured")
	}
	if s.poller == nil {
		return 0, fmt.Errorf("forge subscription polling is disabled")
	}
	return s.poller.CheckSubscriptions(ctx)
}
