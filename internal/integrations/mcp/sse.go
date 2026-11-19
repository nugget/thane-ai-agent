package mcp

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"time"
)

// sseDefaultEvent is the event type of an SSE event without an "event"
// field. MCP servers send JSON-RPC messages as this type.
const sseDefaultEvent = "message"

// sseLineSlack is how much longer than the event bound one line may
// be, so a maximal "data: <payload>" line (plus a leading BOM) fits.
const sseLineSlack = 64

// sseBOM is the UTF-8 byte order mark, stripped once at stream start.
var sseBOM = []byte{0xEF, 0xBB, 0xBF}

// sseEvent is one dispatched Server-Sent Event.
type sseEvent struct {
	// event is the event type; "message" when the event had no "event"
	// field (or an empty one).
	event string
	// data is the event's data lines joined with "\n". Empty data is
	// still dispatched (e.g. the 2025-11-25 priming event "data:").
	data []byte
	// id is the stream's last event ID at dispatch time. It persists
	// across events until an "id" field changes it.
	id string
	// retry is the last reconnection time the stream announced (0 when
	// none). Recorded only: legacy resumption is deferred.
	retry time.Duration
}

// sseReader parses a text/event-stream body as the WHATWG HTML
// "event stream interpretation" defines it: "event", "data", "id" and
// "retry" fields; ":" comment lines; a leading BOM; CR, LF and CRLF
// line endings; and an incomplete event at EOF discarded. An event whose
// data exceeds max bytes, or a line longer than max plus a small slack
// for the field name, fails with errProtocol.
//
// It never waits for more input once a line terminator has arrived, so
// an event is returned as soon as its blank line is read even if the
// server then pauses (a lone CR is not held back to look for an LF).
type sseReader struct {
	br  *bufio.Reader
	max int

	line    []byte // current line, reused
	started bool   // first line seen (BOM handled)
	skipLF  bool   // last terminator was CR: a leading LF is its pair

	eventType []byte
	data      []byte
	hasData   bool // at least one "data" field since the last dispatch
	lastID    string
	retry     time.Duration
}

// newSSEReader returns a parser over r. max bounds one line and one
// event's data; max <= 0 means maxMessageBytes.
func newSSEReader(r io.Reader, max int) *sseReader {
	if max <= 0 {
		max = maxMessageBytes
	}
	return &sseReader{br: bufio.NewReaderSize(r, 64<<10), max: max}
}

// next returns the next dispatched event. It returns io.EOF at a clean
// end of stream (any incomplete trailing event is dropped, as the SSE
// standard requires), errProtocol for an oversize line or event, and
// any other read error unchanged.
func (s *sseReader) next() (sseEvent, error) {
	for {
		line, err := s.readLine()
		if err != nil {
			return sseEvent{}, err
		}
		if len(line) == 0 {
			if ev, ok := s.dispatch(); ok {
				return ev, nil
			}
			continue
		}
		if err := s.field(line); err != nil {
			return sseEvent{}, err
		}
	}
}

// dispatch ends the current event. It reports false when there is
// nothing to dispatch (no data field since the last blank line); the
// event type is reset either way.
func (s *sseReader) dispatch() (sseEvent, bool) {
	typ := string(s.eventType)
	s.eventType = s.eventType[:0]
	if !s.hasData {
		return sseEvent{}, false
	}
	data := s.data
	if n := len(data); n > 0 && data[n-1] == '\n' {
		data = data[:n-1]
	}
	if typ == "" {
		typ = sseDefaultEvent
	}
	ev := sseEvent{
		event: typ,
		data:  bytes.Clone(data),
		id:    s.lastID,
		retry: s.retry,
	}
	if ev.data == nil {
		ev.data = []byte{}
	}
	s.data = s.data[:0]
	s.hasData = false
	return ev, true
}

// field processes one non-empty line.
func (s *sseReader) field(line []byte) error {
	if line[0] == ':' {
		return nil // comment / keep-alive
	}
	name, value := line, []byte(nil)
	if i := bytes.IndexByte(line, ':'); i >= 0 {
		name, value = line[:i], line[i+1:]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
	}
	switch string(name) {
	case "event":
		s.eventType = append(s.eventType[:0], value...)
	case "data":
		if len(s.data)+len(value) > s.max {
			return fmt.Errorf("%w: SSE event exceeds %d bytes", errProtocol, s.max)
		}
		s.data = append(s.data, value...)
		s.data = append(s.data, '\n')
		s.hasData = true
	case "id":
		if bytes.IndexByte(value, 0) < 0 {
			s.lastID = string(value)
		}
	case "retry":
		if ms, ok := sseDigits(value); ok {
			s.retry = time.Duration(ms) * time.Millisecond
		}
	}
	return nil
}

// sseDigits parses a retry value: ASCII digits only, else ignored.
func sseDigits(v []byte) (int64, bool) {
	if len(v) == 0 {
		return 0, false
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(string(v), 10, 64)
	if err != nil || n > int64(time.Duration(1<<63-1)/time.Millisecond) {
		return 0, false
	}
	return n, true
}

// readLine returns the next line without its terminator. The returned
// slice is valid until the next call. At EOF it returns io.EOF even if
// a partial line was read, because a line without a terminator can
// only belong to an incomplete (discarded) event.
func (s *sseReader) readLine() ([]byte, error) {
	s.line = s.line[:0]
	for {
		if s.br.Buffered() == 0 {
			if _, err := s.br.Peek(1); err != nil {
				return nil, err
			}
		}
		chunk, _ := s.br.Peek(s.br.Buffered())
		if s.skipLF {
			s.skipLF = false
			if chunk[0] == '\n' {
				_, _ = s.br.Discard(1)
				continue
			}
		}
		i := bytes.IndexAny(chunk, "\r\n")
		if i < 0 {
			if err := s.appendLine(chunk); err != nil {
				return nil, err
			}
			_, _ = s.br.Discard(len(chunk))
			continue
		}
		if err := s.appendLine(chunk[:i]); err != nil {
			return nil, err
		}
		s.skipLF = chunk[i] == '\r'
		_, _ = s.br.Discard(i + 1)
		if !s.started {
			s.started = true
			s.line = bytes.TrimPrefix(s.line, sseBOM)
		}
		return s.line, nil
	}
}

// appendLine grows the current line, enforcing the size bound.
func (s *sseReader) appendLine(b []byte) error {
	if len(s.line)+len(b) > s.max+sseLineSlack {
		return fmt.Errorf("%w: SSE line exceeds %d bytes", errProtocol, s.max)
	}
	s.line = append(s.line, b...)
	return nil
}
