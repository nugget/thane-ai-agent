package config

import (
	"strings"
	"testing"
)

func TestRootContextPolicyValidate(t *testing.T) {
	tests := []struct {
		name    string
		policy  RootContextPolicy
		wantErr string
	}{
		{name: "empty policy is valid", policy: RootContextPolicy{}},
		{name: "tagged injection", policy: RootContextPolicy{Inject: RootInjectTagged}},
		{name: "on request search", policy: RootContextPolicy{Search: RootSearchOnRequest}},
		{
			name:    "unknown inject rejected",
			policy:  RootContextPolicy{Inject: "always"},
			wantErr: "context.inject",
		},
		{
			name:    "unknown search rejected",
			policy:  RootContextPolicy{Search: "sometimes"},
			wantErr: "context.search",
		},
		{
			name:   "requires_tag with tagged injection",
			policy: RootContextPolicy{Inject: RootInjectTagged, RequiresTag: "devops"},
		},
		{
			name:    "requires_tag without injection rejected",
			policy:  RootContextPolicy{Search: RootSearchOnRequest, RequiresTag: "devops"},
			wantErr: "requires_tag gates prompt injection",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.policy.Validate("vault")
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestRootContextPolicyEffectiveDefaults(t *testing.T) {
	var p RootContextPolicy
	if got := p.EffectiveInject(); got != RootInjectNone {
		t.Fatalf("EffectiveInject() = %q, want none", got)
	}
	if got := p.EffectiveSearch(); got != RootSearchDefault {
		t.Fatalf("EffectiveSearch() = %q, want default", got)
	}
	if p.Declared() {
		t.Fatal("an empty policy must not report itself as declared")
	}
	if !(RootContextPolicy{Search: RootSearchNever}).Declared() {
		t.Fatal("a policy naming search must report as declared")
	}
}
