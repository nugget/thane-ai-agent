package models

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/config"
	"github.com/nugget/thane-ai-agent/internal/router"
)

// Resource is a runtime model provider resource that can serve one or
// more model deployments. Examples include an Ollama server on a
// specific host or a global cloud provider endpoint.
type Resource struct {
	ID       string
	Provider string
	URL      string
}

// Deployment is the normalized routing unit derived from config. The
// same upstream model on different resources becomes distinct
// deployments with distinct IDs.
type Deployment struct {
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

// BuildCatalog converts config-driven model/provider configuration into
// a normalized provider resource and deployment catalog.
func BuildCatalog(cfg *config.Config) (*Catalog, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil config")
	}

	resourceByID := make(map[string]Resource)
	var resources []Resource

	if len(cfg.Models.Servers) > 0 {
		names := make([]string, 0, len(cfg.Models.Servers))
		for name := range cfg.Models.Servers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			srv := cfg.Models.Servers[name]
			provider := srv.Provider
			if provider == "" {
				provider = "ollama"
			}
			res := Resource{
				ID:       name,
				Provider: provider,
				URL:      srv.URL,
			}
			resourceByID[res.ID] = res
			resources = append(resources, res)
		}
	} else {
		url := cfg.Models.PreferredOllamaURL()
		if url != "" {
			res := Resource{
				ID:       "default",
				Provider: "ollama",
				URL:      url,
			}
			resourceByID[res.ID] = res
			resources = append(resources, res)
		}
	}

	ollamaResourceIDs := make([]string, 0)
	for _, res := range resources {
		if res.Provider == "ollama" {
			ollamaResourceIDs = append(ollamaResourceIDs, res.ID)
		}
	}
	sort.Strings(ollamaResourceIDs)

	type unresolved struct {
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
	}

	var pending []unresolved
	countByModel := make(map[string]int)

	for i, m := range cfg.Models.Available {
		if strings.TrimSpace(m.Name) == "" {
			return nil, fmt.Errorf("models.available[%d]: name must not be empty", i)
		}

		provider := strings.TrimSpace(m.Provider)
		resourceID := ""
		server := strings.TrimSpace(m.Server)

		if server != "" {
			res, ok := resourceByID[server]
			if !ok {
				return nil, fmt.Errorf("models.available[%d] (%s): unknown server %q", i, m.Name, server)
			}
			if provider != "" && provider != res.Provider {
				return nil, fmt.Errorf("models.available[%d] (%s): provider %q conflicts with server %q provider %q", i, m.Name, provider, server, res.Provider)
			}
			provider = res.Provider
			resourceID = res.ID
		} else {
			if provider == "" {
				provider = "ollama"
			}
			if provider == "ollama" {
				switch {
				case hasOllamaResource(resourceByID, "default"):
					resourceID = "default"
				case len(ollamaResourceIDs) == 1:
					resourceID = ollamaResourceIDs[0]
				case len(ollamaResourceIDs) == 0:
					return nil, fmt.Errorf("models.available[%d] (%s): provider %q requires an ollama resource", i, m.Name, provider)
				default:
					return nil, fmt.Errorf("models.available[%d] (%s): multiple ollama servers are configured; specify server explicitly", i, m.Name)
				}
			} else {
				if _, ok := resourceByID[provider]; !ok {
					res := Resource{ID: provider, Provider: provider}
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
			Server:        server,
			SupportsTools: m.SupportsTools,
			ContextWindow: m.ContextWindow,
			Speed:         m.Speed,
			Quality:       m.Quality,
			CostTier:      m.CostTier,
			MinComplexity: m.MinComplexity,
		})
		countByModel[m.Name]++
	}

	sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })

	deployments := make([]Deployment, 0, len(pending))
	byID := make(map[string]Deployment, len(pending))
	byModel := make(map[string][]Deployment)
	aliases := make(map[string]string)
	ambiguous := make(map[string][]string)

	for _, p := range pending {
		id := deploymentID(p.ModelName, p.Provider, p.Server, countByModel[p.ModelName] > 1)
		if _, exists := byID[id]; exists {
			return nil, fmt.Errorf("duplicate deployment id %q for model %q", id, p.ModelName)
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
		}
		deployments = append(deployments, dep)
		byID[id] = dep
		byModel[p.ModelName] = append(byModel[p.ModelName], dep)
		aliases[id] = id
	}

	for modelName, deps := range byModel {
		if len(deps) == 1 {
			aliases[modelName] = deps[0].ID
			continue
		}
		ids := make([]string, 0, len(deps))
		for _, dep := range deps {
			ids = append(ids, dep.ID)
		}
		sort.Strings(ids)
		ambiguous[modelName] = ids
	}

	cat := &Catalog{
		LocalFirst: cfg.Models.LocalFirst,
		Resources:  resources,
		Deployments: func() []Deployment {
			out := make([]Deployment, len(deployments))
			copy(out, deployments)
			return out
		}(),
		byID:       byID,
		byModel:    byModel,
		aliases:    aliases,
		ambiguous:  ambiguous,
		resourceBy: resourceByID,
	}

	if cfg.Models.Default != "" {
		id, err := cat.ResolveModelRef(cfg.Models.Default)
		if err != nil {
			return nil, fmt.Errorf("models.default: %w", err)
		}
		cat.DefaultModel = id
	}
	if cfg.Models.RecoveryModel != "" {
		id, err := cat.ResolveModelRef(cfg.Models.RecoveryModel)
		if err != nil {
			return nil, fmt.Errorf("models.recovery_model: %w", err)
		}
		cat.RecoveryModel = id
	}

	return cat, nil
}

func hasOllamaResource(resourceByID map[string]Resource, id string) bool {
	res, ok := resourceByID[id]
	return ok && res.Provider == "ollama"
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

// ResolveModelRef resolves a raw model reference or qualified
// deployment ID into a concrete deployment ID.
func (c *Catalog) ResolveModelRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	if _, ok := c.byID[ref]; ok {
		return ref, nil
	}
	if ids, ok := c.ambiguous[ref]; ok {
		return "", fmt.Errorf("model %q is ambiguous; use one of %q", ref, ids)
	}
	if id, ok := c.aliases[ref]; ok {
		return id, nil
	}
	return "", fmt.Errorf("unknown model %q", ref)
}

// ContextWindowForModel returns the configured context window for a
// model reference or resolved deployment ID. When only an upstream
// model name is available from a provider response and multiple
// deployments share that name, the largest configured window is used as
// a safe upper bound.
func (c *Catalog) ContextWindowForModel(ref string, defaultSize int) int {
	if id, err := c.ResolveModelRef(ref); err == nil {
		if dep, ok := c.byID[id]; ok && dep.ContextWindow > 0 {
			return dep.ContextWindow
		}
	}
	if deps := c.byModel[ref]; len(deps) > 0 {
		maxWindow := 0
		for _, dep := range deps {
			if dep.ContextWindow > maxWindow {
				maxWindow = dep.ContextWindow
			}
		}
		if maxWindow > 0 {
			return maxWindow
		}
	}
	return defaultSize
}

// PrimaryOllamaURL returns the preferred Ollama base URL for callers
// that still need one local LLM endpoint outside the routed deployment
// path (for example media transcription helpers). Preference order is:
// a resource named "default", then the first Ollama resource in sorted
// order.
func (c *Catalog) PrimaryOllamaURL() string {
	if res, ok := c.resourceBy["default"]; ok && res.Provider == "ollama" {
		return res.URL
	}
	for _, res := range c.Resources {
		if res.Provider == "ollama" {
			return res.URL
		}
	}
	return ""
}

// RouterConfig converts the normalized catalog into router config.
func (c *Catalog) RouterConfig(maxAuditLog int) router.Config {
	cfg := router.Config{
		DefaultModel: c.DefaultModel,
		LocalFirst:   c.LocalFirst,
		MaxAuditLog:  maxAuditLog,
	}
	for _, dep := range c.Deployments {
		minComp := router.ComplexitySimple
		switch dep.MinComplexity {
		case "moderate":
			minComp = router.ComplexityModerate
		case "complex":
			minComp = router.ComplexityComplex
		}
		cfg.Models = append(cfg.Models, router.Model{
			Name:          dep.ID,
			UpstreamModel: dep.ModelName,
			Provider:      dep.Provider,
			ResourceID:    dep.ResourceID,
			Server:        dep.Server,
			SupportsTools: dep.SupportsTools,
			ContextWindow: dep.ContextWindow,
			Speed:         dep.Speed,
			Quality:       dep.Quality,
			CostTier:      dep.CostTier,
			MinComplexity: minComp,
		})
	}
	return cfg
}
