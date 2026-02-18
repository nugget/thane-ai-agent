package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
)

// StdioConfig configures a stdio MCP transport that communicates with
// a subprocess over stdin/stdout using newline-delimited JSON-RPC.
type StdioConfig struct {
	// Command is the executable to run.
	Command string

	// Args are command-line arguments passed to the executable.
	Args []string

	// Env are additional environment variables for the subprocess
	// (format: "KEY=VALUE"). These are appended to the current
	// process environment.
	Env []string

	// Logger is the structured logger for transport diagnostics.
	Logger *slog.Logger
}

// StdioTransport communicates with an MCP server running as a
// subprocess. JSON-RPC messages are newline-delimited on stdin/stdout.
type StdioTransport struct {
	config StdioConfig
	logger *slog.Logger

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader
}

// NewStdioTransport creates a stdio transport for the given config.
// The subprocess is not started until the first Send or Notify call.
func NewStdioTransport(cfg StdioConfig) *StdioTransport {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &StdioTransport{
		config: cfg,
		logger: logger,
	}
}

// start launches the subprocess if it is not already running. The
// subprocess lifecycle is independent of call contexts — it survives
// individual request timeouts and is only terminated by explicit
// cleanup() or stop() calls. Caller must hold t.mu.
func (t *StdioTransport) start(_ context.Context) error {
	if t.cmd != nil && t.cmd.ProcessState == nil {
		// Process is still running.
		return nil
	}

	t.logger.Info("starting MCP subprocess",
		"command", t.config.Command,
		"args", t.config.Args,
	)

	cmd := exec.Command(t.config.Command, t.config.Args...)
	cmd.Env = append(os.Environ(), t.config.Env...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return fmt.Errorf("create stdout pipe: %w", err)
	}

	// Capture stderr for logging — not part of the protocol.
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return fmt.Errorf("create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		stderrPipe.Close()
		stdout.Close()
		stdin.Close()
		return fmt.Errorf("start subprocess %s: %w", t.config.Command, err)
	}

	t.cmd = cmd
	t.stdin = stdin
	t.reader = bufio.NewReaderSize(stdout, 1<<20) // 1 MiB buffer for large responses

	// Drain stderr in the background.
	go t.drainStderr(stderrPipe)

	t.logger.Info("MCP subprocess started", "pid", cmd.Process.Pid)
	return nil
}

// drainStderr reads stderr lines and logs them at debug level.
func (t *StdioTransport) drainStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for scanner.Scan() {
		t.logger.Debug("MCP subprocess stderr", "line", scanner.Text())
	}
}

// readResult is the outcome of a single line read from stdout.
type readResult struct {
	line []byte
	err  error
}

// Send sends a JSON-RPC request over stdin and reads the response from
// stdout. The mutex serializes access since stdio is inherently sequential.
// The read is performed in a goroutine so that context cancellation can
// interrupt a blocking read.
func (t *StdioTransport) Send(ctx context.Context, req *Request) (*Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.start(ctx); err != nil {
		return nil, err
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Write request + newline delimiter.
	if _, err := t.stdin.Write(append(data, '\n')); err != nil {
		t.cleanup()
		return nil, fmt.Errorf("write to subprocess stdin: %w", err)
	}

	// Read response lines. The subprocess may emit notifications before
	// the actual response, so we loop until we find a matching ID.
	// Reads are performed in a goroutine so context cancellation works.
	for {
		ch := make(chan readResult, 1)
		go func() {
			line, readErr := t.reader.ReadBytes('\n')
			ch <- readResult{line: line, err: readErr}
		}()

		select {
		case <-ctx.Done():
			// Context cancelled or timed out. Kill the subprocess so
			// the blocked read unblocks, then clean up.
			t.cleanup()
			return nil, ctx.Err()
		case res := <-ch:
			if res.err != nil {
				t.cleanup()
				return nil, fmt.Errorf("read from subprocess stdout: %w", res.err)
			}

			// Try to parse as a response (has "id" field).
			var resp Response
			if err := json.Unmarshal(res.line, &resp); err != nil {
				t.logger.Debug("skipping non-JSON line from MCP subprocess",
					"line", string(res.line),
				)
				continue
			}

			// Skip notifications (id == 0 and no result/error could be a
			// notification). We match on the request ID.
			if resp.ID == req.ID {
				return &resp, nil
			}

			// Log unexpected messages.
			t.logger.Debug("skipping unmatched MCP message", "id", resp.ID)
		}
	}
}

// Notify sends a JSON-RPC notification over stdin. No response is expected.
func (t *StdioTransport) Notify(ctx context.Context, notif *Notification) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := t.start(ctx); err != nil {
		return err
	}

	data, err := json.Marshal(notif)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	if _, err := t.stdin.Write(append(data, '\n')); err != nil {
		t.cleanup()
		return fmt.Errorf("write notification to subprocess stdin: %w", err)
	}

	return nil
}

// Close terminates the subprocess and releases resources.
func (t *StdioTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.stop()
}

// stop terminates the subprocess. Caller must hold t.mu.
func (t *StdioTransport) stop() error {
	if t.cmd == nil || t.cmd.Process == nil {
		return nil
	}

	t.logger.Info("stopping MCP subprocess", "pid", t.cmd.Process.Pid)

	// Close stdin to signal the subprocess to exit.
	if t.stdin != nil {
		t.stdin.Close()
	}

	// Wait briefly for graceful exit, then force kill.
	done := make(chan error, 1)
	go func() { done <- t.cmd.Wait() }()

	select {
	case err := <-done:
		t.cmd = nil
		return err
	case <-time.After(5 * time.Second):
		t.logger.Warn("MCP subprocess did not exit gracefully, killing",
			"pid", t.cmd.Process.Pid,
		)
		_ = t.cmd.Process.Kill()
		<-done
		t.cmd = nil
		return nil
	}
}

// cleanup resets the process state after a failure. Caller must hold t.mu.
func (t *StdioTransport) cleanup() {
	if t.stdin != nil {
		t.stdin.Close()
	}
	if t.cmd != nil && t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
		_ = t.cmd.Wait()
	}
	t.cmd = nil
	t.stdin = nil
	t.reader = nil
}
