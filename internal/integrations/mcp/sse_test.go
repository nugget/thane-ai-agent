package mcp

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// httpTEvent is a comparable view of an sseEvent.
type httpTEvent struct {
	event, data, id string
	retry           time.Duration
}

func httpTCollect(r *sseReader) ([]httpTEvent, error) {
	var out []httpTEvent
	for {
		ev, err := r.next()
		if err != nil {
			return out, err
		}
		out = append(out, httpTEvent{event: ev.event, data: string(ev.data), id: ev.id, retry: ev.retry})
	}
}

func TestSSE_Parse(t *testing.T) {
	msg := func(data string) httpTEvent { return httpTEvent{event: "message", data: data} }
	tests := []struct {
		name    string
		in      string
		max     int
		want    []httpTEvent
		wantErr error // io.EOF when nil
	}{
		{name: "lf", in: "data: hello\n\n", want: []httpTEvent{msg("hello")}},
		{name: "crlf", in: "data: a\r\n\r\ndata: b\r\n\r\n", want: []httpTEvent{msg("a"), msg("b")}},
		{name: "cr only", in: "data: a\r\rdata: b\r\r", want: []httpTEvent{msg("a"), msg("b")}},
		{name: "mixed endings", in: "data: a\r\n\ndata: b\r\r\ndata: c\n\r", want: []httpTEvent{msg("a"), msg("b"), msg("c")}},
		{name: "multi-line data", in: "data: line1\ndata: line2\ndata\n\n", want: []httpTEvent{msg("line1\nline2\n")}},
		{name: "comments and keep-alives", in: ": keepalive\n:\ndata: x\n: mid-event\n\n:\n\n", want: []httpTEvent{msg("x")}},
		{name: "leading bom stripped", in: "\xEF\xBB\xBFdata: x\n\n", want: []httpTEvent{msg("x")}},
		{name: "bom on empty first line", in: "\xEF\xBB\xBF\ndata: x\n\n", want: []httpTEvent{msg("x")}},
		{name: "only one bom stripped", in: "\xEF\xBB\xBF\xEF\xBB\xBFdata: x\n\ndata: y\n\n", want: []httpTEvent{msg("y")}},
		{name: "bom later is not stripped", in: "data: a\n\n\xEF\xBB\xBFdata: b\n\n", want: []httpTEvent{msg("a")}},
		{
			name: "event type and reset",
			in:   "event: endpoint\ndata: /messages\n\ndata: plain\n\nevent: foo\n\ndata: after\n\n",
			want: []httpTEvent{{event: "endpoint", data: "/messages"}, msg("plain"), msg("after")},
		},
		{name: "empty event field is message", in: "event:\ndata: x\n\n", want: []httpTEvent{msg("x")}},
		{
			name: "id persists until changed",
			in:   "id: 1\ndata: a\n\ndata: b\n\nid\ndata: c\n\n",
			want: []httpTEvent{{event: "message", data: "a", id: "1"}, {event: "message", data: "b", id: "1"}, msg("c")},
		},
		{
			name: "id without data updates last id only",
			in:   "id: 7\n\nid: a\x00b\ndata: x\n\n",
			want: []httpTEvent{{event: "message", data: "x", id: "7"}},
		},
		{
			name: "retry digits only",
			in:   "retry: 1500\ndata: a\n\nretry: 1x\ndata: b\n\nretry:\ndata: c\n\nretry: 99999999999999999999\ndata: d\n\n",
			want: []httpTEvent{
				{event: "message", data: "a", retry: 1500 * time.Millisecond},
				{event: "message", data: "b", retry: 1500 * time.Millisecond},
				{event: "message", data: "c", retry: 1500 * time.Millisecond},
				{event: "message", data: "d", retry: 1500 * time.Millisecond},
			},
		},
		{name: "blank lines dispatch nothing", in: "\n\n\r\n\r\r", want: nil},
		{name: "priming event has empty data", in: "id: 42\ndata:\n\n", want: []httpTEvent{{event: "message", data: "", id: "42"}}},
		{name: "one leading space stripped", in: "data:x\n\ndata:  two\n\ndata\n\n", want: []httpTEvent{msg("x"), msg(" two"), msg("")}},
		{name: "unknown fields ignored", in: "foo: bar\nDATA: no\nData: no\ndata: yes\n\n", want: []httpTEvent{msg("yes")}},
		{name: "colons in value kept", in: "data: {\"a\":1,\"b\":\"x:y\"}\n\n", want: []httpTEvent{msg(`{"a":1,"b":"x:y"}`)}},
		{name: "incomplete event at eof dropped", in: "data: a\n\ndata: b\n", want: []httpTEvent{msg("a")}},
		{name: "unterminated line at eof dropped", in: "data: a", want: nil},
		{name: "empty stream", in: "", want: nil},
		{name: "data at max", in: "data: 12345678\n\n", max: 8, want: []httpTEvent{msg("12345678")}},
		{name: "multi-line data at max", in: "data: 1234\ndata: 567\n\n", max: 8, want: []httpTEvent{msg("1234\n567")}},
		{name: "data over max", in: "data: 123456789\n\n", max: 8, wantErr: errProtocol},
		{name: "accumulated data over max", in: "data: 12345\ndata: 678\n\n", max: 8, wantErr: errProtocol},
		{name: "line over max", in: ":" + strings.Repeat("a", 100) + "\n\ndata: x\n\n", max: 8, wantErr: errProtocol},
		{name: "event before oversize is returned", in: "data: ok\n\ndata: 123456789\n\n", max: 8, want: []httpTEvent{msg("ok")}, wantErr: errProtocol},
	}
	readers := map[string]func(string) io.Reader{
		"whole":    func(s string) io.Reader { return strings.NewReader(s) },
		"one byte": func(s string) io.Reader { return iotest.OneByteReader(strings.NewReader(s)) },
		"half":     func(s string) io.Reader { return iotest.HalfReader(strings.NewReader(s)) },
	}
	for _, tt := range tests {
		for rname, mk := range readers {
			t.Run(tt.name+"/"+rname, func(t *testing.T) {
				got, err := httpTCollect(newSSEReader(mk(tt.in), tt.max))
				wantErr := tt.wantErr
				if wantErr == nil {
					wantErr = io.EOF
				}
				if !errors.Is(err, wantErr) {
					t.Fatalf("err = %v, want %v", err, wantErr)
				}
				if len(got) != len(tt.want) {
					t.Fatalf("events = %+v, want %+v", got, tt.want)
				}
				for i := range got {
					if got[i] != tt.want[i] {
						t.Errorf("event %d = %+v, want %+v", i, got[i], tt.want[i])
					}
				}
			})
		}
	}
}

// A lone CR ends the line at once: the reader must not wait for a
// possible LF before dispatching, or a server that pauses after an
// event (e.g. waiting for the reply to its request) would deadlock.
func TestSSE_CRDoesNotWaitForMoreInput(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	r := newSSEReader(pr, 0)
	go func() { _, _ = io.WriteString(pw, "data: x\r\r") }()

	got := make(chan sseEvent, 1)
	go func() {
		ev, err := r.next()
		if err == nil {
			got <- ev
		}
		close(got)
	}()
	select {
	case ev := <-got:
		if string(ev.data) != "x" {
			t.Fatalf("data = %q, want x", ev.data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("next() blocked after a CR-terminated blank line")
	}

	// The LF that may follow a CR belongs to it and adds no blank line.
	go func() {
		_, _ = io.WriteString(pw, "\ndata: y\r\n\r\n")
		_ = pw.Close()
	}()
	events, err := httpTCollect(r)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want EOF", err)
	}
	if len(events) != 1 || events[0].data != "y" {
		t.Fatalf("events = %+v, want one event y", events)
	}
}

func TestSSE_ReadErrorPassesThrough(t *testing.T) {
	boom := errors.New("boom")
	r := newSSEReader(io.MultiReader(strings.NewReader("data: a\n\ndata: b\n"), iotest.ErrReader(boom)), 0)
	events, err := httpTCollect(r)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if len(events) != 1 || events[0].data != "a" {
		t.Fatalf("events = %+v, want one event a", events)
	}
}

func TestSSE_DefaultMax(t *testing.T) {
	if r := newSSEReader(strings.NewReader(""), 0); r.max != maxMessageBytes {
		t.Fatalf("max = %d, want maxMessageBytes", r.max)
	}
}
