package app

import "testing"

// TestContainerSpec_IntentIsFirstClass guards that built-in grouping containers
// carry their purpose on the first-class Spec.Intent field, not the legacy
// metadata["intent"] bag. Built-ins on the metadata path would emit a
// (non-actionable) deprecation warning and lose their intent once the #1106
// one-release fallback is removed.
func TestContainerSpec_IntentIsFirstClass(t *testing.T) {
	spec := containerSpec("cognition", "Core cognition loops.")

	if spec.Intent != "Core cognition loops." {
		t.Errorf("containerSpec Intent = %q, want it on the first-class field", spec.Intent)
	}
	if _, ok := spec.Metadata["intent"]; ok {
		t.Error("containerSpec wrote intent into metadata; built-ins must use the first-class Intent field")
	}
}
