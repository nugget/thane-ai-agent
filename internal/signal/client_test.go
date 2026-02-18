package signal

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// pipeClient creates a Client wired to in-memory pipes instead of a
// real subprocess. The returned writer feeds the client's stdin (from
// the subprocess's perspective: stdout). The returned reader receives
// what the client writes to the subprocess's stdin.
func pipeClient(t *testing.T) (*Client, io.Writer, io.Reader) {
	t.Helper()

	// Pipe 1: client reads from this (simulates subprocess stdout).
	outR, outW := io.Pipe()

	// Pipe 2: client writes to this (simulates subprocess stdin).
	inR, inW := io.Pipe()

	c := &Client{
		command:  "fake",
		args:     nil,
		logger:   slog.Default(),
		stdin:    inW,
		reader:   bufio.NewReaderSize(outR, 1<<20),
		pending:  make(map[int64]chan rpcResponse),
		messages: make(chan *Envelope, 64),
		done:     make(chan struct{}),
		waitErr:  make(chan error, 1),
	}

	go c.readLoop()

	t.Cleanup(func() {
		outW.Close()
		inW.Close()
	})

	return c, outW, inR
}

func TestClient_ReceiveDataMessage(t *testing.T) {
	client, stdout, _ := pipeClient(t)

	notif := `{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"source":"+15551234567","sourceNumber":"+15551234567","sourceName":"Alice","timestamp":1631458508784,"dataMessage":{"timestamp":1631458508784,"message":"Hello!"}}}}` + "\n"

	if _, err := io.WriteString(stdout, notif); err != nil {
		t.Fatalf("write notification: %v", err)
	}

	select {
	case env := <-client.Messages():
		if env.Source != "+15551234567" {
			t.Errorf("source = %q, want +15551234567", env.Source)
		}
		if env.SourceName != "Alice" {
			t.Errorf("sourceName = %q, want Alice", env.SourceName)
		}
		if env.Timestamp != 1631458508784 {
			t.Errorf("timestamp = %d, want 1631458508784", env.Timestamp)
		}
		if env.DataMessage == nil {
			t.Fatal("expected non-nil DataMessage")
		}
		if env.DataMessage.Message != "Hello!" {
			t.Errorf("message = %q, want Hello!", env.DataMessage.Message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
	}
}

func TestClient_ReceiveSkipsNonDataMessages(t *testing.T) {
	client, stdout, _ := pipeClient(t)

	// Receipt notification — should not appear on Messages channel.
	receipt := `{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"source":"+15551234567","timestamp":1631458508784,"receiptMessage":{"when":1631458510000,"type":"DELIVERY","timestamps":[1631458508784]}}}}` + "\n"
	// Data message — should appear.
	data := `{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"source":"+15559999999","timestamp":1631458509000,"dataMessage":{"timestamp":1631458509000,"message":"Real message"}}}}` + "\n"

	if _, err := io.WriteString(stdout, receipt+data); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case env := <-client.Messages():
		if env.Source != "+15559999999" {
			t.Errorf("source = %q, want +15559999999 (receipt should have been skipped)", env.Source)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for data message")
	}
}

func TestClient_RequestResponse(t *testing.T) {
	client, stdout, stdin := pipeClient(t)

	var wg sync.WaitGroup
	wg.Add(1)

	// Read the request from stdin, then write a response to stdout.
	go func() {
		defer wg.Done()
		reader := bufio.NewReader(stdin)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}

		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			t.Errorf("unmarshal request: %v", err)
			return
		}

		if req.Method != "version" {
			t.Errorf("method = %q, want version", req.Method)
		}

		resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"version":"0.13.0"}}`, req.ID) + "\n"
		if _, err := io.WriteString(stdout, resp); err != nil {
			t.Errorf("write response: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.Ping(ctx)
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}

	wg.Wait()
}

func TestClient_SendMessage(t *testing.T) {
	client, stdout, stdin := pipeClient(t)

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		reader := bufio.NewReader(stdin)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}

		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			t.Errorf("unmarshal request: %v", err)
			return
		}

		if req.Method != "send" {
			t.Errorf("method = %q, want send", req.Method)
		}

		params, _ := json.Marshal(req.Params)
		var p map[string]any
		json.Unmarshal(params, &p)

		recipients, ok := p["recipient"].([]any)
		if !ok || len(recipients) != 1 || recipients[0] != "+15551234567" {
			t.Errorf("recipient = %v, want [+15551234567]", p["recipient"])
		}
		if p["message"] != "Hello back!" {
			t.Errorf("message = %v, want Hello back!", p["message"])
		}

		resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"timestamp":1631458509000}}`, req.ID) + "\n"
		if _, err := io.WriteString(stdout, resp); err != nil {
			t.Errorf("write response: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ts, err := client.Send(ctx, "+15551234567", "Hello back!")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if ts != 1631458509000 {
		t.Errorf("timestamp = %d, want 1631458509000", ts)
	}

	wg.Wait()
}

func TestClient_ContextCancellation(t *testing.T) {
	client, _, _ := pipeClient(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := client.call(ctx, "version", nil)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestClient_SubprocessExit(t *testing.T) {
	client, stdout, _ := pipeClient(t)

	// Close stdout to simulate subprocess exit.
	stdout.(io.Closer).Close()

	// The done channel should be closed.
	select {
	case <-client.done:
		// Expected.
	case <-time.After(2 * time.Second):
		t.Fatal("done channel not closed after subprocess exit")
	}

	// Messages channel should also be closed.
	select {
	case _, ok := <-client.Messages():
		if ok {
			t.Error("expected messages channel to be closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("messages channel not closed after subprocess exit")
	}
}
