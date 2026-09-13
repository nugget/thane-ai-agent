package app

import (
	"context"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/channels/email"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// noTurnRunner satisfies [looppkg.Runner] so a non-container loop can be
// constructed and registered; the invariant test never runs a turn.
type noTurnRunner struct{}

func (noTurnRunner) Run(context.Context, looppkg.Request, looppkg.StreamCallback) (*looppkg.Response, error) {
	return &looppkg.Response{}, nil
}

// TestEmailDefaultHandlerCarriesNoOwnerAuthority pins the loop half of
// the #1551 invariant that email identity never becomes authority. The
// built-in handler every new-mail wake lands in is registered under its
// built-in parent container, because tags and bindings cascade from
// containers, and its effective tags must be only email, its effective
// bindings empty, and its metadata unlike an owner channel loop's.
func TestEmailDefaultHandlerCarriesNoOwnerAuthority(t *testing.T) {
	cfg := &config.Config{Email: config.EmailConfig{Accounts: []config.EmailAccountConfig{{
		Name: "primary",
		IMAP: config.EmailIMAPConfig{Host: "imap.example.com", Username: "thane@example.com"},
	}}}}

	var handlerSpec, parentSpec *looppkg.Spec
	for _, spec := range builtInServiceDefinitionSpecs(cfg) {
		if spec.Name == email.DefaultHandlerLoopName {
			handlerSpec = &spec
		}
	}
	if handlerSpec == nil {
		t.Fatalf("no %s spec when email is configured", email.DefaultHandlerLoopName)
	}
	// The Go-shipped Task must keep naming the automated key the poller
	// emits, and the move it forbids.
	for _, want := range []string{"automated (present, and true, only for", "Never reply to an event carrying automated."} {
		if !strings.Contains(handlerSpec.Task, want) {
			t.Errorf("handler Task must contain %q", want)
		}
	}
	for _, spec := range builtInContainerDefinitionSpecs(cfg, nil) {
		if spec.Name == handlerSpec.ParentName {
			parentSpec = &spec
		}
	}
	if parentSpec == nil {
		t.Fatalf("handler parent %q is not a built-in container", handlerSpec.ParentName)
	}

	reg := looppkg.NewRegistry()
	parent, err := looppkg.NewFromSpec(*parentSpec, looppkg.Deps{})
	if err != nil {
		t.Fatalf("new parent container: %v", err)
	}
	if err := reg.Register(parent); err != nil {
		t.Fatalf("register parent container: %v", err)
	}
	handlerCfg := handlerSpec.ToConfig()
	handlerCfg.ParentID = parent.ID()
	handler, err := looppkg.New(handlerCfg, looppkg.Deps{Runner: noTurnRunner{}})
	if err != nil {
		t.Fatalf("new handler: %v", err)
	}
	if err := reg.Register(handler); err != nil {
		t.Fatalf("register handler: %v", err)
	}

	tags := reg.EffectiveTags(handler.ID())
	if len(tags) != 1 || tags[0].Tag != "email" {
		t.Errorf("handler effective tags = %+v, want only email", tags)
	}
	if bindings := reg.EffectiveBindings(handler.ID()); len(bindings) != 0 {
		t.Errorf("handler effective bindings = %+v, want none; it triages every mailbox", bindings)
	}
	if handlerSpec.Metadata["category"] == "channel" || handlerSpec.Metadata["is_owner"] != "" {
		t.Errorf("handler metadata must not mark an owner channel loop, got %v", handlerSpec.Metadata)
	}
}
