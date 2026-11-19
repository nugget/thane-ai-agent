package mcp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"syscall"
	"time"
)

// Close shuts the server down, bounded by ctx: close stdin, wait up to
// closeStdinWait for the process to exit, SIGTERM the process group,
// wait up to termWait, then SIGKILL the group. If ctx ends first the
// group is killed at once. Close then waits for Done (bounded by ctx)
// and closes it if it is still open. Idempotent, safe to call
// concurrently and after Done; always returns nil.
func (t *stdioTransport) Close(ctx context.Context) error {
	t.mu.Lock()
	if !t.finished {
		t.closeRequested = true
	}
	t.mu.Unlock()

	t.beginShutdown()
	select {
	case <-t.exited:
	case <-ctx.Done():
		t.logger.Debug("mcp: close deadline reached; killing stdio server process group", "pid", t.pid)
		t.signalIfRunning(syscall.SIGKILL)
	}
	select {
	case <-t.done:
	case <-ctx.Done():
	}
	t.finish(errStdioClosed)
	return nil
}

// beginShutdown starts the graceful stop sequence once, in the
// background. It runs for at most closeStdinWait + termWait.
func (t *stdioTransport) beginShutdown() {
	t.shutdownOnce.Do(func() { go t.shutdown() })
}

// shutdown is the spec's stdio stop sequence: close stdin, wait, SIGTERM
// the group, wait, SIGKILL the group.
func (t *stdioTransport) shutdown() {
	t.closeStdin()
	if t.awaitExit(t.closeStdinWait) {
		return
	}
	t.logger.Debug("mcp: stdio server still running after stdin closed; sending SIGTERM", "pid", t.pid)
	t.signalIfRunning(syscall.SIGTERM)
	if t.awaitExit(t.termWait) {
		return
	}
	t.logger.Debug("mcp: stdio server still running after SIGTERM; sending SIGKILL", "pid", t.pid)
	t.signalIfRunning(syscall.SIGKILL)
}

// closeStdin closes the parent's end of the child's stdin once. A write
// blocked on a full pipe returns os.ErrClosed.
func (t *stdioTransport) closeStdin() {
	t.stdinOnce.Do(func() { _ = t.stdin.Close() })
}

// awaitExit waits up to d for the process to be reaped.
func (t *stdioTransport) awaitExit(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-t.exited:
		return true
	case <-timer.C:
		return false
	}
}

// signalIfRunning signals the process group while its leader has not
// been reaped yet. After that, waitProcess has already killed the group.
func (t *stdioTransport) signalIfRunning(sig syscall.Signal) {
	select {
	case <-t.exited:
		return
	default:
	}
	t.signalGroup(sig)
}

// signalGroup signals the whole process group. ESRCH (already gone) is
// success.
func (t *stdioTransport) signalGroup(sig syscall.Signal) {
	if err := syscall.Kill(-t.pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.logger.Debug("mcp: signalling stdio server process group failed", "pid", t.pid, "signal", sig.String(), "error", err)
	}
}

// waitProcess reaps the process. Once Wait returns it kills the rest of
// the process group (a grandchild holding stdout open would otherwise
// keep the reader blocked forever, #1167), lets the stdout reader drain
// to EOF for up to exitDrainWait, finishes the transport, then closes
// the parent's read ends so both readers return, and logs the exit.
func (t *stdioTransport) waitProcess() {
	err := t.cmd.Wait()
	t.exitErr = err
	close(t.exited)
	t.signalGroup(syscall.SIGKILL)

	expired := make(chan struct{})
	timer := time.AfterFunc(t.exitDrainWait, func() { close(expired) })
	select {
	case <-t.readerDone:
	case <-expired:
	}
	t.finish(fmt.Errorf("server process exited (%s)", stdioExitText(err)))
	select {
	case <-t.stderrDone:
	case <-expired:
	}
	timer.Stop()
	_ = t.stdout.Close()
	_ = t.stderr.Close()
	<-t.readerDone
	<-t.stderrDone
	t.closeStdin()
	t.logExit(err)
}

// logExit reports the exit once: Debug for an exit Close asked for, WARN
// with the stderr tail when the process exited on its own or was closed
// before it ever spoke JSON-RPC (usually a start-up failure). With a
// shared hooks.exitWarned, only the first such exit of a run of failures
// is a WARN; the supervisor retries a broken server every backoff, and
// the same stderr tail at WARN each time would only bury it.
func (t *stdioTransport) logExit(err error) {
	t.mu.Lock()
	requested := t.closeRequested
	t.mu.Unlock()
	status := stdioExitText(err)
	switch {
	case requested && t.sawMessage.Load():
		t.logger.Debug("mcp: stdio server exited", "pid", t.pid, "exit", status)
	case requested:
		t.warnExit("mcp: stdio server was closed before it sent any JSON-RPC message", status)
	case t.fatal.Load():
		t.logger.Debug("mcp: stdio server stopped after a transport failure", "pid", t.pid, "exit", status)
	default:
		t.warnExit("mcp: stdio server exited unexpectedly", status)
	}
}

// warnExit logs an exit the operator should see, with the stderr tail:
// at WARN unless an earlier transport of this server already warned in
// the current run of failures, then at Debug.
func (t *stdioTransport) warnExit(msg, status string) {
	level := slog.LevelDebug
	if gate := t.hooks.exitWarned; gate == nil || gate.CompareAndSwap(false, true) {
		level = slog.LevelWarn
	}
	t.logger.Log(context.Background(), level, msg, "pid", t.pid, "exit", status, "stderr_tail", t.stderrTail.String())
}

// markLive records that the server sent a valid JSON-RPC message. It
// does not re-arm the shared exit WARN: a server that answers the
// handshake and then crashes is still failing, and only the client knows
// when a connection has lasted long enough to end the run of failures.
func (t *stdioTransport) markLive() {
	t.sawMessage.Store(true)
}

// stdioExitText describes cmd.Wait's result for logs: "exit status 0",
// "exit status 3", "signal: killed", ...
func stdioExitText(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}
