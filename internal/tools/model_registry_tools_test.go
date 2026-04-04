package tools

import (
	"testing"

	"github.com/nugget/thane-ai-agent/internal/models"
)

func TestConfigureModelRegistryTools_StoresDeps(t *testing.T) {
	reg := NewEmptyRegistry()
	modelRegistry := &models.Registry{}

	deploymentPersistCalls := 0
	deploymentDeleteCalls := 0
	resourcePersistCalls := 0
	resourceDeleteCalls := 0
	routerSyncCalls := 0

	reg.ConfigureModelRegistryTools(ModelRegistryToolDeps{
		Registry: modelRegistry,
		SyncRouter: func() {
			routerSyncCalls++
		},
		PersistDeploymentPolicy: func(string, models.DeploymentPolicy) error {
			deploymentPersistCalls++
			return nil
		},
		DeleteDeploymentPolicy: func(string) error {
			deploymentDeleteCalls++
			return nil
		},
		PersistResourcePolicy: func(string, models.ResourcePolicy) error {
			resourcePersistCalls++
			return nil
		},
		DeleteResourcePolicy: func(string) error {
			resourceDeleteCalls++
			return nil
		},
	})

	if reg.modelRegistry != modelRegistry {
		t.Fatal("model registry dependency was not stored")
	}
	if reg.modelRegistrySyncRouter == nil {
		t.Fatal("router sync callback was not stored")
	}
	if reg.persistModelRegistryPolicy == nil || reg.deletePersistedModelRegistryPolicy == nil {
		t.Fatal("deployment policy callbacks were not stored")
	}
	if reg.persistModelRegistryResourcePolicy == nil || reg.deletePersistedModelRegistryResourcePolicy == nil {
		t.Fatal("resource policy callbacks were not stored")
	}

	reg.modelRegistrySyncRouter()
	if err := reg.persistModelRegistryPolicy("spark/gpt-oss:20b", models.DeploymentPolicy{}); err != nil {
		t.Fatalf("persist deployment policy callback: %v", err)
	}
	if err := reg.deletePersistedModelRegistryPolicy("spark/gpt-oss:20b"); err != nil {
		t.Fatalf("delete deployment policy callback: %v", err)
	}
	if err := reg.persistModelRegistryResourcePolicy("spark", models.ResourcePolicy{}); err != nil {
		t.Fatalf("persist resource policy callback: %v", err)
	}
	if err := reg.deletePersistedModelRegistryResourcePolicy("spark"); err != nil {
		t.Fatalf("delete resource policy callback: %v", err)
	}

	if routerSyncCalls != 1 {
		t.Fatalf("router sync calls = %d, want 1", routerSyncCalls)
	}
	if deploymentPersistCalls != 1 {
		t.Fatalf("deployment persist calls = %d, want 1", deploymentPersistCalls)
	}
	if deploymentDeleteCalls != 1 {
		t.Fatalf("deployment delete calls = %d, want 1", deploymentDeleteCalls)
	}
	if resourcePersistCalls != 1 {
		t.Fatalf("resource persist calls = %d, want 1", resourcePersistCalls)
	}
	if resourceDeleteCalls != 1 {
		t.Fatalf("resource delete calls = %d, want 1", resourceDeleteCalls)
	}
}
