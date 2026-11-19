package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// stdioTScriptParams is the union of the params the scripted modes read.
type stdioTScriptParams struct {
	MS       int    `json:"ms"`
	Token    int    `json:"token"`
	Late     bool   `json:"late"`
	Name     string `json:"name"`
	Bytes    int    `json:"bytes"`
	Count    int    `json:"count"`
	Requests []struct {
		ID     json.RawMessage `json:"id"`
		Self   bool            `json:"self"`
		Method string          `json:"method"`
		WaitMS int             `json:"wait_ms"`
	} `json:"requests"`
}

// stdioTStats is the "stats" result.
type stdioTStats struct {
	Cancelled   []stdioTCancel             `json:"cancelled"`
	Slept       int                        `json:"slept"`
	Held        int                        `json:"held"`
	Replies     map[string]json.RawMessage `json:"replies"`
	Initialized bool                       `json:"initialized"`
	Received    []string                   `json:"received"`
}

// stdioTAsked is one server-initiated request and the reply it got
// (null when none arrived in time).
type stdioTAsked struct {
	ID    json.RawMessage `json:"id"`
	Reply json.RawMessage `json:"reply"`
}

// answerScript serves the "stdio-*" test modes. Each method scripts one
// behaviour the transport must cope with.
func (s *stdioTServer) answerScript(req *Request) {
	var p stdioTScriptParams
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
	}
	switch req.Method {
	case "echo":
		params := req.Params
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		s.result(req.ID, params)
	case "env":
		cwd, _ := os.Getwd()
		s.result(req.ID, map[string]string{"value": os.Getenv(p.Name), "cwd": cwd})
	case "sleep":
		cancelled := s.sleep(req.ID, p.MS)
		if cancelled && !p.Late {
			return
		}
		s.result(req.ID, map[string]any{"token": p.Token, "cancelled": cancelled})
		s.mu.Lock()
		s.slept++
		s.mu.Unlock()
	case "stats":
		s.mu.Lock()
		st := stdioTStats{Cancelled: s.cancelled, Slept: s.slept, Held: len(s.held), Replies: s.replies, Initialized: s.initialized,
			Received: s.received}
		raw, _ := json.Marshal(st)
		s.mu.Unlock()
		s.result(req.ID, json.RawMessage(raw))
	case "notify":
		s.send(&Notification{JSONRPC: jsonrpcVersion, Method: methodLogMessage, Params: json.RawMessage(`{"level":"info","data":"hello"}`)})
		s.send(&Notification{JSONRPC: jsonrpcVersion, Method: methodToolsListChanged})
		s.result(req.ID, map[string]any{})
	case "ask":
		s.ask(req, &p)
	case "batch":
		s.writeLine([]byte(fmt.Sprintf(`[{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"in a batch"}},`+
			`{"jsonrpc":"2.0","id":"b1","method":"ping"},`+
			`{"jsonrpc":"2.0","id":%s,"result":{"batched":true}}]`, req.ID)))
	case "bigbatch": // a batch of count notifications, then the result
		parts := make([]string, p.Count)
		for i := range parts {
			parts[i] = `{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"debug","data":"n"}}`
		}
		s.writeLine([]byte("[" + strings.Join(parts, ",") + "]"))
		s.result(req.ID, map[string]any{})
	case "hold":
		s.mu.Lock()
		s.held = append(s.held, req.ID)
		s.mu.Unlock()
	case "release":
		s.mu.Lock()
		held := s.held
		s.held = nil
		s.mu.Unlock()
		parts := make([]string, 0, len(held)+1)
		for _, id := range held {
			parts = append(parts, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"held_id":%s}}`, id, id))
		}
		parts = append(parts, fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"released":%d}}`, req.ID, len(held)))
		s.writeLine([]byte("[" + strings.Join(parts, ",") + "]"))
	case "crash":
		fmt.Fprintln(os.Stderr, "boom: simulated crash")
		os.Exit(3)
	case "oversize":
		prefix := []byte(`{"jsonrpc":"2.0","method":"notifications/message","params":{"data":"`)
		suffix := []byte(`"}}`)
		pad := maxMessageBytes + 1 - len(prefix) - len(suffix)
		s.writeLine(append(append(prefix, bytes.Repeat([]byte("x"), pad)...), suffix...))
	case "garbage":
		s.writeLine([]byte("this is not json"))
		s.writeLine([]byte("   "))
		s.writeLine([]byte("[]"))
		s.writeLine([]byte(`{"jsonrpc":"2.0"}`))
		s.result(req.ID, map[string]any{"ok": true})
	case "nullid":
		s.writeLine([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`))
		s.writeLine([]byte(`{"jsonrpc":"2.0","error":{"code":-32700,"message":"parse error"}}`))
		s.writeLine([]byte(`{"jsonrpc":"2.0","id":"nobody","result":{}}`))
		s.result(req.ID, map[string]any{"ok": true})
	case "flood":
		s.mu.Lock()
		s.floodDone = make(chan struct{})
		done := s.floodDone
		s.mu.Unlock()
		go stdioTFlood(p.Bytes, done)
		s.result(req.ID, map[string]any{})
	case "flood-wait":
		s.mu.Lock()
		done := s.floodDone
		s.mu.Unlock()
		<-done
		s.result(req.ID, map[string]any{})
	case "exit-orphan":
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), stdioTHelperEnv+"=stdio-hold")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			s.fail(req.ID, -32000, err.Error(), nil)
			return
		}
		s.send(&Notification{JSONRPC: jsonrpcVersion, Method: "test/grandchild", Params: json.RawMessage(fmt.Sprintf(`{"pid":%d}`, cmd.Process.Pid))})
		os.Exit(0)
	default:
		s.fail(req.ID, codeMethodNotFound, "Method not found", req.Method)
	}
}

// ask sends the scripted server-initiated requests, waits for each
// reply, and answers req with what came back.
func (s *stdioTServer) ask(req *Request, p *stdioTScriptParams) {
	type outstanding struct {
		id   json.RawMessage
		ch   chan json.RawMessage
		wait time.Duration
	}
	var outs []outstanding
	for _, r := range p.Requests {
		id := r.ID
		if r.Self {
			id = req.ID
		}
		ch := make(chan json.RawMessage, 1)
		s.mu.Lock()
		s.waiting[stdioTKey(id)] = ch
		s.mu.Unlock()
		wait := 5 * time.Second
		if r.WaitMS > 0 {
			wait = time.Duration(r.WaitMS) * time.Millisecond
		}
		outs = append(outs, outstanding{id: id, ch: ch, wait: wait})
		s.send(&Request{JSONRPC: jsonrpcVersion, ID: id, Method: r.Method, Params: json.RawMessage(`{}`)})
	}
	asked := make([]stdioTAsked, 0, len(outs))
	for _, o := range outs {
		a := stdioTAsked{ID: o.id, Reply: json.RawMessage("null")}
		select {
		case raw := <-o.ch:
			a.Reply = raw
		case <-time.After(o.wait):
		}
		asked = append(asked, a)
	}
	s.result(req.ID, map[string]any{"replies": asked})
}

// stdioTFlood writes total bytes of stderr: short lines, then one very
// long line, then a marker line, and closes done.
func stdioTFlood(total int, done chan struct{}) {
	defer close(done)
	line := []byte(strings.Repeat("e", 511) + "\n")
	for written := 0; written < total; written += len(line) {
		if _, err := os.Stderr.Write(line); err != nil {
			return
		}
	}
	_, _ = os.Stderr.Write(append(bytes.Repeat([]byte("L"), 1<<20), '\n'))
	_, _ = os.Stderr.Write([]byte("flood done\n"))
}
