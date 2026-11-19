package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// defaultStartWait caps how long Manager.Start waits for first connect
// attempts before registering what is ready.
const defaultStartWait = 30 * time.Second

// ServerConfig is the app-side description of one configured MCP
// server. The app maps config.MCPServerConfig into it; package mcp
// never imports config.
type ServerConfig struct {
	Name           string
	Transport      string // "stdio" | "http"
	Protocol       string // "auto" | "modern" | "legacy"; "" = auto
	Command        string
	Args           []string
	Env            []string // KEY=VALUE, added to Thane's environment
	Dir            string   // absolute after app-side expansion; "" = inherit
	URL            string
	Headers        map[string]string
	ConnectTimeout time.Duration // 0 = 30s
	CallTimeout    time.Duration // 0 = 2m
	Include        []string
	Exclude        []string
	Tags           []string
	ToolOverrides  map[string]ToolOverride
}

// ToolOverride carries operator-supplied metadata for one bridged tool.
type ToolOverride struct {
	Enabled     *bool // true force-includes; false excludes; nil defers to include/exclude
	Tags        []string
	Description string
}

// Manager owns every configured MCP server for the life of the
// process. Each server has a supervisor that connects, negotiates the
// protocol era, lists tools and reconnects in the background. Tools of
// servers ready at Start are registered on the shared registry; tools
// that appear later (a late server, a changed list) reach agent runs
// through Snapshot, the agent.DynamicToolSource overlay: the main loop
// and any delegate whose tags cover them.
type Manager struct {
	logger    *slog.Logger
	servers   []*managedServer // config order
	byName    map[string]*managedServer
	startWait time.Duration

	// Test hooks: build the transport factory for a server, and adjust
	// client timing. nil means real transports and default timing.
	newFactory  func(ServerConfig) transportFactory
	tuneClients func(*clientConfig)

	mu       sync.Mutex
	started  bool
	closed   bool
	static   map[string]staticTool // registered at Start, by Thane name
	reserved map[string]struct{}   // every registry name at the end of Start
	snapshot []*tools.Tool         // sorted; immutable once published
	tagAdds  map[string][]string   // immutable once published
}

// managedServer is one configured server and its runtime pieces.
type managedServer struct {
	cfg    ServerConfig
	logger *slog.Logger
	client *serverClient
	bridge *serverBridge
}

// staticTool remembers a tool registered at Start.
type staticTool struct {
	server *managedServer
	fp     string
}

// NewManager prepares the servers without any I/O. Config validation
// already rejected settings that can never work; what remains is
// cleaned here with a WARN each: reserved or invalid header names are
// dropped (protocol headers are Thane's), env entries without '=' are
// dropped, and a server whose tools carry no capability tag is flagged
// because tag-gated runs will never offer them.
func NewManager(servers []ServerConfig, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	m := &Manager{
		logger:    logger,
		byName:    make(map[string]*managedServer, len(servers)),
		startWait: defaultStartWait,
		static:    make(map[string]staticTool),
	}
	for _, srv := range servers {
		lg := logger.With("mcp_server", srv.Name)
		switch srv.Transport {
		case transportStdio, transportHTTP:
		default:
			lg.Error("mcp: unknown transport; server skipped", "transport", srv.Transport)
			continue
		}
		if _, dup := m.byName[srv.Name]; dup {
			lg.Error("mcp: duplicate server name; later entry skipped")
			continue
		}
		cfg := cleanServerConfig(srv, lg)
		ms := &managedServer{cfg: cfg, logger: lg}
		m.servers = append(m.servers, ms)
		m.byName[cfg.Name] = ms
	}
	return m
}

// cleanServerConfig copies srv (so config is never aliased) and drops
// what can never be sent.
func cleanServerConfig(srv ServerConfig, logger *slog.Logger) ServerConfig {
	cfg := srv
	cfg.Args = append([]string(nil), srv.Args...)
	cfg.Include = append([]string(nil), srv.Include...)
	cfg.Exclude = append([]string(nil), srv.Exclude...)
	cfg.Tags = append([]string(nil), srv.Tags...)
	cfg.ToolOverrides = maps.Clone(srv.ToolOverrides)

	cfg.Env = nil
	for i, kv := range srv.Env {
		if strings.IndexByte(kv, '=') <= 0 {
			// Never log the entry itself: it may be a bare secret.
			logger.Warn("mcp: dropping env entry without KEY=VALUE form", "env_index", i)
			continue
		}
		cfg.Env = append(cfg.Env, kv)
	}

	cfg.Headers = nil
	if len(srv.Headers) > 0 {
		cfg.Headers = make(map[string]string, len(srv.Headers))
		for _, h := range httpOperatorHeaders(srv.Headers, logger) {
			cfg.Headers[h.name] = h.value
		}
	}

	tagged := len(cleanTags(srv.Tags)) > 0
	for _, o := range srv.ToolOverrides {
		tagged = tagged || len(cleanTags(o.Tags)) > 0
	}
	if !tagged {
		logger.Warn("mcp: server has no tags; its tools will be invisible under capability-tag gating. Add tags: to the server (or to tools: entries)")
	}
	return cfg
}

// factoryFor builds the transport factory for one server.
func (m *Manager) factoryFor(cfg ServerConfig, logger *slog.Logger) transportFactory {
	if m.newFactory != nil {
		return m.newFactory(cfg)
	}
	if cfg.Transport == transportHTTP {
		hc := httpConfig{url: cfg.URL, headers: cfg.Headers, logger: logger}
		return func(ctx context.Context, hooks transportHooks) (Transport, error) {
			t, err := newHTTPTransport(ctx, hc, hooks)
			if err != nil {
				return nil, err
			}
			return t, nil
		}
	}
	// One exitWarned for all of this server's processes: a server that
	// keeps failing at start-up logs its exit at WARN once per run.
	sc := stdioConfig{command: cfg.Command, args: cfg.Args, env: cfg.Env, dir: cfg.Dir, logger: logger,
		exitWarned: new(atomic.Bool)}
	return func(ctx context.Context, hooks transportHooks) (Transport, error) {
		t, err := newStdioTransport(ctx, sc, hooks)
		if err != nil {
			return nil, err
		}
		return t, nil
	}
}

// Start connects every server in parallel, waits until each first
// attempt has finished, the boot cap has elapsed or ctx is done, then
// registers the tools of servers that have a tool list on reg, serially,
// in config order and then sorted by raw tool name. reg is never written
// after Start returns; later changes reach agent runs through Snapshot.
// Call Start once.
//
// The supervisors keep ctx's values but not its cancellation: only
// Close stops them. Shutdown cancels the app ctx before draining
// in-flight requests, and those requests may still be waiting on MCP
// tool calls.
func (m *Manager) Start(ctx context.Context, reg *tools.Registry) {
	life := context.WithoutCancel(ctx)
	for _, s := range m.servers {
		cc := clientConfig{
			name:           s.cfg.Name,
			transport:      s.cfg.Transport,
			protocol:       s.cfg.Protocol,
			connectTimeout: s.cfg.ConnectTimeout,
			callTimeout:    s.cfg.CallTimeout,
			onToolsChanged: m.rebuild,
			logger:         m.logger,
		}
		if m.tuneClients != nil {
			m.tuneClients(&cc)
		}
		s.client = newServerClient(life, cc, m.factoryFor(s.cfg, s.logger))
		s.bridge = newServerBridge(s.cfg, s.client, s.logger)
	}
	for _, s := range m.servers {
		s.client.start()
	}
	m.awaitFirstAttempts(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	var ready, pending []string
	registered := 0
	for _, s := range m.servers {
		defs := s.client.tools()
		if defs == nil {
			pending = append(pending, s.cfg.Name)
			continue
		}
		ready = append(ready, s.cfg.Name)
		for _, bt := range s.bridge.bridge(defs) {
			if reg.Get(bt.tool.Name) != nil {
				m.logCollision(s, bt)
				continue
			}
			reg.Register(bt.tool)
			m.static[bt.tool.Name] = staticTool{server: s, fp: bt.fp}
			registered++
		}
	}
	m.reserved = make(map[string]struct{})
	for _, name := range reg.AllToolNames() {
		m.reserved[name] = struct{}{}
	}
	m.started = true
	m.rebuildLocked()
	m.logger.Info("mcp: servers started",
		"servers", len(m.servers), "ready", ready, "pending", pending,
		"registered_tools", registered, "late_tools", len(m.snapshot))
}

// awaitFirstAttempts waits for every first attempt, the boot cap, or
// ctx, whichever comes first.
func (m *Manager) awaitFirstAttempts(ctx context.Context) {
	timer := time.NewTimer(m.startWait)
	defer timer.Stop()
	for _, s := range m.servers {
		select {
		case <-s.client.firstAttempt():
		case <-timer.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// logCollision warns, once per tool, that a bridged name is already
// taken by a native tool or an earlier server's tool.
func (m *Manager) logCollision(s *managedServer, bt bridgedTool) {
	s.bridge.logOnce(slog.LevelWarn, "collide:"+bt.raw,
		"mcp: tool name already taken by another tool; this server's tool is skipped",
		"tool", bt.tool.Name, "raw_tool", logText(bt.raw))
}

// Probe errors. Their text is fixed because it is shown in health
// views (connwatch, system_health, the metacognitive panel): it never
// carries a URL, host or server-authored text. The full error is
// logged at Debug.
var (
	errProbeUnreachable  = errors.New("mcp: not connected: unreachable")
	errProbeAuth         = errors.New("mcp: not connected: credentials refused")
	errProbeIncompatible = errors.New("mcp: not connected: protocol incompatible")
	errProbeTimeout      = errors.New("mcp: health check timed out")
	errProbeNotConnected = errors.New("mcp: not connected")
)

// Probe is the connwatch health check for one server: an era-correct
// ping or discover while connected, the last connect error otherwise.
// A failed probe never tears a connection down. A failure is reported as
// one of the fixed errProbe* errors.
func (m *Manager) Probe(ctx context.Context, server string) error {
	s, ok := m.byName[server]
	if !ok || s.client == nil {
		return fmt.Errorf("mcp: no started server named %q", server)
	}
	err := s.client.probe(ctx)
	if err == nil {
		return nil
	}
	s.logger.Debug("mcp: health check failed", append([]any{"error", err}, httpStatusLogArgs(err)...)...)
	return probeError(err)
}

// probeError maps a probe failure to its fixed health-view error.
func probeError(err error) error {
	var statusErr *httpStatusError
	switch {
	case errors.As(err, &statusErr) && classifyStatus(statusErr.Status) == statusAuth:
		return errProbeAuth
	case errors.Is(err, errIncompatible), errors.Is(err, errProtocol):
		return errProbeIncompatible
	case errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, errNotReady):
		return errProbeTimeout
	case errors.Is(err, errUnreachable), errors.Is(err, errConnectionLost),
		errors.Is(err, context.DeadlineExceeded):
		return errProbeUnreachable
	default:
		return errProbeNotConnected
	}
}

// Close stops every server in parallel, bounded by ctx. Idempotent.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	errs := make([]error, len(m.servers))
	var wg sync.WaitGroup
	for i, s := range m.servers {
		if s.client == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = s.client.close(ctx)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
