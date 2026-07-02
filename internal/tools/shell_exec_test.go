package tools

import (
	"context"
	"os/exec"
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
	// Regression for #1167: a same-group grandchild that inherits the
	// output pipes must not block Exec past the deadline. Depending on
	// scheduling this returns via WaitDelay (sh exits before the cancel
	// watchdog syncs, so Cancel never fires) or via the group kill —
	// either way the old code blocked for the grandchild's lifetime.
	// The escaped-session test below pins the WaitDelay path
	// deterministically.
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

// escapedSessionCommand returns a shell fragment that starts a
// 30-second pipe-holder in a NEW session — outside the process group
// the deadline kills — or skips the test when no escape helper exists
// on this platform (setsid ships with util-linux; perl covers macOS).
func escapedSessionCommand(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("setsid"); err == nil {
		return "setsid sleep 30 &"
	}
	if _, err := exec.LookPath("perl"); err == nil {
		return "perl -MPOSIX -e 'POSIX::setsid(); sleep 30' &"
	}
	t.Skip("no setsid or perl available to escape the process group")
	return ""
}

func TestShellExec_WaitDelayReapsEscapedSession(t *testing.T) {
	// The WaitDelay backstop, exercised deterministically: the
	// pipe-holder calls setsid, so the process-group kill cannot reach
	// it and only WaitDelay's pipe abandonment unblocks the turn.
	// Removing cmd.WaitDelay makes this block for the holder's full
	// 30 seconds.
	cfg := DefaultShellExecConfig()
	cfg.Enabled = true
	se := NewShellExec(cfg)

	start := time.Now()
	result, err := se.Exec(context.Background(), escapedSessionCommand(t)+" echo started", 1)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.TimedOut {
		t.Errorf("expected TimedOut, got %+v", result)
	}
	// Bounded by deadline + WaitDelay (1s + 5s) with slack; anywhere
	// near the holder's 30s lifetime means the backstop is broken.
	if elapsed > 12*time.Second {
		t.Fatalf("Exec blocked %v — escaped session held the turn past the WaitDelay window", elapsed)
	}
	if !strings.Contains(result.Stdout, "started") {
		t.Errorf("stdout = %q, want pre-deadline output preserved", result.Stdout)
	}
}
