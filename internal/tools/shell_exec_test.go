package tools

import (
	"context"
	"testing"
	"time"
)

func TestShellExec_BasicCommand(t *testing.T) {
	cfg := DefaultShellExecConfig()
	cfg.Enabled = true
	se := NewShellExec(cfg)

	result, err := se.Exec(context.Background(), "echo hello", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.Stdout != "hello\n" {
		t.Errorf("expected 'hello\\n', got %q", result.Stdout)
	}
}

func TestShellExec_Disabled(t *testing.T) {
	cfg := DefaultShellExecConfig()
	cfg.Enabled = false
	se := NewShellExec(cfg)

	_, err := se.Exec(context.Background(), "echo hello", 0)
	if err == nil {
		t.Fatal("expected error when disabled")
	}
}

func TestShellExec_DeniedCommand(t *testing.T) {
	cfg := DefaultShellExecConfig()
	cfg.Enabled = true
	se := NewShellExec(cfg)

	_, err := se.Exec(context.Background(), "rm -rf /", 0)
	if err == nil {
		t.Fatal("expected error for denied command")
	}
}

func TestShellExec_Timeout(t *testing.T) {
	cfg := DefaultShellExecConfig()
	cfg.Enabled = true
	cfg.DefaultTimeout = 1 * time.Second
	se := NewShellExec(cfg)

	result, err := se.Exec(context.Background(), "sleep 10", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.TimedOut {
		t.Error("expected timeout")
	}
}

func TestShellExec_NonZeroExit(t *testing.T) {
	cfg := DefaultShellExecConfig()
	cfg.Enabled = true
	se := NewShellExec(cfg)

	result, err := se.Exec(context.Background(), "exit 42", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ExitCode != 42 {
		t.Errorf("expected exit code 42, got %d", result.ExitCode)
	}
}

func TestShellExec_CapturesStderr(t *testing.T) {
	cfg := DefaultShellExecConfig()
	cfg.Enabled = true
	se := NewShellExec(cfg)

	result, err := se.Exec(context.Background(), "echo error >&2", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Stderr != "error\n" {
		t.Errorf("expected stderr 'error\\n', got %q", result.Stderr)
	}
}
