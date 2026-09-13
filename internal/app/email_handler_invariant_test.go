package app

import (
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
)

// TestEmailDefaultHandlerCarriesNoOwnerAuthority pins the loop half of
// the #1551 invariant that email identity never becomes authority: the
// built-in handler every new-mail wake lands in wears only the email
// tag, has no binding, and is not shaped like an owner channel loop, so
// no email turn can inherit owner capability or owner attention rights.
func TestEmailDefaultHandlerCarriesNoOwnerAuthority(t *testing.T) {
	cfg := &config.Config{Email: config.EmailConfig{Accounts: []config.EmailAccountConfig{{
		Name: "primary",
		IMAP: config.EmailIMAPConfig{Host: "imap.example.com", Username: "thane@example.com"},
	}}}}

	found := false
	for _, spec := range builtInServiceDefinitionSpecs(cfg) {
		if spec.Name != email.DefaultHandlerLoopName {
			continue
		}
		found = true
		if len(spec.Tags) != 1 || spec.Tags[0] != "email" {
			t.Errorf("handler tags = %v, want only email", spec.Tags)
		}
		if len(spec.Bindings) != 0 {
			t.Errorf("handler bindings = %v, want none; it triages every mailbox", spec.Bindings)
		}
		if spec.Metadata["category"] == "channel" || spec.Metadata["is_owner"] != "" {
			t.Errorf("handler metadata must not mark an owner channel loop, got %v", spec.Metadata)
		}
	}
	if !found {
		t.Fatalf("no %s spec when email is configured", email.DefaultHandlerLoopName)
	}
}
