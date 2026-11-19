package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// stdioConfig configures one stdio server process.
type stdioConfig struct {
	command string
	args    []string
	env     []string // appended to os.Environ(); entries already KEY=VALUE
	dir     string   // absolute; "" = inherit Thane's working directory
	logger  *slog.Logger

	// exitWarned is shared by every transport of one server, so a
	// server that keeps failing at start-up logs its exit (with the
	// stderr tail) at WARN once per run of failures, and at Debug after
	// that. A process that sends a valid message re-arms it. nil warns
	// on every exit.
	exitWarned *atomic.Bool

	// Timing knobs; zero means the default. Tests shorten them.
	closeStdinWait  time.Duration // after closing stdin, before SIGTERM (2s)
	termWait        time.Duration // after SIGTERM, before SIGKILL (3s)
	exitDrainWait   time.Duration // after Wait returns, stdout drain grace (2s)
	frameFinishWait time.Duration // to finish a line its caller's ctx cut short (2m)
}

const (
	stdioDefaultCloseStdinWait  = 2 * time.Second
	stdioDefaultTermWait        = 3 * time.Second
	stdioDefaultExitDrainWait   = 2 * time.Second
	stdioDefaultFrameFinishWait = 2 * time.Minute
)

// stdioTransport implements Transport over a child process's
// stdin/stdout (newline-delimited JSON-RPC).
//
// The child runs in its own process group with all three standard
// streams on os.Pipe pairs, so writes honour deadlines and the parent's
// read ends can be closed to unblock the readers. One long-lived
// goroutine reads stdout and routes responses to pending calls by
// integer id; another drains stderr forever; a third waits for the
// process. Calls are unlimited and concurrent. A call whose ctx ends
// is abandoned (and, when cancellable, cancelled with
// notifications/cancelled); it never kills the process, even when the
// ctx ends halfway through writing the request (see writeFrame). Only
// Close, a process exit, or a fatal framing failure ends the process.
type stdioTransport struct {
	logger *slog.Logger
	hooks  transportHooks // hooks.inbound is this transport's own gate
	cmd    *exec.Cmd
	pid    int

	stdin  *os.File // parent's write end of the child's stdin
	stdout *os.File // parent's read end of the child's stdout
	stderr *os.File // parent's read end of the child's stderr

	exitWarned *atomic.Bool // see stdioConfig.exitWarned

	closeStdinWait  time.Duration
	termWait        time.Duration
	exitDrainWait   time.Duration
	frameFinishWait time.Duration

	nextID   atomic.Int64
	writeSem chan struct{} // capacity 1: one message on stdin at a time

	mu             sync.Mutex
	pending        map[int64]chan *Response // nil once finished
	finished       bool                     // Done is closed (or closing)
	doneCause      error                    // why the transport finished
	closeRequested bool                     // Close was called while the transport was live

	done       chan struct{}
	doneOnce   sync.Once
	lifeCtx    context.Context // handed to the hooks; cancelled when Done closes
	lifeCancel context.CancelFunc

	exited     chan struct{} // closed once cmd.Wait has returned
	exitErr    error         // cmd.Wait's result; read only after exited is closed
	readerDone chan struct{} // closed when the stdout reader returns
	stderrDone chan struct{} // closed when the stderr drain returns

	stdinOnce    sync.Once
	shutdownOnce sync.Once
	fatal        atomic.Bool // torn down by a framing or write failure
	sawMessage   atomic.Bool // the server wrote at least one valid JSON-RPC message
	nullIDWarned atomic.Bool
	stderrTail   stdioRing
}

var _ Transport = (*stdioTransport)(nil)

// newStdioTransport spawns the process immediately (exec.Command, never
// CommandContext; own process group). ctx bounds the spawn only and is
// never retained for the process lifetime: the hooks get
// context.WithoutCancel(ctx) plus the transport's own cancel. On error
// nothing is left running and the error wraps errUnreachable.
func newStdioTransport(ctx context.Context, cfg stdioConfig, hooks transportHooks) (*stdioTransport, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: start %s: %w", errUnreachable, cfg.command, err)
	}
	if cfg.command == "" {
		return nil, fmt.Errorf("%w: no command configured", errUnreachable)
	}
	logger := cfg.logger
	if logger == nil {
		logger = slog.Default()
	}

	// Every stream is an os.Pipe so the parent ends are pollable: stdin
	// writes honour deadlines, and closing a read end unblocks its
	// reader even when a grandchild still holds the write end open.
	// Index 0 is stdin (the child reads), 1 stdout and 2 stderr (the
	// child writes).
	var child, parent [3]*os.File
	for i := range child {
		r, w, err := os.Pipe()
		if err != nil {
			stdioCloseFiles(child[:], parent[:])
			return nil, fmt.Errorf("%w: start %s: create pipes: %w", errUnreachable, cfg.command, err)
		}
		if i == 0 {
			child[i], parent[i] = r, w
		} else {
			child[i], parent[i] = w, r
		}
	}

	cmd := exec.Command(cfg.command, cfg.args...)
	cmd.Env = append(os.Environ(), cfg.env...)
	cmd.Dir = cfg.dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = child[0], child[1], child[2]
	err := cmd.Start()
	stdioCloseFiles(child[:]) // the child has its own copies now
	if err != nil {
		stdioCloseFiles(parent[:])
		return nil, fmt.Errorf("%w: start %s: %w", errUnreachable, cfg.command, err)
	}

	lifeCtx, lifeCancel := context.WithCancel(context.WithoutCancel(ctx))
	hooks.inbound = newInboundGate()
	t := &stdioTransport{
		logger:          logger,
		hooks:           hooks,
		cmd:             cmd,
		pid:             cmd.Process.Pid,
		stdin:           parent[0],
		stdout:          parent[1],
		stderr:          parent[2],
		exitWarned:      cfg.exitWarned,
		closeStdinWait:  durationOr(cfg.closeStdinWait, stdioDefaultCloseStdinWait),
		termWait:        durationOr(cfg.termWait, stdioDefaultTermWait),
		exitDrainWait:   durationOr(cfg.exitDrainWait, stdioDefaultExitDrainWait),
		frameFinishWait: durationOr(cfg.frameFinishWait, stdioDefaultFrameFinishWait),
		writeSem:        make(chan struct{}, 1),
		pending:         make(map[int64]chan *Response),
		done:            make(chan struct{}),
		lifeCtx:         lifeCtx,
		lifeCancel:      lifeCancel,
		exited:          make(chan struct{}),
		readerDone:      make(chan struct{}),
		stderrDone:      make(chan struct{}),
	}
	go t.waitProcess()
	go t.readStdout()
	go t.drainStderr()
	logger.Debug("mcp: stdio server started", "command", cfg.command, "pid", t.pid)
	return t, nil
}

// stdioCloseFiles closes every non-nil file in each set.
func stdioCloseFiles(sets ...[]*os.File) {
	for _, set := range sets {
		for _, f := range set {
			if f != nil {
				_ = f.Close()
			}
		}
	}
}

// Call sends req with a fresh integer id and waits for its response. A
// JSON-RPC error response is returned as (resp, nil). When ctx ends
// first the call is abandoned (a late response is dropped) and, if
// info.cancellable, the server is sent notifications/cancelled; the
// process keeps running.
func (t *stdioTransport) Call(ctx context.Context, req *Request, info callInfo) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("stdio: %w", err)
	}
	n := t.nextID.Add(1)
	out := *req
	out.JSONRPC = jsonrpcVersion
	out.ID = intID(n)

	ch := make(chan *Response, 1)
	if !t.register(n, ch) {
		return nil, fmt.Errorf("%w: stdio: %w", errUnreachable, t.cause())
	}
	var cancelID json.RawMessage // cancelled after a cut-short write
	if info.cancellable {
		cancelID = out.ID
	}
	if err := t.writeFrame(ctx, &out, cancelID); err != nil {
		t.unregister(n)
		return nil, fmt.Errorf("stdio: %w", err)
	}
	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("%w: stdio: %w", errConnectionLost, t.cause())
		}
		return resp, nil
	case <-ctx.Done():
		return t.abandon(ctx, n, ch, out.Method, info)
	}
}

// abandon gives up on call n after its ctx ended. If the response (or
// the connection loss) won the race it is returned instead.
func (t *stdioTransport) abandon(ctx context.Context, n int64, ch chan *Response, method string, info callInfo) (*Response, error) {
	if !t.unregister(n) {
		// The reader or finish already took the entry; its send or
		// close is imminent.
		if resp, ok := <-ch; ok {
			return resp, nil
		}
		return nil, fmt.Errorf("%w: stdio: %w", errConnectionLost, t.cause())
	}
	if info.cancellable {
		nctx, cancel := cancelNotifyContext(ctx)
		err := t.write(nctx, cancelledNotification(intID(n), stdioCancelReason(ctx.Err())))
		cancel()
		if err != nil {
			t.logger.Debug("mcp: sending notifications/cancelled failed", "method", method, "id", n, "error", err)
		}
	}
	return nil, fmt.Errorf("stdio: %w", ctx.Err())
}

// Notify writes n and returns once the line is on stdin.
func (t *stdioTransport) Notify(ctx context.Context, n *Notification, _ callInfo) error {
	out := *n
	out.JSONRPC = jsonrpcVersion
	if err := t.write(ctx, &out); err != nil {
		return fmt.Errorf("stdio: %w", err)
	}
	return nil
}

// Done is closed when the process exits, stdout ends, a fatal framing
// or write failure tears the transport down, or Close finishes.
func (t *stdioTransport) Done() <-chan struct{} { return t.done }

// reply writes a response to a server-initiated request.
func (t *stdioTransport) reply(ctx context.Context, resp *Response) error {
	return t.write(ctx, resp)
}

// stdioCancelReason is the notifications/cancelled reason for a call
// whose ctx ended with err.
func stdioCancelReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "cancelled"
}

// register adds a pending call; false once the transport finished.
func (t *stdioTransport) register(id int64, ch chan *Response) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return false
	}
	t.pending[id] = ch
	return true
}

// unregister removes a pending call and reports whether it was still
// pending (false: a response or finish already claimed it).
func (t *stdioTransport) unregister(id int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.pending[id]; !ok {
		return false
	}
	delete(t.pending, id)
	return true
}

// route hands a response to its pending call, or drops it.
func (t *stdioTransport) route(resp *Response) {
	id, ok := idInt(resp.ID)
	if !ok {
		if !hasID(resp.ID) && resp.Error != nil {
			if t.nullIDWarned.CompareAndSwap(false, true) {
				t.logger.Warn("mcp: stdio server sent an error response with no id; dropping it",
					"pid", t.pid, "code", resp.Error.Code, "message", truncateUTF8(resp.Error.Message, stdioPreviewBytes))
			}
			return
		}
		t.logger.Debug("mcp: dropping response with an unknown id", "id", stdioPreview(resp.ID))
		return
	}
	t.mu.Lock()
	ch, ok := t.pending[id]
	if ok {
		delete(t.pending, id)
	}
	t.mu.Unlock()
	if !ok {
		t.logger.Debug("mcp: dropping late or unknown response", "id", id)
		return
	}
	ch <- resp // capacity 1 and claimed exactly once: never blocks
}

// cause returns why the transport finished, for wrapping into errors
// returned after Done.
func (t *stdioTransport) cause() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.doneCause == nil {
		return errors.New("transport closed")
	}
	return t.doneCause
}

// finish closes Done exactly once: it fails every pending call with
// errConnectionLost (by closing its channel), records cause, and
// cancels the lifetime ctx handed to the hooks.
func (t *stdioTransport) finish(cause error) {
	t.doneOnce.Do(func() {
		t.mu.Lock()
		t.finished = true
		t.doneCause = cause
		pending := t.pending
		t.pending = nil
		t.mu.Unlock()
		for _, ch := range pending {
			close(ch)
		}
		t.lifeCancel()
		close(t.done)
	})
}

// lost finishes the transport because the server went away (stdout
// ended, stdin broke) and stops whatever is left of the process in the
// background. Its exit is still reported as unexpected.
func (t *stdioTransport) lost(cause error) {
	t.finish(cause)
	t.beginShutdown()
}

// fail tears the transport down after a framing failure (an oversize
// message or a partial write), which Thane has already logged at WARN,
// and stops the process in the background.
func (t *stdioTransport) fail(cause error) {
	t.fatal.Store(true)
	t.lost(cause)
}
