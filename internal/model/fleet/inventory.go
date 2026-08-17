package fleet

import (
	"context"
	"sort"
	"sync"
	"time"

	modelproviders "github.com/nugget/thane-ai-agent/internal/model/fleet/providers"
)

// Inventory is the mutable provider-exported overlay that sits on top
// of the immutable config-defined model catalog.
type Inventory struct {
	Resources []ResourceInventory
}

// ResourceInventory captures the models advertised by one provider
// resource at a point in time. Errors are recorded per-resource so the
// overlay can be partial without blocking startup.
type ResourceInventory struct {
	ResourceID   string
	Provider     string
	Capabilities modelproviders.Capabilities
	Attempted    bool
	Models       []DiscoveredModel
	Error        string
}

// DiscoveredModel is provider-exported model metadata normalized just
// enough for Thane's overlay layer.
type DiscoveredModel struct {
	SupportsChat        bool
	Name                string
	ModelType           string
	Publisher           string
	CompatibilityType   string
	State               string
	Family              string
	Families            []string
	ParameterSize       string
	Quantization        string
	SupportsTools       bool
	TrainedForToolUse   bool
	SupportsStreaming   bool
	SupportsImages      bool
	ContextWindow       int
	MaxContextWindow    int
	LoadedContextWindow int
	LoadedInstanceID    string
}

// DiscoverInventory probes configured resources for live model
// inventory. Discovery is best-effort; individual resource failures are
// captured in the returned overlay instead of aborting startup.
func DiscoverInventory(ctx context.Context, cat *Catalog, bundle *ClientBundle) *Inventory {
	if cat == nil || bundle == nil {
		return &Inventory{}
	}

	inv := &Inventory{
		Resources: make([]ResourceInventory, 0, len(cat.Resources)),
	}

	// Probe every resource at once, each under its own deadline. Run
	// sequentially, one unreachable-but-listening runner delayed every
	// resource behind it and, at boot, the process itself — and the
	// failure of one endpoint is no reason to learn nothing about the
	// rest. Results are written by index so the inventory keeps catalog
	// order regardless of which probe finishes first.
	results := make([]ResourceInventory, len(cat.Resources))
	keep := make([]bool, len(cat.Resources))
	var wg sync.WaitGroup
	for i, res := range cat.Resources {
		wg.Add(1)
		// i and res are per-iteration variables (go.mod declares go
		// 1.25; this has held since 1.22), so the closure captures a
		// distinct pair each time and each goroutine writes a distinct
		// element of results. Concurrent writes to different elements of
		// one slice do not race — the header is never touched.
		go func() {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, resourceProbeTimeout)
			defer cancel()
			results[i], keep[i] = probeResource(probeCtx, res, bundle)
		}()
	}
	wg.Wait()

	for i := range results {
		if keep[i] {
			inv.Resources = append(inv.Resources, results[i])
		}
	}

	return inv
}

// resourceProbeTimeout bounds one resource's inventory probe.
//
// Discovery is a single cheap request per resource, so a healthy runner
// answers in well under a second. The bound exists for the unhealthy
// one: a server that completes its TCP handshake and then goes quiet
// has nothing else stopping it, and before probes ran concurrently it
// held up startup and every resource queued behind it. That is the same
// failure the stream-idle guard closes during generation, reached one
// loop earlier.
const resourceProbeTimeout = 15 * time.Second

// probeResource asks one resource what models it has. The bool reports
// whether the result belongs in the inventory at all — a provider with
// nothing to discover (anthropic) is not a failure, it is silent.
func probeResource(ctx context.Context, res Resource, bundle *ClientBundle) (ResourceInventory, bool) {
	ri := ResourceInventory{
		ResourceID:   res.ID,
		Provider:     res.Provider,
		Capabilities: providerCapabilities(res.Provider, res.Capabilities),
	}

	switch res.Provider {
	case "ollama":
		ri.Attempted = true
		client := bundle.OllamaClients[res.ID]
		if client == nil {
			ri.Error = "missing ollama client"
			return ri, true
		}
		models, err := client.ListModelInfos(ctx)
		if err != nil {
			ri.Error = err.Error()
			return ri, true
		}
		sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
		for _, m := range models {
			ri.Models = append(ri.Models, DiscoveredModel{
				SupportsChat:      true,
				Name:              m.Name,
				Family:            m.Details.Family,
				Families:          append([]string(nil), m.Details.Families...),
				ParameterSize:     m.Details.ParameterSize,
				Quantization:      m.Details.QuantizationLevel,
				SupportsTools:     ri.Capabilities.SupportsTools,
				SupportsStreaming: ri.Capabilities.SupportsStreaming,
				SupportsImages: modelproviders.SupportsImagesForModel(
					ri.Provider,
					m.Name,
					m.Details.Family,
					m.Details.Families,
					ri.Capabilities,
				),
			})
		}
	case "openai_compat":
		ri.Attempted = true
		client := bundle.OpenAICompatClients[res.ID]
		if client == nil {
			ri.Error = "missing openai_compat client"
			return ri, true
		}
		models, err := client.ListModelInfos(ctx)
		if err != nil {
			ri.Error = err.Error()
			return ri, true
		}
		sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
		for _, m := range models {
			// The OpenAI schema carries only an id, so most of this
			// is what the server volunteered beyond it. A ceiling of
			// zero means the server did not report one — the
			// deployment then keeps its configured window rather
			// than inheriting a fabricated zero.
			ceiling := m.ContextCeiling()
			ri.Models = append(ri.Models, DiscoveredModel{
				SupportsChat:      modelproviders.SupportsChatForModel(ri.Provider, m.Type, ri.Capabilities),
				Name:              m.ID,
				Family:            m.Arch,
				Quantization:      m.Quantization,
				SupportsTools:     ri.Capabilities.SupportsTools,
				SupportsStreaming: ri.Capabilities.SupportsStreaming,
				SupportsImages: modelproviders.SupportsImagesForModel(
					ri.Provider, m.ID, m.Arch, nil, ri.Capabilities,
				),
				ContextWindow:    ceiling,
				MaxContextWindow: ceiling,
			})
		}
	case "lmstudio":
		ri.Attempted = true
		client := bundle.LMStudioClients[res.ID]
		if client == nil {
			ri.Error = "missing lmstudio client"
			return ri, true
		}
		models, err := client.ListModelInfos(ctx)
		if err != nil {
			ri.Error = err.Error()
			return ri, true
		}
		sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
		for _, m := range models {
			supportsChat := modelproviders.SupportsChatForModel(ri.Provider, m.Type, ri.Capabilities)
			contextWindow := m.LoadedContextLength
			if contextWindow <= 0 {
				contextWindow = m.MaxContextLength
			}
			ri.Models = append(ri.Models, DiscoveredModel{
				SupportsChat:      supportsChat,
				Name:              m.ID,
				ModelType:         m.Type,
				Publisher:         m.Publisher,
				CompatibilityType: m.CompatibilityType,
				State:             m.State,
				Family:            m.Arch,
				Quantization:      m.Quantization,
				SupportsTools:     supportsChat && ri.Capabilities.SupportsTools,
				TrainedForToolUse: m.TrainedForToolUse,
				SupportsStreaming: supportsChat && ri.Capabilities.SupportsStreaming,
				SupportsImages: (m.Vision || modelproviders.SupportsImagesForModel(
					ri.Provider,
					m.ID,
					m.Arch,
					nil,
					modelproviders.Capabilities{
						SupportsImages: supportsChat && ri.Capabilities.SupportsImages,
					},
				)) && supportsChat,
				ContextWindow:       contextWindow,
				MaxContextWindow:    m.MaxContextLength,
				LoadedContextWindow: m.LoadedContextLength,
				LoadedInstanceID:    m.LoadedInstanceID,
			})
		}
	}

	if !ri.Attempted {
		return ri, false
	}
	return ri, true
}
