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
	openAICompatClients := make(map[string]*modelproviders.OpenAICompatClient)
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
			lc := modelproviders.NewLMStudioClientWithTTL(res.URL, serverAPIKey(cfg, res.ID), res.ID, logger.With("resource", res.ID), res.IdleTTLSeconds)
			applyStreamIdleTimeout(lc.OpenAICompatClient, cfg, res.ID)
			applyChatTemplateKwargs(lc.OpenAICompatClient, cfg, res.ID)
			lmstudioClients[res.ID] = lc
			healthClients[res.ID] = ResourceHealthClient{
				Ping:          lc.Ping,
				AttachWatcher: lc.AttachWatcher,
			}
			client = lc
		case "openai_compat":
			// Any server speaking the OpenAI chat protocol: vLLM,
			// SGLang, llama-server, NIM, or Ollama's own /v1 surface.
			// No idle TTL — that is an LM Studio extension, and sending
			// it to a server that does not know the field is a needless
			// compatibility risk.
			oc := modelproviders.NewOpenAICompatClient(res.URL, serverAPIKey(cfg, res.ID), "openai_compat", res.ID, logger.With("resource", res.ID), 0)
			applyStreamIdleTimeout(oc, cfg, res.ID)
			applyChatTemplateKwargs(oc, cfg, res.ID)
			openAICompatClients[res.ID] = oc
			healthClients[res.ID] = ResourceHealthClient{
				Ping:          oc.Ping,
				AttachWatcher: oc.AttachWatcher,
			}
			client = oc
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

// applyStreamIdleTimeout overrides a client's silence bound when the
// resource configured one. An unset value keeps the client's default
// rather than collapsing to zero, since zero means "never give up" and
// an omitted config line does not mean that.
func applyStreamIdleTimeout(c *modelproviders.OpenAICompatClient, cfg *config.Config, resourceID string) {
	if c == nil || cfg == nil {
		return
	}
	res, ok := cfg.Models.Resources[resourceID]
	if !ok || res.StreamIdleTimeout == 0 {
		return
	}
	c.SetStreamIdleTimeout(res.StreamIdleTimeout)
}

// applyChatTemplateKwargs forwards the resource's configured
// chat_template_kwargs onto its client. Resources that configured none
// are left alone, which keeps the field off the wire for every server
// that has no opinion about it.
func applyChatTemplateKwargs(c *modelproviders.OpenAICompatClient, cfg *config.Config, resourceID string) {
	if c == nil || cfg == nil {
		return
	}
	res, ok := cfg.Models.Resources[resourceID]
	if !ok || len(res.ChatTemplateKwargs) == 0 {
		return
	}
	c.SetChatTemplateKwargs(res.ChatTemplateKwargs)
}
