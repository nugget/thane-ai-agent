package models

import (
	"fmt"
	"log/slog"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/llm"
)

// ClientBundle contains the routed LLM client plus the concrete Ollama
// clients keyed by resource ID for connection watching and other
// resource-level concerns.
type ClientBundle struct {
	Client          llm.Client
	ResourceClients map[string]llm.Client
	OllamaClients   map[string]*llm.OllamaClient
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

	ollamaClients := make(map[string]*llm.OllamaClient)
	resourceClients := make(map[string]llm.Client, len(cat.Resources))

	var anthropicClient *llm.AnthropicClient

	for _, res := range cat.Resources {
		var client llm.Client
		switch res.Provider {
		case "ollama":
			oc := llm.NewOllamaClient(res.URL, logger.With("resource", res.ID))
			ollamaClients[res.ID] = oc
			client = oc
		case "anthropic":
			if !cfg.Anthropic.Configured() {
				return nil, fmt.Errorf("resource %q requires anthropic config", res.ID)
			}
			if anthropicClient == nil {
				anthropicClient = llm.NewAnthropicClient(cfg.Anthropic.APIKey, logger)
			}
			client = anthropicClient
		default:
			return nil, fmt.Errorf("provider %q is not implemented for resource %q", res.Provider, res.ID)
		}

		resourceClients[res.ID] = client
	}

	bundle := &ClientBundle{
		ResourceClients: resourceClients,
		OllamaClients:   ollamaClients,
	}
	client, err := bundle.BuildRoutedClient(cat)
	if err != nil {
		return nil, err
	}
	bundle.Client = client
	return bundle, nil
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

	var fallback llm.Client
	switch {
	case cat.DefaultModel != "":
		if dep, ok := cat.byID[cat.DefaultModel]; ok {
			fallback = b.ResourceClients[dep.ResourceID]
		}
	case len(b.OllamaClients) > 0:
		if url := cat.PrimaryOllamaURL(); url != "" {
			for id, client := range b.OllamaClients {
				if res, ok := cat.resourceBy[id]; ok && res.URL == url {
					fallback = client
					break
				}
			}
		}
	}

	if fallback == nil {
		for _, client := range b.ResourceClients {
			fallback = client
			break
		}
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
