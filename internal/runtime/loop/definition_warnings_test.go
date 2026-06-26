package loop

import (
	"strings"
	"testing"
	"time"
)

func TestBuildDefinitionWarnings_DefaultSleepAndDelegation(t *testing.T) {
	spec := Spec{
		Name:      "comal_burn_ban_monitor",
		Task:      "Check the county burn ban source hourly and update Home Assistant.",
		Operation: OperationService,
		Tags:      []string{"web", "ha"},
	}

	warnings := BuildDefinitionWarnings(spec)
	if len(warnings) < 3 {
		t.Fatalf("warnings = %#v, want at least 3 warnings", warnings)
	}
	codes := make(map[string]bool, len(warnings))
	for _, warning := range warnings {
		codes[warning.Code] = true
	}
	for _, code := range []string{
		"service_default_sleep_envelope",
		"task_mentions_timing_without_explicit_sleep",
		"service_delegation_gating_enabled",
	} {
		if !codes[code] {
			t.Fatalf("warning codes = %#v, want %q", codes, code)
		}
	}
}

func TestBuildDefinitionWarnings_FixedIntervalJitterAndCompletion(t *testing.T) {
	spec := Spec{
		Name:         "battery_watch",
		Task:         "Watch batteries.",
		Operation:    OperationService,
		Completion:   CompletionConversation,
		SleepMin:     15 * time.Minute,
		SleepMax:     15 * time.Minute,
		SleepDefault: 15 * time.Minute,
	}

	warnings := BuildDefinitionWarnings(spec)
	codes := make(map[string]bool, len(warnings))
	for _, warning := range warnings {
		codes[warning.Code] = true
	}
	if !codes["service_completion_not_periodic"] {
		t.Fatalf("warning codes = %#v, want service_completion_not_periodic", codes)
	}
	if !codes["fixed_interval_with_jitter"] {
		t.Fatalf("warning codes = %#v, want fixed_interval_with_jitter", codes)
	}
}

func TestBuildDefinitionWarnings_MetadataShadowsSpecField(t *testing.T) {
	tests := []struct {
		name      string
		spec      Spec
		wantCode  string // "" = assert the shadow code is ABSENT
		wantInMsg string
	}{
		{
			name:      "parent_name in metadata fires with parent guidance",
			spec:      Spec{Operation: OperationService, Metadata: map[string]string{"parent_name": "travel"}},
			wantCode:  "metadata_shadows_spec_field",
			wantInMsg: "top-level parent_name field",
		},
		{
			name:      "generic shadow (tags) fires and names the field",
			spec:      Spec{Operation: OperationService, Metadata: map[string]string{"tags": "x"}},
			wantCode:  "metadata_shadows_spec_field",
			wantInMsg: "shadows the top-level tags field",
		},
		{
			// Containers and event-driven loops nest too, so the shadow check
			// must fire regardless of operation — this guards the placement of
			// the check before the service-only early return.
			name:      "shadow fires for a container, not just services",
			spec:      Spec{Operation: OperationContainer, Metadata: map[string]string{"parent_name": "core"}},
			wantCode:  "metadata_shadows_spec_field",
			wantInMsg: "does not nest this loop",
		},
		{
			name:     "non-shadow metadata key stays silent",
			spec:     Spec{Operation: OperationService, Metadata: map[string]string{"owner": "alice"}},
			wantCode: "",
		},
		{
			name:     "legacy quality_floor key is not flagged",
			spec:     Spec{Operation: OperationService, Metadata: map[string]string{"quality_floor": "3"}},
			wantCode: "",
		},
		{
			name:     "metadata key named metadata is not self-shadowed",
			spec:     Spec{Operation: OperationService, Metadata: map[string]string{"metadata": "x"}},
			wantCode: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got *DefinitionWarning
			for _, w := range BuildDefinitionWarnings(tc.spec) {
				if w.Code == "metadata_shadows_spec_field" {
					w := w
					got = &w
					break
				}
			}
			if tc.wantCode == "" {
				if got != nil {
					t.Fatalf("unexpected shadow warning: %q", got.Message)
				}
				return
			}
			if got == nil {
				t.Fatalf("missing %q warning for spec %+v", tc.wantCode, tc.spec)
			}
			if tc.wantInMsg != "" && !strings.Contains(got.Message, tc.wantInMsg) {
				t.Errorf("message = %q, want substring %q", got.Message, tc.wantInMsg)
			}
		})
	}
}

func TestBuildDefinitionWarnings_MetadataShadowDeterministicOrder(t *testing.T) {
	spec := Spec{
		Operation: OperationService,
		Metadata:  map[string]string{"tags": "x", "parent_name": "travel"},
	}
	var order []string
	for _, w := range BuildDefinitionWarnings(spec) {
		if w.Code == "metadata_shadows_spec_field" {
			order = append(order, w.Message)
		}
	}
	if len(order) != 2 {
		t.Fatalf("want 2 shadow warnings, got %d: %#v", len(order), order)
	}
	// Keys are sorted, so parent_name (the inert-parent message) precedes tags.
	if !strings.Contains(order[0], "parent_name") || !strings.Contains(order[1], "tags") {
		t.Errorf("shadow warnings not deterministically sorted: %#v", order)
	}
}
