package models

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/config"
	modelproviders "github.com/nugget/thane-ai-agent/internal/models/providers"
	"github.com/nugget/thane-ai-agent/internal/router"
)

// Resource is a runtime model provider resource that can serve one or
// more model deployments. Examples include an Ollama server on a
// specific host or a global cloud provider endpoint.
type Resource struct {
	ID           string
	Provider     string
	URL          string
	Capabilities modelproviders.Capabilities
}

// DeploymentSource describes where a deployment definition came from.
type DeploymentSource string

const (
	DeploymentSourceConfig     DeploymentSource = "config"
	DeploymentSourceDiscovered DeploymentSource = "discovered"
)

// Deployment is the normalized routing unit derived from config. The
// same upstream model on different resources becomes distinct
// deployments with distinct IDs.
type Deployment struct {
	ID                    string
	ModelName             string
	Provider              string
	ResourceID            string
	Server                string
	SupportsTools         bool
	ProviderSupportsTools bool
	SupportsStreaming     bool
	SupportsImages        bool
	ContextWindow         int
	Speed                 int
	Quality               int
	CostTier              int
	MinComplexity         string
	Source                DeploymentSource
	Routable              bool

	// Provider-exported metadata kept alongside the normalized Thane
	// deployment so later routing/policy layers can reason with it.
	Family        string
	Families      []string
	ParameterSize string
	Quantization  string

	PolicyState     DeploymentPolicyState
	PolicySource    DeploymentPolicySource
	PolicyReason    string
	PolicyUpdatedAt time.Time
}

// Catalog is the normalized, provider-aware model view used by both
// runtime client construction and the router.
type Catalog struct {
	DefaultModel  string
	RecoveryModel string
	LocalFirst    bool
	Resources     []Resource
	Deployments   []Deployment

	byID       map[string]Deployment
	byModel    map[string][]Deployment
	aliases    map[string]string
	ambiguous  map[string][]string
	resourceBy map[string]Resource
}

func routingEligible(dep Deployment) bool {
	if !dep.Routable {
		return false
	}
	return dep.PolicyState != DeploymentPolicyStateInactive
}

func (c *Catalog) preferredRoutedDefault() string {
	if c == nil {
		return ""
	}
	if dep, ok := c.byID[c.DefaultModel]; ok && routingEligible(dep) {
		return dep.ID
	}
	if dep, ok := c.byID[c.RecoveryModel]; ok && routingEligible(dep) {
		return dep.ID
	}
	for _, dep := range c.Deployments {
		if routingEligible(dep) {
			return dep.ID
		}
	}
	return c.DefaultModel
}

// BuildCatalog converts config-driven model/provider configuration into
// a normalized provider resource and deployment catalog.
func BuildCatalog(cfg *config.Config) (*Catalog, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}

	resourceByID := make(map[string]Resource)
	var resources []Resource

	if len(cfg.Models.Resources) > 0 {
		names := make([]string, 0, len(cfg.Models.Resources))
		for name := range cfg.Models.Resources {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			srv := cfg.Models.Resources[name]
			provider := srv.Provider
			if provider == "" {
				provider = "ollama"
			}
			res := Resource{
				ID:           name,
				Provider:     provider,
				URL:          srv.URL,
				Capabilities: modelproviders.CapabilitiesForProvider(provider),
			}
			resourceByID[res.ID] = res
			resources = append(resources, res)
		}
	} else {
		url := cfg.Models.PreferredOllamaURL()
		if url != "" {
			res := Resource{
				ID:           "default",
				Provider:     "ollama",
				URL:          url,
				Capabilities: modelproviders.CapabilitiesForProvider("ollama"),
			}
			resourceByID[res.ID] = res
			resources = append(resources, res)
		}
	}

	resourceIDsByProvider := make(map[string][]string)
	for _, res := range resources {
		resourceIDsByProvider[res.Provider] = append(resourceIDsByProvider[res.Provider], res.ID)
	}
	for provider := range resourceIDsByProvider {
		sort.Strings(resourceIDsByProvider[provider])
	}

	type unresolved struct {
		ID            string
		ModelName     string
		Provider      string
		ResourceID    string
		Server        string
		SupportsTools bool
		ContextWindow int
		Speed         int
		Quality       int
		CostTier      int
		MinComplexity string
		Source        DeploymentSource
		Routable      bool
		AlwaysQualify bool
		Family        string
		Families      []string
		ParameterSize string
		Quantization  string
	}

	var pending []unresolved
	countByModel := make(map[string]int)

	for i, m := range cfg.Models.Available {
		if strings.TrimSpace(m.Name) == "" {
			return nil, fmt.Errorf("models.available[%d]: name must not be empty", i)
		}

		provider := strings.ToLower(strings.TrimSpace(m.Provider))
		resourceID := ""
		resourceName := strings.TrimSpace(m.Resource)

		if resourceName != "" {
			res, ok := resourceByID[resourceName]
			if !ok {
				return nil, fmt.Errorf("models.available[%d] (%s): unknown resource %q", i, m.Name, resourceName)
			}
			if provider != "" && provider != res.Provider {
				return nil, fmt.Errorf("models.available[%d] (%s): provider %q conflicts with resource %q provider %q", i, m.Name, provider, resourceName, res.Provider)
			}
			provider = res.Provider
			resourceID = res.ID
		} else {
			if provider == "" {
				provider = "ollama"
			}
			if provider == "ollama" || provider == "lmstudio" {
				providerResourceIDs := resourceIDsByProvider[provider]
				switch {
				case hasProviderResource(resourceByID, "default", provider):
					resourceID = "default"
				case len(providerResourceIDs) == 1:
					resourceID = providerResourceIDs[0]
				case len(providerResourceIDs) == 0:
					return nil, fmt.Errorf("models.available[%d] (%s): provider %q requires a configured resource", i, m.Name, provider)
				default:
					return nil, fmt.Errorf("models.available[%d] (%s): multiple %s resources are configured; specify resource explicitly", i, m.Name, provider)
				}
			} else {
				if _, ok := resourceByID[provider]; !ok {
					res := Resource{
						ID:           provider,
						Provider:     provider,
						Capabilities: modelproviders.CapabilitiesForProvider(provider),
					}
					resourceByID[res.ID] = res
					resources = append(resources, res)
				}
				resourceID = provider
			}
		}

		pending = append(pending, unresolved{
			ModelName:     m.Name,
			Provider:      provider,
			ResourceID:    resourceID,
			Server:        resourceName,
			SupportsTools: m.SupportsTools,
			ContextWindow: m.ContextWindow,
			Speed:         m.Speed,
			Quality:       m.Quality,
			CostTier:      m.CostTier,
			MinComplexity: m.MinComplexity,
			Source:        DeploymentSourceConfig,
			Routable:      true,
		})
		countByModel[m.Name]++
	}

	sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })

	deployments := make([]Deployment, 0, len(pending))

	for _, p := range pending {
		id := p.ID
		if id == "" {
			id = deploymentID(p.ModelName, p.Provider, p.Server, countByModel[p.ModelName] > 1 || p.AlwaysQualify)
		}

		dep := Deployment{
			ID:            id,
			ModelName:     p.ModelName,
			Provider:      p.Provider,
			ResourceID:    p.ResourceID,
			Server:        p.Server,
			SupportsTools: p.SupportsTools,
			ContextWindow: p.ContextWindow,
			Speed:         p.Speed,
			Quality:       p.Quality,
			CostTier:      p.CostTier,
			MinComplexity: p.MinComplexity,
			Source:        p.Source,
			Routable:      p.Routable,
			Family:        p.Family,
			Families:      append([]string(nil), p.Families...),
			ParameterSize: p.ParameterSize,
			Quantization:  p.Quantization,
		}
		if res, ok := resourceByID[p.ResourceID]; ok {
			dep.ProviderSupportsTools = res.Capabilities.SupportsTools
			dep.SupportsStreaming = res.Capabilities.SupportsStreaming
			dep.SupportsImages = res.Capabilities.SupportsImages
		}
		deployments = append(deployments, dep)
	}

	cat := &Catalog{
		LocalFirst:  cfg.Models.LocalFirst,
		Resources:   append([]Resource(nil), resources...),
		Deployments: append([]Deployment(nil), deployments...),
	}
	if err := cat.reindex(cfg.Models.Default, cfg.Models.RecoveryModel); err != nil {
		return nil, err
	}

	return cat, nil
}

func hasProviderResource(resourceByID map[string]Resource, id, provider string) bool {
	res, ok := resourceByID[id]
	return ok && res.Provider == provider
}

func deploymentID(modelName, provider, server string, duplicate bool) string {
	if !duplicate {
		return modelName
	}
	if server != "" {
		return server + "/" + modelName
	}
	return provider + "/" + modelName
}

func (c *Catalog) reindex(defaultRef, recoveryRef string) error {
	byID := make(map[string]Deployment, len(c.Deployments))
	byModel := make(map[string][]Deployment)
	aliases := make(map[string]string)
	ambiguous := make(map[string][]string)
	resourceBy := make(map[string]Resource, len(c.Resources))

	sort.Slice(c.Resources, func(i, j int) bool { return c.Resources[i].ID < c.Resources[j].ID })
	sort.Slice(c.Deployments, func(i, j int) bool { return c.Deployments[i].ID < c.Deployments[j].ID })

	for _, res := range c.Resources {
		resourceBy[res.ID] = res
	}
	for _, dep := range c.Deployments {
		if strings.TrimSpace(dep.ID) == "" {
			return fmt.Errorf("deployment id must not be empty for model %q", dep.ModelName)
		}
		if _, exists := byID[dep.ID]; exists {
			return fmt.Errorf("duplicate deployment id %q for model %q", dep.ID, dep.ModelName)
		}
		aliases[dep.ID] = dep.ID
		byID[dep.ID] = dep
		byModel[dep.ModelName] = append(byModel[dep.ModelName], dep)
	}
	for modelName, deps := range byModel {
		if len(deps) == 1 {
			aliases[modelName] = deps[0].ID
			continue
		}
		if _, ok := byID[modelName]; ok {
			// Preserve a stable configured deployment ID even after
			// discovery adds additional qualified deployments for the
			// same upstream model name.
			continue
		}
		ids := make([]string, 0, len(deps))
		for _, dep := range deps {
			ids = append(ids, dep.ID)
		}
		sort.Strings(ids)
		ambiguous[modelName] = ids
	}

	c.byID = byID
	c.byModel = byModel
	c.aliases = aliases
	c.ambiguous = ambiguous
	c.resourceBy = resourceBy

	if defaultRef != "" {
		id, err := c.ResolveModelRef(defaultRef)
		if err != nil {
			return fmt.Errorf("models.default: %w", err)
		}
		c.DefaultModel = id
	}
	if recoveryRef != "" {
		id, err := c.ResolveModelRef(recoveryRef)
		if err != nil {
			return fmt.Errorf("models.recovery_model: %w", err)
		}
		c.RecoveryModel = id
	}
	return nil
}

// RouterConfig converts the normalized catalog into router config.
func (c *Catalog) RouterConfig(maxAuditLog int) router.Config {
	cfg := router.Config{
		LocalFirst:  c.LocalFirst,
		MaxAuditLog: maxAuditLog,
	}
	cfg.DefaultModel = c.preferredRoutedDefault()
	for _, dep := range c.Deployments {
		if !routingEligible(dep) {
			continue
		}
		minComp := router.ComplexitySimple
		switch dep.MinComplexity {
		case "moderate":
			minComp = router.ComplexityModerate
		case "complex":
			minComp = router.ComplexityComplex
		}
		cfg.Models = append(cfg.Models, router.Model{
			Name:                  dep.ID,
			UpstreamModel:         dep.ModelName,
			Provider:              dep.Provider,
			ResourceID:            dep.ResourceID,
			Server:                dep.Server,
			SupportsTools:         dep.SupportsTools,
			ProviderSupportsTools: dep.ProviderSupportsTools,
			SupportsStreaming:     dep.SupportsStreaming,
			SupportsImages:        dep.SupportsImages,
			ContextWindow:         dep.ContextWindow,
			Speed:                 dep.Speed,
			Quality:               dep.Quality,
			CostTier:              dep.CostTier,
			MinComplexity:         minComp,
		})
	}
	return cfg
}
