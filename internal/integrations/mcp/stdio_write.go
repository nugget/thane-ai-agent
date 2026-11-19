package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// write sends one message as a single compact JSON line (writeFrame
// with no follow-up).
func (t *stdioTransport) write(ctx context.Context, msg any) error {
	return t.writeFrame(ctx, msg, nil)
}

// writeFrame sends one message as a single compact JSON line. Writes are
// serialised on writeSem; the stdin write deadline follows ctx (deadline
// and cancellation). Errors:
//   - ctx ended before any byte was written: ctx.Err();
//   - ctx ended after part of the line was written: ctx.Err() too. A
//     line cut short would break the stream's framing, and a server busy
//     with other work may simply not be reading stdin yet, so the rest
//     of the line is finished in the background (finishFrame), which
//     keeps writeSem until it is done. When cancelID is set (the line
//     is a cancellable request with that id), notifications/cancelled
//     for it follows right after the line, so the server learns at once
//     that the request it is about to read is abandoned;
//   - transport finished, stdin closed, or the process gone with nothing
//     written: errUnreachable (the message was not delivered);
//   - any other partial write: errConnectionLost, and the transport is
//     torn down because the stream framing is broken.
func (t *stdioTransport) writeFrame(ctx context.Context, msg any, cancelID json.RawMessage) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("%w: encode message: %w", errProtocol, err)
	}
	b = append(b, '\n')

	select {
	case t.writeSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-t.done:
		return fmt.Errorf("%w: %w", errUnreachable, t.cause())
	}
	held := true // false once finishFrame owns writeSem
	defer func() {
		if held {
			<-t.writeSem
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-t.done:
		return fmt.Errorf("%w: %w", errUnreachable, t.cause())
	default:
	}

	if dl, ok := ctx.Deadline(); ok {
		_ = t.stdin.SetWriteDeadline(dl)
	}
	fired := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = t.stdin.SetWriteDeadline(time.Now())
		close(fired)
	})
	n, err := t.stdin.Write(b)
	if !stop() {
		<-fired
	}
	_ = t.stdin.SetWriteDeadline(time.Time{})

	// A write deadline only ever comes from ctx.
	ctxEnded := ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrClosed):
		// The stop sequence closed stdin; Done follows the exit.
		if n > 0 {
			return fmt.Errorf("%w: server stdin closed mid-message: %w", errConnectionLost, err)
		}
		return fmt.Errorf("%w: server stdin closed: %w", errUnreachable, err)
	case n > 0 && ctxEnded:
		cerr := stdioCtxErr(ctx)
		held = false
		go t.finishFrame(b, n, t.cancelLine(cancelID, cerr))
		return cerr
	case n > 0:
		cause := fmt.Errorf("partial write to server stdin (%d of %d bytes): %w", n, len(b), err)
		t.logger.Warn("mcp: stdio write was cut short; closing the connection", "pid", t.pid, "error", err)
		t.fail(cause)
		return fmt.Errorf("%w: %w", errConnectionLost, cause)
	case ctxEnded:
		return stdioCtxErr(ctx)
	default:
		// EPIPE and the like: the server is gone or stopped reading.
		cause := fmt.Errorf("write to server stdin: %w", err)
		t.lost(cause)
		return fmt.Errorf("%w: %w", errUnreachable, cause)
	}
}

// cancelLine is the notifications/cancelled line for request id after
// its ctx ended with err, or nil when id is nil.
func (t *stdioTransport) cancelLine(id json.RawMessage, err error) []byte {
	if id == nil {
		return nil
	}
	b, merr := json.Marshal(cancelledNotification(id, stdioCancelReason(err)))
	if merr != nil {
		return nil
	}
	return append(b, '\n')
}

// finishFrame writes the rest of line b, of which sent bytes are already
// on stdin, then follow (if any), and releases writeSem, which the
// caller handed over. The rest of the line gets frameFinishWait, follow
// gets cancelNotifyTimeout. Failing to finish the line breaks the
// stream's framing and tears the transport down: a server that stays
// blocked that long is logged at WARN; a server that went away (EPIPE)
// has its exit logged. A closed stdin means the stop sequence is already
// under way and finishes the transport with its own cause, so it is only
// logged at Debug. A follow-up that cannot be written only logs at
// Debug: the line ended cleanly, so the stream is still framed.
func (t *stdioTransport) finishFrame(b []byte, sent int, follow []byte) {
	defer func() { <-t.writeSem }()
	defer func() { _ = t.stdin.SetWriteDeadline(time.Time{}) }()

	_ = t.stdin.SetWriteDeadline(time.Now().Add(t.frameFinishWait))
	n, err := t.stdin.Write(b[sent:])
	if err != nil {
		cause := fmt.Errorf("partial write to server stdin (%d of %d bytes): %w", sent+n, len(b), err)
		switch {
		case errors.Is(err, os.ErrClosed):
			// Only the stop sequence (Close, lost, fail) or waitProcess
			// after the exit closes stdin, and each finishes the
			// transport with its own cause; this write is a casualty.
			t.logger.Debug("mcp: stdin closed while finishing a stdio write", "pid", t.pid, "error", cause)
		case errors.Is(err, os.ErrDeadlineExceeded):
			t.logger.Warn("mcp: stdio write was cut short; closing the connection",
				"pid", t.pid, "error", err, "waited", t.frameFinishWait)
			t.fail(cause)
		default:
			t.lost(cause)
		}
		return
	}
	if follow == nil {
		return
	}
	_ = t.stdin.SetWriteDeadline(time.Now().Add(cancelNotifyTimeout))
	if _, err := t.stdin.Write(follow); err != nil {
		t.logger.Debug("mcp: sending notifications/cancelled failed", "pid", t.pid, "error", err)
	}
}

// stdioCtxErr returns ctx's error after a write deadline (which only
// ever comes from ctx) fired, allowing for ctx's own timer to lag the
// file deadline by a moment.
func stdioCtxErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return context.DeadlineExceeded
	}
}
