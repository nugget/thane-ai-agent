package tools

import "github.com/nugget/thane-ai-agent/internal/models"

// ModelRegistryToolDeps is the future-facing wiring seam for model
// registry tools. It intentionally carries the live registry plus the
// persistence and router-sync callbacks needed for operator controls,
// but does not register any tools yet.
type ModelRegistryToolDeps struct {
	Registry                *models.Registry
	SyncRouter              func()
	PersistDeploymentPolicy func(string, models.DeploymentPolicy) error
	DeleteDeploymentPolicy  func(string) error
	PersistResourcePolicy   func(string, models.ResourcePolicy) error
	DeleteResourcePolicy    func(string) error
}

// ConfigureModelRegistryTools stores the runtime model-registry dependencies
// needed by future model-registry tools. The actual tool family is
// intentionally deferred until we tackle model tooling more broadly.
func (r *Registry) ConfigureModelRegistryTools(deps ModelRegistryToolDeps) {
	r.modelRegistry = deps.Registry
	r.modelRegistrySyncRouter = deps.SyncRouter
	r.persistModelRegistryPolicy = deps.PersistDeploymentPolicy
	r.deletePersistedModelRegistryPolicy = deps.DeleteDeploymentPolicy
	r.persistModelRegistryResourcePolicy = deps.PersistResourcePolicy
	r.deletePersistedModelRegistryResourcePolicy = deps.DeleteResourcePolicy
}
