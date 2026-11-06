package fleet

import (
	"fmt"
	"log/slog"
	"time"

	modelproviders "github.com/nugget/thane-ai-agent/internal/model/fleet/providers"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
)

// ResourceClient is the provider client [NewResourceClient] built for
// one model resource, together with the typed handle for its provider.
// Exactly one typed handle is non-nil, and it is the same value as
// Client.
type ResourceClient struct {
	// Client is the inference client for the resource, before any
	// routing. It is the value [ClientBundle.ResourceClients] holds.
	Client llm.Client
	// Health is the resource's ping and readiness-watch surface, or nil
	// for a provider that has none. Anthropic has none: it is a single
	// cloud account rather than an endpoint Thane watches.
	Health *ResourceHealthClient
	// Ollama is the typed client when the resource's provider is
	// "ollama".
	Ollama *modelproviders.OllamaClient
	// LMStudio is the typed client when the resource's provider is
	// "lmstudio".
	LMStudio *modelproviders.LMStudioClient
	// OpenAICompat is the typed client when the resource's provider is
	// "openai_compat". An LM Studio resource leaves it nil even though
	// its client embeds one; reach that through LMStudio.
	OpenAICompat *modelproviders.OpenAICompatClient
	// Anthropic is the typed client when the resource's provider is
	// "anthropic".
	Anthropic *modelproviders.AnthropicClient
}

// ResourceClientOption adjusts how [NewResourceClient] builds a client.
// An option overrides the value the resource and config would otherwise
// supply, so a caller replaying recorded traffic can reproduce the
// serving settings of the recording rather than those of the local
// config.
type ResourceClientOption func(*resourceClientSettings)

// resourceClientSettings is what the options resolve to. The *Set flags
// exist because an explicit zero or empty value is itself an override:
// zero idle TTL omits the field, zero stream idle timeout disables the
// guard, and empty kwargs send none, each regardless of config.
type resourceClientSettings struct {
	logger                *slog.Logger
	streamIdleTimeout     time.Duration
	streamIdleTimeoutSet  bool
	chatTemplateKwargs    map[string]any
	chatTemplateKwargsSet bool
	idleTTLSeconds        int
	idleTTLSecondsSet     bool
	anthropic             *modelproviders.AnthropicClient
}

// WithClientLogger sets the logger the client writes through. Endpoint
// clients add a resource attribute naming the resource; the Anthropic
// client uses the logger as given. Without this option the client logs
// through [slog.Default].
func WithClientLogger(logger *slog.Logger) ResourceClientOption {
	return func(s *resourceClientSettings) {
		s.logger = logger
	}
}

// WithStreamIdleTimeout sets how long an OpenAI-protocol endpoint
// (openai_compat or lmstudio) may send nothing before a request is
// abandoned, in place of the resource's configured stream_idle_timeout.
// Zero disables the guard. Other providers ignore it.
func WithStreamIdleTimeout(d time.Duration) ResourceClientOption {
	return func(s *resourceClientSettings) {
		s.streamIdleTimeout = d
		s.streamIdleTimeoutSet = true
	}
}

// WithChatTemplateKwargs sets the chat_template_kwargs an OpenAI-protocol
// endpoint (openai_compat or lmstudio) sends with every request, in
// place of the resource's configured ones. An empty map sends none.
// Other providers ignore it.
func WithChatTemplateKwargs(kwargs map[string]any) ResourceClientOption {
	return func(s *resourceClientSettings) {
		s.chatTemplateKwargs = kwargs
		s.chatTemplateKwargsSet = true
	}
}

// WithIdleTTLSeconds sets the LM Studio idle ttl sent with every
// inference request, in place of [Resource].IdleTTLSeconds. Zero omits
// the field. Only lmstudio resources send a ttl; other providers ignore
// it.
func WithIdleTTLSeconds(seconds int) ResourceClientOption {
	return func(s *resourceClientSettings) {
		s.idleTTLSeconds = seconds
		s.idleTTLSecondsSet = true
	}
}

// WithSharedAnthropicClient makes an anthropic resource reuse client
// instead of constructing its own. Every anthropic resource is the same
// account, so [BuildClients] builds one client and shares it. A nil
// client means "construct one". Other providers ignore it.
func WithSharedAnthropicClient(client *modelproviders.AnthropicClient) ResourceClientOption {
	return func(s *resourceClientSettings) {
		s.anthropic = client
	}
}

// NewResourceClient constructs the provider client for one catalog
// resource. It is the only place provider clients are wired from
// config, so production and anything replaying production traffic
// build identical clients.
//
// The resource supplies the provider, URL, ID and idle TTL. cfg
// supplies credentials (models.resources.<id>.api_key, anthropic.api_key)
// and the resource's stream_idle_timeout and chat_template_kwargs.
// Options override those serving settings. cfg may be nil for endpoint
// providers, which then run unauthenticated with client defaults.
//
// It returns an error for an anthropic resource when anthropic is not
// configured, and for a provider it does not implement.
func NewResourceClient(res Resource, cfg *config.Config, opts ...ResourceClientOption) (*ResourceClient, error) {
	s := resolveResourceClientSettings(res, cfg, opts)

	switch res.Provider {
	case "ollama":
		oc := modelproviders.NewOllamaClient(res.URL, s.logger.With("resource", res.ID))
		return &ResourceClient{
			Client: oc,
			Health: &ResourceHealthClient{Ping: oc.Ping, AttachWatcher: oc.SetWatcher},
			Ollama: oc,
		}, nil
	case "lmstudio":
		lc := modelproviders.NewLMStudioClientWithTTL(res.URL, serverAPIKey(cfg, res.ID), res.ID, s.logger.With("resource", res.ID), s.idleTTLSeconds)
		s.applyOpenAIProtocol(lc.OpenAICompatClient)
		return &ResourceClient{
			Client:   lc,
			Health:   &ResourceHealthClient{Ping: lc.Ping, AttachWatcher: lc.AttachWatcher},
			LMStudio: lc,
		}, nil
	case "openai_compat":
		// Any server speaking the OpenAI chat protocol: vLLM,
		// SGLang, llama-server, NIM, or Ollama's own /v1 surface.
		// No idle TTL — that is an LM Studio extension, and sending
		// it to a server that does not know the field is a needless
		// compatibility risk.
		oc := modelproviders.NewOpenAICompatClient(res.URL, serverAPIKey(cfg, res.ID), "openai_compat", res.ID, s.logger.With("resource", res.ID), 0)
		s.applyOpenAIProtocol(oc)
		return &ResourceClient{
			Client:       oc,
			Health:       &ResourceHealthClient{Ping: oc.Ping, AttachWatcher: oc.AttachWatcher},
			OpenAICompat: oc,
		}, nil
	case "anthropic":
		if cfg == nil || !cfg.Anthropic.Configured() {
			return nil, fmt.Errorf("resource %q requires anthropic config", res.ID)
		}
		ac := s.anthropic
		if ac == nil {
			ac = modelproviders.NewAnthropicClient(cfg.Anthropic.APIKey, s.logger)
		}
		return &ResourceClient{Client: ac, Anthropic: ac}, nil
	default:
		return nil, fmt.Errorf("provider %q is not implemented for resource %q", res.Provider, res.ID)
	}
}

// resolveResourceClientSettings layers the options over the values the
// resource and config supply. A config stream_idle_timeout of zero is
// "unset", not "disabled": it leaves the client's default in place,
// because an omitted config line does not mean "never give up".
func resolveResourceClientSettings(res Resource, cfg *config.Config, opts []ResourceClientOption) resourceClientSettings {
	s := resourceClientSettings{idleTTLSeconds: res.IdleTTLSeconds}
	if cfg != nil {
		if rc, ok := cfg.Models.Resources[res.ID]; ok {
			if rc.StreamIdleTimeout != 0 {
				s.streamIdleTimeout = rc.StreamIdleTimeout
				s.streamIdleTimeoutSet = true
			}
			if len(rc.ChatTemplateKwargs) != 0 {
				s.chatTemplateKwargs = rc.ChatTemplateKwargs
				s.chatTemplateKwargsSet = true
			}
		}
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&s)
		}
	}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	return s
}

// applyOpenAIProtocol applies the serving settings shared by every
// OpenAI-protocol client. Settings nobody supplied are left alone, which
// keeps the client's defaults and keeps chat_template_kwargs off the
// wire for servers that have no opinion about it.
func (s resourceClientSettings) applyOpenAIProtocol(c *modelproviders.OpenAICompatClient) {
	if s.streamIdleTimeoutSet {
		c.SetStreamIdleTimeout(s.streamIdleTimeout)
	}
	if s.chatTemplateKwargsSet {
		c.SetChatTemplateKwargs(s.chatTemplateKwargs)
	}
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
