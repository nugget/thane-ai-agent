package email

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// folderCacheMaxAge is how old a cached folder listing may be before
// the poller refreshes it on its next visit to the account.
const folderCacheMaxAge = 10 * time.Minute

// ServiceDependencies supplies the runtime collaborators a [Service]
// needs. Fields are optional unless stated: a service without a message
// bus polls but cannot dispatch wakes, and one without a contact
// resolver sends without trust gating.
type ServiceDependencies struct {
	// State persists the poller's per-account high-water marks.
	// Required when polling is enabled.
	State *opstate.Store `json:"-"`

	// MessageBus delivers new-mail wake envelopes to the handler loop.
	MessageBus *messages.Bus `json:"-"`

	// Contacts resolves addresses to trust zones for the send gate and
	// the wake metadata. Nil disables trust gating.
	Contacts ContactResolver `json:"-"`

	// WakeTarget overrides the loop that receives new-mail wakes. Nil
	// means [DefaultHandlerLoopName].
	WakeTarget *messages.LoopWakeTarget `json:"-"`

	// Logger receives account, poller, and tool diagnostics. Nil means
	// the default logger.
	Logger *slog.Logger `json:"-"`
}

// Service owns the configured email runtime: account resolution, the
// model-facing tools and context block, the poller, the recent
// operations log, and the folder cache. Application code depends on
// this facade rather than assembling those pieces separately, and
// every account lookup a tool performs goes through
// [Service.ResolveAccount], which is where loop bindings are enforced.
type Service struct {
	manager         *Manager
	logger          *slog.Logger
	tools           *Tools
	contextProvider *ContextProvider
	poller          *Poller
	opLog           *OperationLog

	foldersMu sync.Mutex
	folders   map[string]folderSnapshot
}

// folderSnapshot is one account's most recent successful folder listing.
type folderSnapshot struct {
	Folders []Folder
	At      time.Time
}

// ResolvedAccount is the client and configuration selected for one
// operation. Name is always explicit, including when the caller
// selected the primary account by omitting a name or was routed to a
// bound account.
type ResolvedAccount struct {
	Name   string        `json:"-"`
	Client *Client       `json:"-"`
	Config AccountConfig `json:"-"`
}

// HealthProbe is one account's liveness check for connwatch: the
// account name and a probe bound by the caller's context.
type HealthProbe struct {
	Account string
	Probe   func(ctx context.Context) error
}

// NewService creates the email runtime from configuration and shared
// dependencies. The configuration is defaulted and validated here so a
// caller cannot construct a service around an inconsistent block.
func NewService(cfg Config, deps ServiceDependencies) (*Service, error) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Configured() {
		return nil, fmt.Errorf("email service requires at least one configured account")
	}

	s := &Service{
		manager: NewManager(cfg, deps.Logger),
		logger:  deps.Logger,
		opLog:   NewOperationLog(),
		folders: make(map[string]folderSnapshot),
	}
	s.tools = newTools(s, deps.Contacts, deps.Logger)
	s.contextProvider = newContextProvider(s)

	if cfg.PollingInterval() > 0 {
		if deps.State == nil {
			return nil, fmt.Errorf("email polling is enabled but no operational state store was provided")
		}
		opts := []PollerOption{WithContactResolver(deps.Contacts)}
		if deps.MessageBus != nil {
			opts = append(opts, WithMessageBus(deps.MessageBus))
		}
		if deps.WakeTarget != nil {
			opts = append(opts, WithDefaultWakeLoop(*deps.WakeTarget))
		}
		s.poller = newPoller(s, deps.State, deps.Logger, opts...)
	}
	return s, nil
}

// ToolProvider returns the email-owned model tool provider.
func (s *Service) ToolProvider() *Tools {
	if s == nil {
		return nil
	}
	return s.tools
}

// ContextProvider returns the tag-gated Email Accounts context provider.
func (s *Service) ContextProvider() *ContextProvider {
	if s == nil {
		return nil
	}
	return s.contextProvider
}

// ResolveAccount selects the account an operation runs against while
// enforcing any email_account loop binding carried by ctx: an omitted
// name resolves to the bound account when one is present and to the
// primary account otherwise, and a name that differs from the binding
// is refused with text that names both and the retry. An unknown name
// lists the accounts that exist.
func (s *Service) ResolveAccount(ctx context.Context, requested string) (ResolvedAccount, error) {
	if s == nil || s.manager == nil {
		return ResolvedAccount{}, fmt.Errorf("email service is not configured")
	}
	requested = strings.TrimSpace(requested)

	if bound := boundAccount(ctx); bound != "" {
		if requested != "" && requested != bound {
			s.logger.Warn("email account request refused by binding",
				"requested_account", requested,
				"bound_account", bound,
				"loop_id", looppkg.LoopIDFromContext(ctx))
			return ResolvedAccount{}, fmt.Errorf("email account %q is not available here: this loop is bound to account %q, and the binding is part of its definition rather than something a tool call can change. Retry with account=%q or omit the argument, and if the work genuinely requires %q, say so instead of routing around it",
				requested, bound, bound, requested)
		}
		requested = bound
	}

	name, err := s.manager.resolveName(requested)
	if err != nil {
		return ResolvedAccount{}, err
	}
	return ResolvedAccount{
		Name:   name,
		Client: s.manager.clients[name],
		Config: s.manager.configs[name],
	}, nil
}

// boundAccount returns the email account this caller is scoped to, or
// an empty string when the caller is unbound.
func boundAccount(ctx context.Context) string {
	return looppkg.BindingFromContext(ctx, looppkg.BindingEmailAccount)
}

// AccountsInConfigOrder returns the account configurations in
// declaration order, primary first.
func (s *Service) AccountsInConfigOrder() []AccountConfig {
	if s == nil || s.manager == nil {
		return nil
	}
	return s.manager.AccountsInConfigOrder()
}

// AccountNames returns the configured account names, primary first.
func (s *Service) AccountNames() []string {
	if s == nil || s.manager == nil {
		return nil
	}
	return s.manager.AccountNames()
}

// BccOwner returns the configured audit-copy address, or empty.
func (s *Service) BccOwner() string {
	if s == nil || s.manager == nil {
		return ""
	}
	return s.manager.BccOwner()
}

// HealthProbes returns one liveness probe per account for connwatch.
func (s *Service) HealthProbes() []HealthProbe {
	if s == nil || s.manager == nil {
		return nil
	}
	probes := make([]HealthProbe, 0, len(s.manager.order))
	for _, name := range s.manager.order {
		client := s.manager.clients[name]
		probes = append(probes, HealthProbe{Account: name, Probe: client.Ping})
	}
	return probes
}

// PollingEnabled reports whether new-mail polling is configured.
// Built-in loop definitions honor this rather than assuming a poller
// exists.
func (s *Service) PollingEnabled() bool {
	return s != nil && s.poller != nil
}

// CheckNewMessages runs one poll cycle across every account and returns
// the number of wake events delivered. See [Poller.CheckNewMessages].
func (s *Service) CheckNewMessages(ctx context.Context) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("email service is not configured")
	}
	if s.poller == nil {
		return 0, fmt.Errorf("email polling is disabled")
	}
	return s.poller.CheckNewMessages(ctx)
}

// Close closes every account connection.
func (s *Service) Close() {
	if s == nil || s.manager == nil {
		return
	}
	s.manager.Close()
}

// recordOp appends a successful operation to the recent-operations log.
func (s *Service) recordOp(tool, account, folder, ref string) {
	if s == nil {
		return
	}
	s.opLog.Record(Operation{Tool: tool, Account: account, Folder: folder, Ref: ref})
}

// listFolders lists an account's folders and records the result in the
// cache the context block renders from. Every successful LIST feeds the
// cache — tool calls, the poller's refresh, and the error path that
// lists folders to teach — so the block reflects the freshest listing
// anyone made rather than depending on any one caller.
func (s *Service) listFolders(ctx context.Context, acct ResolvedAccount) ([]Folder, error) {
	folders, err := acct.Client.ListFolders(ctx)
	if err != nil {
		return nil, err
	}
	s.rememberFolders(acct.Name, folders)
	return folders, nil
}

// rememberFolders stores a listing in the cache.
func (s *Service) rememberFolders(account string, folders []Folder) {
	s.foldersMu.Lock()
	s.folders[account] = folderSnapshot{Folders: slices.Clone(folders), At: time.Now()}
	s.foldersMu.Unlock()
}

// cachedFolders returns the most recent listing for an account, or
// ok=false when none has succeeded yet.
func (s *Service) cachedFolders(account string) (folderSnapshot, bool) {
	s.foldersMu.Lock()
	defer s.foldersMu.Unlock()
	snap, ok := s.folders[account]
	return snap, ok
}

// refreshFoldersIfStale re-lists an account's folders when the cache
// has no listing or one older than folderCacheMaxAge. The poller calls
// it on every visit so a handler that only ever wakes from the poller
// still sees folder vocabulary. Failures are logged at Debug: the
// listing is a convenience, and the poll itself must not fail on it.
func (s *Service) refreshFoldersIfStale(ctx context.Context, acct ResolvedAccount) {
	if snap, ok := s.cachedFolders(acct.Name); ok && time.Since(snap.At) < folderCacheMaxAge {
		return
	}
	if _, err := s.listFolders(ctx, acct); err != nil {
		s.logger.Debug("email folder cache refresh failed", "account", acct.Name, "error", err)
	}
}
