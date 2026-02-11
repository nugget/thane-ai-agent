// Package tools provides shell execution capabilities for the agent.
package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ShellExec provides command execution capabilities.
type ShellExec struct {
	enabled        bool
	workingDir     string
	allowedCmds    []string // Empty = allow all
	deniedCmds     []string // Patterns to block (e.g., "rm -rf", "sudo")
	defaultTimeout time.Duration
	maxOutputBytes int
}

// ShellExecConfig configures the shell executor.
type ShellExecConfig struct {
	Enabled        bool
	WorkingDir     string
	AllowedCmds    []string
	DeniedCmds     []string
	DefaultTimeout time.Duration
	MaxOutputBytes int
}

// DefaultShellExecConfig returns safe defaults.
func DefaultShellExecConfig() ShellExecConfig {
	return ShellExecConfig{
		Enabled:     false, // Disabled by default for safety
		WorkingDir:  "",
		AllowedCmds: []string{}, // Empty = allow all (when enabled)
		DeniedCmds: []string{
			"rm -rf /",
			"rm -rf /*",
			"mkfs",
			"dd if=",
			"> /dev/sd",
			"chmod -R 777 /",
			":(){ :|:& };:", // Fork bomb
		},
		DefaultTimeout: 30 * time.Second,
		MaxOutputBytes: 100 * 1024, // 100KB
	}
}

// NewShellExec creates a new shell executor.
func NewShellExec(cfg ShellExecConfig) *ShellExec {
	if cfg.DefaultTimeout == 0 {
		cfg.DefaultTimeout = 30 * time.Second
	}
	if cfg.MaxOutputBytes == 0 {
		cfg.MaxOutputBytes = 100 * 1024
	}
	return &ShellExec{
		enabled:        cfg.Enabled,
		workingDir:     cfg.WorkingDir,
		allowedCmds:    cfg.AllowedCmds,
		deniedCmds:     cfg.DeniedCmds,
		defaultTimeout: cfg.DefaultTimeout,
		maxOutputBytes: cfg.MaxOutputBytes,
	}
}

// Enabled reports whether shell execution is available.
func (s *ShellExec) Enabled() bool {
	return s.enabled
}

// ExecResult contains the result of a command execution.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
	TimedOut bool   `json:"timedOut,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Exec executes a shell command.
func (s *ShellExec) Exec(ctx context.Context, command string, timeoutSec int) (*ExecResult, error) {
	if !s.enabled {
		return nil, fmt.Errorf("shell execution is disabled")
	}

	// Check denied patterns
	cmdLower := strings.ToLower(command)
	for _, denied := range s.deniedCmds {
		if strings.Contains(cmdLower, strings.ToLower(denied)) {
			return nil, fmt.Errorf("command blocked by security policy: matches denied pattern %q", denied)
		}
	}

	// Check allowed list if specified
	if len(s.allowedCmds) > 0 {
		allowed := false
		for _, prefix := range s.allowedCmds {
			if strings.HasPrefix(command, prefix) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("command not in allowlist")
		}
	}

	// Set timeout
	timeout := s.defaultTimeout
	if timeoutSec > 0 {
		timeout = time.Duration(timeoutSec) * time.Second
	}
	// Cap at 5 minutes
	if timeout > 5*time.Minute {
		timeout = 5 * time.Minute
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Execute via shell
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if s.workingDir != "" {
		cmd.Dir = s.workingDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := &ExecResult{
		Stdout:   truncateOutput(stdout.String(), s.maxOutputBytes),
		Stderr:   truncateOutput(stderr.String(), s.maxOutputBytes),
		ExitCode: 0,
	}

	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.Error = "command timed out"
		result.ExitCode = -1
		return result, nil
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.Error = err.Error()
			result.ExitCode = -1
		}
	}

	return result, nil
}

// truncateOutput truncates output to maxBytes, adding a note if truncated.
func truncateOutput(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes] + "\n\n[... output truncated ...]"
}
