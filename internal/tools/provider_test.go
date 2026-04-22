package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeProvider is a Provider implementation for tests.
type fakeProvider struct {
	name  string
	tools []*Tool
}

func (f *fakeProvider) Name() string   { return f.name }
func (f *fakeProvider) Tools() []*Tool { return f.tools }

func TestErrUnavailable_ErrorMessageIncludesToolAndReason(t *testing.T) {
	err := ErrUnavailable{Tool: "signal_send_message", Reason: "signal-cli not connected"}
	msg := err.Error()
	if !strings.Contains(msg, "signal_send_message") {
		t.Errorf("error %q should contain tool name", msg)
	}
	if !strings.Contains(msg, "signal-cli not connected") {
		t.Errorf("error %q should contain reason", msg)
	}
}

func TestErrUnavailable_ErrorsAs(t *testing.T) {
	var wrapped error = ErrUnavailable{Tool: "mqtt_wake_list", Reason: "broker offline"}

	var target ErrUnavailable
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As should detect ErrUnavailable")
	}
	if target.Tool != "mqtt_wake_list" {
		t.Errorf("target.Tool = %q, want mqtt_wake_list", target.Tool)
	}
}

func TestRegisterProvider_RegistersAllTools(t *testing.T) {
	reg := NewEmptyRegistry()

	handler := func(ctx context.Context, args map[string]any) (string, error) { return "ok", nil }
	p := &fakeProvider{
		name: "test",
		tools: []*Tool{
			{Name: "test_a", Description: "A", Handler: handler},
			{Name: "test_b", Description: "B", Handler: handler},
		},
	}
	reg.RegisterProvider(p)

	if reg.Get("test_a") == nil {
		t.Error("test_a should be registered")
	}
	if reg.Get("test_b") == nil {
		t.Error("test_b should be registered")
	}
}

func TestRegisterProvider_NilProviderIsNoOp(t *testing.T) {
	reg := NewEmptyRegistry()
	// Should not panic.
	reg.RegisterProvider(nil)
}

func TestRegisterProvider_SkipsNilHandlers(t *testing.T) {
	reg := NewEmptyRegistry()

	p := &fakeProvider{
		name: "nil-handler",
		tools: []*Tool{
			{Name: "bad_tool", Description: "bad", Handler: nil},
			{Name: "good_tool", Description: "good", Handler: func(ctx context.Context, args map[string]any) (string, error) { return "ok", nil }},
		},
	}
	reg.RegisterProvider(p)

	// bad_tool should be skipped (nil handler violates provider contract).
	if reg.Get("bad_tool") != nil {
		t.Error("tool with nil handler should be skipped by RegisterProvider")
	}
	// good_tool should still register.
	if reg.Get("good_tool") == nil {
		t.Error("tool with valid handler should be registered")
	}
}

// TestRegisterProvider_DeclaredButUnavailablePattern documents the
// async-binding pattern: a provider can declare a tool up front and
// have the handler return ErrUnavailable until the runtime is ready.
// The tool is visible to the registry (and capability-tag resolution)
// from the moment of registration; only invocation fails.
func TestRegisterProvider_DeclaredButUnavailablePattern(t *testing.T) {
	var ready bool
	handler := func(ctx context.Context, args map[string]any) (string, error) {
		if !ready {
			return "", ErrUnavailable{Tool: "test_tool", Reason: "runtime not yet bound"}
		}
		return "ok", nil
	}

	reg := NewEmptyRegistry()
	reg.RegisterProvider(&fakeProvider{
		name:  "test",
		tools: []*Tool{{Name: "test_tool", Handler: handler}},
	})

	tool := reg.Get("test_tool")
	if tool == nil {
		t.Fatal("tool should be registered before runtime binds")
	}

	// Invocation before ready → ErrUnavailable.
	_, err := tool.Handler(context.Background(), nil)
	var unavail ErrUnavailable
	if !errors.As(err, &unavail) {
		t.Fatalf("pre-bind invocation should return ErrUnavailable, got %v", err)
	}

	// Runtime binds; subsequent invocation succeeds.
	ready = true
	out, err := tool.Handler(context.Background(), nil)
	if err != nil {
		t.Fatalf("post-bind invocation: %v", err)
	}
	if out != "ok" {
		t.Errorf("post-bind invocation = %q, want ok", out)
	}
}
