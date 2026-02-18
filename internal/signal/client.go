package signal

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
	"sync/atomic"
	"time"
)

// rpcResponse pairs a raw JSON result with an optional error for
// delivery through the pending channel.
type rpcResponse struct {
	Result json.RawMessage
	Error  *rpcError
}

// rpcError is a JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error implements the error interface for rpcError.
func (e *rpcError) Error() string {
	return fmt.Sprintf("signal-cli rpc error %d: %s", e.Code, e.Message)
}

// rpcRequest is a JSON-RPC 2.0 request written to signal-cli's stdin.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcRaw is used to inspect incoming JSON lines from signal-cli to
// determine whether they are responses (have an id) or notifications
// (have a method).
type rpcRaw struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"` // nil for notifications
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Client communicates with a signal-cli process running in jsonRpc
// mode over stdin/stdout. Incoming message notifications are pushed to
// a channel; outbound requests use request-response correlation via a
// pending map.
type Client struct {
	command string
	args    []string
	logger  *slog.Logger

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader

	nextID  atomic.Int64
	mu      sync.Mutex                 // protects pending + stdin writes
	pending map[int64]chan rpcResponse // request ID → response channel

	messages chan *Envelope // inbound message notifications
	done     chan struct{}  // closed when reader goroutine exits
	waitErr  chan error     // receives cmd.Wait result (exactly once)
}

// NewClient creates a signal-cli JSON-RPC client. Call Start to launch
// the subprocess.
func NewClient(command string, args []string, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		command:  command,
		args:     args,
		logger:   logger,
		pending:  make(map[int64]chan rpcResponse),
		messages: make(chan *Envelope, 64),
		done:     make(chan struct{}),
		waitErr:  make(chan error, 1),
	}
}

// Start launches the signal-cli subprocess and begins reading
// notifications. Must be called exactly once.
func (c *Client) Start(ctx context.Context) error {
	c.logger.Info("starting signal-cli subprocess",
		"command", c.command,
		"args", c.args,
	)

	cmd := exec.CommandContext(ctx, c.command, c.args...)
	cmd.Env = os.Environ()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return fmt.Errorf("create stdout pipe: %w", err)
	}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return fmt.Errorf("create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start signal-cli: %w", err)
	}

	c.cmd = cmd
	c.stdin = stdin
	c.reader = bufio.NewReaderSize(stdout, 1<<20) // 1 MiB

	go c.drainStderr(stderrPipe)
	go c.readLoop()
	go func() {
		err := cmd.Wait()
		if err != nil {
			c.logger.Error("signal-cli subprocess exited with error", "error", err)
		} else {
			c.logger.Info("signal-cli subprocess exited")
		}
		c.waitErr <- err
	}()

	c.logger.Info("signal-cli subprocess started", "pid", cmd.Process.Pid)
	return nil
}

// Messages returns the channel of inbound message envelopes. The
// channel is closed when the subprocess exits.
func (c *Client) Messages() <-chan *Envelope {
	return c.messages
}

// Send sends a text message to a recipient and returns the server
// timestamp of the sent message.
func (c *Client) Send(ctx context.Context, recipient, message string) (int64, error) {
	raw, err := c.call(ctx, "send", map[string]any{
		"recipient": []string{recipient},
		"message":   message,
	})
	if err != nil {
		return 0, fmt.Errorf("signal send: %w", err)
	}

	var result sendResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return 0, fmt.Errorf("unmarshal send result: %w", err)
	}
	return result.Timestamp, nil
}

// SendReceipt sends a read receipt for the given message timestamp.
func (c *Client) SendReceipt(ctx context.Context, recipient string, timestamp int64) error {
	_, err := c.call(ctx, "sendReceipt", map[string]any{
		"recipient":       recipient,
		"targetTimestamp": timestamp,
		"type":            "read",
	})
	if err != nil {
		return fmt.Errorf("signal sendReceipt: %w", err)
	}
	return nil
}

// SendTyping sends a typing indicator start or stop.
func (c *Client) SendTyping(ctx context.Context, recipient string, stop bool) error {
	params := map[string]any{
		"recipient": recipient,
	}
	if stop {
		params["stop"] = true
	}
	_, err := c.call(ctx, "sendTyping", params)
	if err != nil {
		return fmt.Errorf("signal sendTyping: %w", err)
	}
	return nil
}

// Ping checks that the signal-cli subprocess is responsive by
// requesting its version. Suitable as a connwatch probe.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.call(ctx, "version", nil)
	return err
}

// Close shuts down the signal-cli subprocess gracefully. It closes
// stdin to signal exit, waits briefly, then force-kills if needed.
// The waiter goroutine started by Start() owns cmd.Wait(); Close
// reads its result via waitErr.
func (c *Client) Close() error {
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}

	c.logger.Info("stopping signal-cli subprocess", "pid", c.cmd.Process.Pid)

	// Close stdin to signal the subprocess to exit.
	if c.stdin != nil {
		c.stdin.Close()
	}

	// Wait for the waiter goroutine to deliver the exit status, or
	// force-kill after a timeout.
	select {
	case err := <-c.waitErr:
		return err
	case <-time.After(5 * time.Second):
		c.logger.Warn("signal-cli did not exit gracefully, killing",
			"pid", c.cmd.Process.Pid,
		)
		_ = c.cmd.Process.Kill()
		<-c.waitErr
		return nil
	}
}

// call sends a JSON-RPC request and waits for the response.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	// Bail early if context is already cancelled to avoid blocking on
	// a pipe write that has no reader.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	id := c.nextID.Add(1)
	ch := make(chan rpcResponse, 1)

	c.mu.Lock()
	c.pending[id] = ch

	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	data, err := json.Marshal(req)
	if err != nil {
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if _, err := c.stdin.Write(append(data, '\n')); err != nil {
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("write to signal-cli stdin: %w", err)
	}
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-c.done:
		return nil, fmt.Errorf("signal-cli subprocess exited")
	}
}

// readLoop reads newline-delimited JSON from the subprocess stdout,
// routing responses to their pending channels and notifications to the
// messages channel.
func (c *Client) readLoop() {
	defer close(c.done)
	defer close(c.messages)

	for {
		line, err := c.reader.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				c.logger.Error("signal-cli read error", "error", err)
			}
			// Drain any pending requests.
			c.mu.Lock()
			for id, ch := range c.pending {
				ch <- rpcResponse{Error: &rpcError{
					Code:    -1,
					Message: "subprocess exited",
				}}
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}

		var raw rpcRaw
		if err := json.Unmarshal(line, &raw); err != nil {
			c.logger.Debug("signal-cli non-JSON line", "line", string(line))
			continue
		}

		// Response (has ID) — route to pending channel.
		if raw.ID != nil {
			c.mu.Lock()
			ch, ok := c.pending[*raw.ID]
			if ok {
				delete(c.pending, *raw.ID)
			}
			c.mu.Unlock()

			if ok {
				ch <- rpcResponse{Result: raw.Result, Error: raw.Error}
			} else {
				c.logger.Debug("signal-cli response for unknown ID", "id", *raw.ID)
			}
			continue
		}

		// Notification — parse and route.
		if raw.Method == "receive" {
			var notif receiveNotification
			if err := json.Unmarshal(raw.Params, &notif); err != nil {
				c.logger.Warn("signal-cli malformed receive notification",
					"error", err,
					"params", string(raw.Params),
				)
				continue
			}

			// Only forward data messages (text). Skip typing
			// indicators, receipts, and sync messages — those are
			// informational and not actionable for the bridge.
			if notif.Envelope.DataMessage != nil {
				select {
				case c.messages <- &notif.Envelope:
				default:
					c.logger.Warn("signal message channel full, dropping message",
						"sender", notif.Envelope.Source,
					)
				}
			}
			continue
		}

		c.logger.Debug("signal-cli unknown message", "method", raw.Method)
	}
}

// drainStderr reads stderr lines and logs them at debug level.
func (c *Client) drainStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for scanner.Scan() {
		c.logger.Debug("signal-cli stderr", "line", scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		c.logger.Warn("signal-cli stderr scan error", "error", err)
	}
}
