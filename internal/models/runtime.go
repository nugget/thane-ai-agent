package models

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/llm"
	modelproviders "github.com/nugget/thane-ai-agent/internal/models/providers"
)

// RefreshResult describes the outcome of a runtime inventory refresh.
type RefreshResult struct {
	Changed  bool
	Snapshot *RegistrySnapshot
}

// ExplicitModelPrepareResult describes the provider-side outcome of
// readying an explicit deployment for immediate use.
type ExplicitModelPrepareResult struct {
	Changed  bool
	Resolved string
	Instance string
	Snapshot *RegistrySnapshot
}

// Runtime owns the long-lived model registry plus the swappable routed
// llm.Client built from it.
type Runtime struct {
	registry *Registry
	bundle   *ClientBundle
	client   *llm.DynamicClient

	refreshMu sync.Mutex
}

// NewRuntime builds the initial registry, performs a first inventory
// refresh, and constructs the swappable routed client.
func NewRuntime(ctx context.Context, base *Catalog, cfg *config.Config, logger *slog.Logger) (*Runtime, error) {
	if base == nil {
		return nil, fmt.Errorf("nil base catalog")
	}
	if logger == nil {
		logger = slog.Default()
	}

	bundle, err := BuildClients(base, cfg, logger)
	if err != nil {
		return nil, err
	}
	registry, err := NewRegistry(base)
	if err != nil {
		return nil, err
	}
	client, err := bundle.BuildRoutedClient(registry.Catalog())
	if err != nil {
		return nil, err
	}

	rt := &Runtime{
		registry: registry,
		bundle:   bundle,
		client:   llm.NewDynamicClient(client),
	}
	if _, err := rt.Refresh(ctx); err != nil {
		return nil, err
	}
	return rt, nil
}

// Client returns the swappable llm.Client.
func (r *Runtime) Client() llm.Client {
	if r == nil {
		return nil
	}
	return r.client
}

// Registry returns the long-lived model registry.
func (r *Runtime) Registry() *Registry {
	if r == nil {
		return nil
	}
	return r.registry
}

// OllamaClients returns the stable per-resource Ollama clients used by
// watchers and discovery refresh.
func (r *Runtime) OllamaClients() map[string]*modelproviders.OllamaClient {
	if r == nil || r.bundle == nil {
		return nil
	}
	return r.bundle.OllamaClients
}

// LMStudioClient returns the stable per-resource LM Studio client used
// by runtime discovery and explicit recovery flows.
func (r *Runtime) LMStudioClient(resourceID string) *modelproviders.LMStudioClient {
	if r == nil || r.bundle == nil {
		return nil
	}
	return r.bundle.LMStudioClients[resourceID]
}

// HealthClients returns the stable per-resource health clients used by
// connwatch and inventory refresh triggers.
func (r *Runtime) HealthClients() map[string]ResourceHealthClient {
	if r == nil || r.bundle == nil {
		return nil
	}
	return r.bundle.HealthClients
}

// InventoryClientCount reports how many resource clients participate in
// runtime inventory discovery.
func (r *Runtime) InventoryClientCount() int {
	if r == nil || r.bundle == nil {
		return 0
	}
	return len(r.bundle.OllamaClients) + len(r.bundle.LMStudioClients)
}

// Refresh probes provider inventory, updates the registry overlay, and
// swaps in a new routed client for future requests.
func (r *Runtime) Refresh(ctx context.Context) (*RefreshResult, error) {
	if r == nil {
		return nil, fmt.Errorf("nil runtime")
	}

	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	return r.refreshLocked(ctx)
}

func (r *Runtime) refreshLocked(ctx context.Context) (*RefreshResult, error) {
	if r == nil {
		return nil, fmt.Errorf("nil runtime")
	}

	before := r.registry.Snapshot()
	if err := r.registry.Refresh(ctx, r.bundle); err != nil {
		return nil, err
	}
	nextClient, err := r.bundle.BuildRoutedClient(r.registry.Catalog())
	if err != nil {
		return nil, err
	}
	r.bundle.Client = nextClient
	if err := r.client.Swap(nextClient); err != nil {
		return nil, err
	}

	after := r.registry.Snapshot()
	changed := before == nil || after == nil || before.Generation != after.Generation
	return &RefreshResult{
		Changed:  changed,
		Snapshot: after,
	}, nil
}

// PrepareExplicitModel asks the backing provider to ready an explicit
// deployment for the requested context size, then refreshes the live
// registry snapshot when the provider state changes. Today this is used
// only for LM Studio deployments whose loaded context window is smaller
// than the runner-advertised maximum.
func (r *Runtime) PrepareExplicitModel(ctx context.Context, ref string, contextSize int) (*ExplicitModelPrepareResult, error) {
	if r == nil {
		return nil, fmt.Errorf("nil runtime")
	}

	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	cat := r.registry.Catalog()
	if cat == nil {
		return nil, fmt.Errorf("model registry is not initialized")
	}
	dep, err := cat.ResolveDeploymentRef(ref)
	if err != nil {
		return nil, err
	}
	if !CanExpandLoadedContext(dep, contextSize) {
		return &ExplicitModelPrepareResult{Resolved: dep.ID}, nil
	}

	client := r.bundle.LMStudioClients[dep.ResourceID]
	if client == nil {
		return nil, fmt.Errorf("lmstudio resource %q is unavailable", dep.ResourceID)
	}
	loadResp, err := client.LoadModel(ctx, dep.ModelName, contextSize)
	if err != nil {
		return nil, err
	}

	result, err := r.refreshLocked(ctx)
	if err != nil {
		return nil, err
	}
	return &ExplicitModelPrepareResult{
		Changed:  result.Changed,
		Resolved: dep.ID,
		Instance: loadResp.InstanceID,
		Snapshot: result.Snapshot,
	}, nil
}

// CanExpandLoadedContext reports whether a deployment can plausibly be
// reloaded by its runner to satisfy the requested context size.
func CanExpandLoadedContext(dep Deployment, contextSize int) bool {
	if strings.TrimSpace(dep.Provider) != "lmstudio" {
		return false
	}
	if contextSize <= 0 {
		return false
	}
	if dep.MaxContextWindow <= 0 {
		return false
	}
	if dep.LoadedContextWindow <= 0 {
		return contextSize <= dep.MaxContextWindow
	}
	if dep.MaxContextWindow <= dep.LoadedContextWindow {
		return false
	}
	if contextSize <= dep.LoadedContextWindow {
		return false
	}
	return contextSize <= dep.MaxContextWindow
}
