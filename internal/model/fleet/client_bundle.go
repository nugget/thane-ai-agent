package fleet

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	modelproviders "github.com/nugget/thane-ai-agent/internal/model/fleet/providers"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
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
	// OpenAICompatClients holds clients for provider-agnostic
	// OpenAI-protocol endpoints, keyed by resource ID.
	OpenAICompatClients map[string]*modelproviders.OpenAICompatClient
	// AnthropicClient is the singleton Anthropic provider shared across
	// all anthropic-backed resources, retained here so late-bind
	// machinery (e.g., Runtime.SetLogger) can find it without scanning
	// ResourceClients for the *AnthropicClient type.
	AnthropicClient *modelproviders.AnthropicClient
}

// ResourceHealthClient is the minimal health/watch surface that app
// wiring needs from one model-provider resource.
type ResourceHealthClient struct {
	Ping          func(ctx context.Context) error
	AttachWatcher func(w llm.ReadyWatcher)
}

// BuildClients constructs provider clients and a routed llm.Client from
// the normalized catalog. Each resource's client comes from
// [NewResourceClient]; every anthropic resource shares one client.
func BuildClients(cat *Catalog, cfg *config.Config, logger *slog.Logger) (*ClientBundle, error) {
	if cat == nil {
		return nil, fmt.Errorf("nil model catalog")
	}
	if logger == nil {
		logger = slog.Default()
	}

	ollamaClients := make(map[string]*modelproviders.OllamaClient)
	lmstudioClients := make(map[string]*modelproviders.LMStudioClient)
	openAICompatClients := make(map[string]*modelproviders.OpenAICompatClient)
	resourceClients := make(map[string]llm.Client, len(cat.Resources))
	healthClients := make(map[string]ResourceHealthClient, len(cat.Resources))

	var anthropicClient *modelproviders.AnthropicClient

	for _, res := range cat.Resources {
		rc, err := NewResourceClient(res, cfg, WithClientLogger(logger), WithSharedAnthropicClient(anthropicClient))
		if err != nil {
			return nil, err
		}
		resourceClients[res.ID] = rc.Client
		if rc.Health != nil {
			healthClients[res.ID] = *rc.Health
		}
		switch {
		case rc.Ollama != nil:
			ollamaClients[res.ID] = rc.Ollama
		case rc.LMStudio != nil:
			lmstudioClients[res.ID] = rc.LMStudio
		case rc.OpenAICompat != nil:
			openAICompatClients[res.ID] = rc.OpenAICompat
		case rc.Anthropic != nil:
			anthropicClient = rc.Anthropic
		}
	}

	bundle := &ClientBundle{
		ResourceClients:     resourceClients,
		HealthClients:       healthClients,
		OllamaClients:       ollamaClients,
		LMStudioClients:     lmstudioClients,
		OpenAICompatClients: openAICompatClients,
		AnthropicClient:     anthropicClient,
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

	fallback, err := b.fallbackClient(cat)
	if err != nil {
		return nil, err
	}

	multi := llm.NewMultiClient(fallback)
	for id, client := range b.ResourceClients {
		multi.AddProvider(id, client)
	}

	for _, dep := range cat.Deployments {
		upstreamModel := dep.ModelName
		if dep.Provider == "lmstudio" && dep.LoadedInstanceID != "" {
			upstreamModel = dep.LoadedInstanceID
		}
		multi.AddRoute(dep.ID, dep.ResourceID, upstreamModel)
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
