package models

import (
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/llm"
)

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
		out := make([]string, len(ids))
		copy(out, ids)
		return "", &llm.AmbiguousModelError{Model: ref, Targets: out}
	}
	if id, ok := c.aliases[ref]; ok {
		return id, nil
	}
	return "", &UnknownModelError{Model: ref}
}

// ResolveDeploymentRef resolves a caller-provided model reference or
// deployment ID into the normalized deployment metadata.
func (c *Catalog) ResolveDeploymentRef(ref string) (Deployment, error) {
	if c == nil {
		return Deployment{}, fmt.Errorf("nil catalog")
	}
	id, err := c.ResolveModelRef(ref)
	if err != nil {
		return Deployment{}, err
	}
	dep, ok := c.byID[id]
	if !ok {
		return Deployment{}, &UnknownModelError{Model: ref}
	}
	return dep, nil
}

// DeploymentByRef resolves a model reference or deployment ID and
// returns the normalized deployment metadata when known.
func (c *Catalog) DeploymentByRef(ref string) (Deployment, bool) {
	dep, err := c.ResolveDeploymentRef(ref)
	if err != nil {
		return Deployment{}, false
	}
	return dep, true
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

// ResourceByID returns the normalized resource metadata for the given
// configured resource ID.
func (c *Catalog) ResourceByID(id string) (Resource, bool) {
	if c == nil {
		return Resource{}, false
	}
	res, ok := c.resourceBy[strings.TrimSpace(id)]
	return res, ok
}
