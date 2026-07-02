package tools

import (
	"context"
	"strings"
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

func TestShellExec_TimeoutReapsGrandchildren(t *testing.T) {
	// Regression for #1167: a grandchild that inherits the output pipes
	// must not block Exec past the deadline. Before the process-group
	// kill + WaitDelay, sh died at the timeout but Run() blocked until
	// the backgrounded sleep released stdout — wedging the agent turn
	// for the grandchild's whole lifetime.
	cfg := DefaultShellExecConfig()
	cfg.Enabled = true
	se := NewShellExec(cfg)

	start := time.Now()
	result, err := se.Exec(context.Background(), "sleep 30 & echo started", 1)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.TimedOut {
		t.Errorf("expected TimedOut, got %+v", result)
	}
	// 1s deadline + 5s WaitDelay worst case; anywhere near the
	// grandchild's 30s lifetime means the hang is back.
	if elapsed > 10*time.Second {
		t.Fatalf("Exec blocked %v — grandchild held the turn past the deadline", elapsed)
	}
	// Output produced before the deadline is preserved.
	if !strings.Contains(result.Stdout, "started") {
		t.Errorf("stdout = %q, want pre-deadline output preserved", result.Stdout)
	}
}

func TestShellExec_TimeoutGroupKillsLiveTree(t *testing.T) {
	// The incident shape from #1167: the command is still running at
	// the deadline with a backgrounded grandchild holding the pipes.
	// The group kill must reap the whole tree at the deadline — well
	// inside the 5s WaitDelay backstop, which would otherwise be the
	// thing unblocking us.
	cfg := DefaultShellExecConfig()
	cfg.Enabled = true
	se := NewShellExec(cfg)

	start := time.Now()
	result, err := se.Exec(context.Background(), "sleep 30 & sleep 30", 1)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.TimedOut {
		t.Errorf("expected TimedOut, got %+v", result)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("Exec blocked %v — group kill did not reap the tree at the 1s deadline", elapsed)
	}
}
