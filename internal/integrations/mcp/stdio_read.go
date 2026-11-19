package mcp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

const (
	stdioReadBufferBytes   = 64 << 10 // bufio size for stdout
	stdioRetainBufferBytes = 1 << 20  // drop a line buffer that grew past this
	stdioPreviewBytes      = 256      // unparseable-line preview in logs
	stdioStderrLineBytes   = 4 << 10  // stderr lines are truncated to this
	stdioStderrTailLines   = 20       // stderr lines kept for WARN logs
)

// readStdout is the single long-lived stdout reader. It decodes each
// line (one message or a batch), routes responses to pending calls and
// hands requests and notifications to dispatchInbound. Blank lines are
// skipped, unparseable lines and batches over maxInboundBatch are
// logged and skipped, and a line over maxMessageBytes tears the
// transport down with errProtocol. EOF or a read error finishes the
// transport with the connection lost.
func (t *stdioTransport) readStdout() {
	defer close(t.readerDone)
	br := bufio.NewReaderSize(t.stdout, stdioReadBufferBytes)
	var buf []byte
	for {
		line, tooLong, err := stdioReadLine(br, buf[:0], maxMessageBytes)
		if tooLong {
			t.logger.Warn("mcp: stdio server sent a message over the size limit; closing the connection",
				"pid", t.pid, "limit_bytes", maxMessageBytes, "stderr_tail", t.stderrTail.String())
			t.fail(fmt.Errorf("%w: stdout message exceeds %d bytes", errProtocol, maxMessageBytes))
			return
		}
		if len(line) > 0 {
			t.handleLine(line)
		}
		if err != nil {
			t.lost(stdioReadCause(err))
			return
		}
		buf = line
		if cap(buf) > stdioRetainBufferBytes {
			buf = nil
		}
	}
}

// stdioReadCause describes why stdout ended.
func stdioReadCause(err error) error {
	switch {
	case errors.Is(err, io.EOF):
		return errors.New("server closed stdout")
	case errors.Is(err, os.ErrClosed):
		return errors.New("server process exited")
	default:
		return fmt.Errorf("read server stdout: %w", err)
	}
}

// stdioReadLine appends the next '\n'-terminated line to buf and returns
// it with the newline. The bool reports a line whose content exceeds limit
// bytes (the rest of it is left unread). At EOF the unterminated tail,
// if any, is returned together with the error.
func stdioReadLine(br *bufio.Reader, buf []byte, limit int) ([]byte, bool, error) {
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		n := len(buf)
		if n > 0 && buf[n-1] == '\n' {
			n--
		}
		if n > limit {
			return nil, true, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return buf, false, err
		}
	}
}

// handleLine decodes and dispatches one stdout line.
func (t *stdioTransport) handleLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	msgs, err := decodeMessages(line)
	if err != nil {
		t.logger.Debug("mcp: skipping unparseable stdout line from stdio server",
			"pid", t.pid, "bytes", len(line), "preview", stdioPreview(line), "error", err)
		return
	}
	if t.hooks.inbound.batchTooLong(len(msgs), t.logger, "pid", t.pid) {
		return
	}
	for i := range msgs {
		m := &msgs[i]
		switch m.kind() {
		case kindResponse:
			t.markLive()
			t.route(m.response())
		case kindRequest, kindNotification:
			t.markLive()
			dispatchInbound(t.lifeCtx, t.hooks, m, t.reply, t.logger)
		default:
			t.logger.Debug("mcp: skipping invalid JSON-RPC message from stdio server", "pid", t.pid, "preview", stdioPreview(line))
		}
	}
}

// stdioPreview returns a rune-safe prefix of b for logs.
func stdioPreview(b []byte) string {
	if len(b) > stdioPreviewBytes+stdioRuneSlack {
		b = b[:stdioPreviewBytes+stdioRuneSlack]
	}
	return truncateUTF8(strings.ToValidUTF8(string(b), "�"), stdioPreviewBytes)
}

// stdioRuneSlack is the longest UTF-8 encoding of one rune; a preview
// cut keeps this much slack so truncateUTF8 can end on a rune boundary.
const stdioRuneSlack = 4

// drainStderr reads stderr until it ends, never stopping on long lines:
// each line is cut to stdioStderrLineBytes, logged at Debug and kept in
// the tail ring that exit and failure WARNs include. Draining keeps a
// chatty server from blocking on a full stderr pipe.
func (t *stdioTransport) drainStderr() {
	defer close(t.stderrDone)
	br := bufio.NewReaderSize(t.stderr, stdioStderrLineBytes)
	line := make([]byte, 0, stdioStderrLineBytes)
	truncated := false
	for {
		chunk, err := br.ReadSlice('\n')
		if err == nil {
			chunk = chunk[:len(chunk)-1]
		}
		if room := stdioStderrLineBytes - len(line); room >= len(chunk) {
			line = append(line, chunk...)
		} else {
			line = append(line, chunk[:max(room, 0)]...)
			truncated = true
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		t.recordStderr(line, truncated)
		line, truncated = line[:0], false
		if err != nil {
			return
		}
	}
}

// recordStderr logs one stderr line at Debug and keeps it in the ring.
func (t *stdioTransport) recordStderr(line []byte, truncated bool) {
	text := strings.TrimSpace(strings.ToValidUTF8(string(line), "�"))
	if text == "" {
		return
	}
	if truncated {
		text = truncateUTF8(text, stdioStderrLineBytes) + " …[truncated]"
	}
	t.logger.Debug("mcp: stdio server stderr", "pid", t.pid, "line", text)
	t.stderrTail.add(text)
}

// stdioRing keeps the last stdioStderrTailLines stderr lines.
type stdioRing struct {
	mu    sync.Mutex
	lines [stdioStderrTailLines]string
	next  int
	count int
}

// add appends a line, evicting the oldest when full.
func (r *stdioRing) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines[r.next] = line
	r.next = (r.next + 1) % len(r.lines)
	if r.count < len(r.lines) {
		r.count++
	}
}

// String returns the kept lines, oldest first, joined by newlines.
func (r *stdioRing) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, r.count)
	start := (r.next - r.count + len(r.lines)) % len(r.lines)
	for i := range r.count {
		out = append(out, r.lines[(start+i)%len(r.lines)])
	}
	return strings.Join(out, "\n")
}
