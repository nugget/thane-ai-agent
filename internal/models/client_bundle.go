package models

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/llm"
	modelproviders "github.com/nugget/thane-ai-agent/internal/models/providers"
)

// ClientBundle contains the routed LLM client plus provider-specific
// resource clients keyed by resource ID for connection watching and
// inventory discovery.
type ClientBundle struct {
	Client          llm.Client
	ResourceClients map[string]llm.Client
	HealthClients   map[string]ResourceHealthClient
	OllamaClients   map[string]*modelproviders.OllamaClient
	LMStudioClients map[string]*modelproviders.LMStudioClient
}

// ResourceHealthClient is the minimal health/watch surface that app
// wiring needs from one model-provider resource.
type ResourceHealthClient struct {
	Ping          func(ctx context.Context) error
	AttachWatcher func(w llm.ReadyWatcher)
}

// BuildClients constructs provider clients and a routed llm.Client from
// the normalized catalog.
func BuildClients(cat *Catalog, cfg *config.Config, logger *slog.Logger) (*ClientBundle, error) {
	if cat == nil {
		return nil, fmt.Errorf("nil model catalog")
	}
	if logger == nil {
		logger = slog.Default()
	}

	ollamaClients := make(map[string]*modelproviders.OllamaClient)
	lmstudioClients := make(map[string]*modelproviders.LMStudioClient)
	resourceClients := make(map[string]llm.Client, len(cat.Resources))
	healthClients := make(map[string]ResourceHealthClient, len(cat.Resources))

	var anthropicClient *modelproviders.AnthropicClient

	for _, res := range cat.Resources {
		var client llm.Client
		switch res.Provider {
		case "ollama":
			oc := modelproviders.NewOllamaClient(res.URL, logger.With("resource", res.ID))
			ollamaClients[res.ID] = oc
			healthClients[res.ID] = ResourceHealthClient{
				Ping:          oc.Ping,
				AttachWatcher: oc.SetWatcher,
			}
			client = oc
		case "lmstudio":
			lc := modelproviders.NewLMStudioClient(res.URL, serverAPIKey(cfg, res.ID), logger.With("resource", res.ID))
			lmstudioClients[res.ID] = lc
			healthClients[res.ID] = ResourceHealthClient{
				Ping:          lc.Ping,
				AttachWatcher: lc.AttachWatcher,
			}
			client = lc
		case "anthropic":
			if !cfg.Anthropic.Configured() {
				return nil, fmt.Errorf("resource %q requires anthropic config", res.ID)
			}
			if anthropicClient == nil {
				anthropicClient = modelproviders.NewAnthropicClient(cfg.Anthropic.APIKey, logger)
			}
			client = anthropicClient
		default:
			return nil, fmt.Errorf("provider %q is not implemented for resource %q", res.Provider, res.ID)
		}

		resourceClients[res.ID] = client
	}

	bundle := &ClientBundle{
		ResourceClients: resourceClients,
		HealthClients:   healthClients,
		OllamaClients:   ollamaClients,
		LMStudioClients: lmstudioClients,
	}
	client, err := bundle.BuildRoutedClient(cat)
	if err != nil {
		return nil, err
	}
	bundle.Client = client
	return bundle, nil
}

func serverAPIKey(cfg *config.Config, id string) string {
	if cfg == nil {
		return ""
	}
	if srv, ok := cfg.Models.Resources[id]; ok {
		return srv.APIKey
	}
	return ""
}

// BuildRoutedClient constructs a routed llm.Client for the provided
// effective catalog using the bundle's stable per-resource clients.
func (b *ClientBundle) BuildRoutedClient(cat *Catalog) (llm.Client, error) {
	if b == nil {
		return nil, fmt.Errorf("nil client bundle")
	}
	if cat == nil {
		return nil, fmt.Errorf("nil model catalog")
	}

	fallback, err := b.fallbackClient(cat)
	if err != nil {
		return nil, err
	}

	multi := llm.NewMultiClient(fallback)
	for id, client := range b.ResourceClients {
		multi.AddProvider(id, client)
	}

	for _, dep := range cat.Deployments {
		multi.AddRoute(dep.ID, dep.ResourceID, dep.ModelName)
	}
	for alias, target := range cat.aliases {
		if alias != target {
			multi.AddAlias(alias, target)
		}
	}
	for alias, targets := range cat.ambiguous {
		multi.MarkAmbiguous(alias, targets)
	}

	return multi, nil
}

func (b *ClientBundle) fallbackClient(cat *Catalog) (llm.Client, error) {
	if cat == nil {
		return nil, fmt.Errorf("nil model catalog")
	}
	if preferred := cat.preferredRoutedDefault(); preferred != "" {
		if dep, ok := cat.byID[preferred]; ok {
			if client, ok := b.ResourceClients[dep.ResourceID]; ok {
				return client, nil
			}
		}
	}
	if url := cat.PrimaryOllamaURL(); url != "" {
		for _, res := range cat.Resources {
			if res.URL != url {
				continue
			}
			if client, ok := b.ResourceClients[res.ID]; ok {
				return client, nil
			}
		}
	}
	if client, ok := b.ResourceClients["default"]; ok {
		return client, nil
	}
	if len(b.ResourceClients) == 0 {
		return nil, fmt.Errorf("no resource clients configured")
	}
	ids := make([]string, 0, len(b.ResourceClients))
	for id := range b.ResourceClients {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return b.ResourceClients[ids[0]], nil
}
