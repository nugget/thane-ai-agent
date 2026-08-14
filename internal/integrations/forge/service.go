package forge

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
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
}

// Service owns the configured forge runtime: account resolution, model-facing
// tools and context, repository subscriptions, and their poller. Application
// code depends on this facade instead of assembling those pieces separately.
type Service struct {
	manager         *Manager
	tools           *Tools
	contextProvider *ContextProvider
	poller          *SubscriptionPoller
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
	forgeTools := NewTools(manager, opLog, deps.Logger, subscriptions)
	forgeTools.SetLoopResolver(deps.LoopResolver)

	return &Service{
		manager:         manager,
		tools:           forgeTools,
		contextProvider: NewContextProvider(manager, opLog),
		poller:          NewSubscriptionPoller(manager, subscriptions, deps.MessageBus, deps.Logger),
	}, nil
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

// ResolveAccount selects the configured forge account used by a request.
func (s *Service) ResolveAccount(name string) (ResolvedAccount, error) {
	if s == nil || s.manager == nil {
		return ResolvedAccount{}, fmt.Errorf("forge service is not configured")
	}
	return s.manager.ResolveAccount(name)
}

// CheckSubscriptions polls followed repositories and delivers any resulting
// event wakes. Repository failures remain isolated by [SubscriptionPoller].
func (s *Service) CheckSubscriptions(ctx context.Context) (int, error) {
	if s == nil || s.poller == nil {
		return 0, fmt.Errorf("forge service is not configured")
	}
	return s.poller.CheckSubscriptions(ctx)
}
