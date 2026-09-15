package signal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/model/prompts"
	"github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

const holdTestSender = "+15551234567"

// syncBuffer is a goroutine-safe log sink: the end-to-end tests log from
// the sender loop's goroutine while the test goroutine reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// rpcCapture answers every signal-cli JSON-RPC request with success and
// records it. Client.Send waits for that answer, so a send made inside
// signalResponseRunner.Run is recorded before Run returns.
type rpcCapture struct {
	mu    sync.Mutex
	calls []capturedRPC
}

type capturedRPC struct {
	Method string
	Params map[string]any
}

func captureRPC(stdin io.Reader, stdout io.Writer) *rpcCapture {
	c := &rpcCapture{}
	go func() {
		reader := bufio.NewReader(stdin)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				return
			}
			var req struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal(line, &req); err != nil {
				continue
			}
			c.mu.Lock()
			c.calls = append(c.calls, capturedRPC{Method: req.Method, Params: req.Params})
			c.mu.Unlock()
			if _, err := fmt.Fprintf(stdout, `{"jsonrpc":"2.0","id":%d,"result":{}}`+"\n", req.ID); err != nil {
				return
			}
		}
	}()
	return c
}

// sentMessages returns the text of every Signal "send" the bridge made.
func (c *rpcCapture) sentMessages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, call := range c.calls {
		if call.Method != "send" {
			continue
		}
		msg, _ := call.Params["message"].(string)
		out = append(out, msg)
	}
	return out
}

// noteRecorder stands in for the conversation store behind RecordNote.
type noteRecorder struct {
	mu     sync.Mutex
	convID []string
	notes  []signalReplyNote
	raw    []string
	added  chan struct{}
}

func newNoteRecorder() *noteRecorder {
	return &noteRecorder{added: make(chan struct{}, 16)}
}

func (r *noteRecorder) record(conversationID, note string) error {
	var decoded signalReplyNote
	if err := json.Unmarshal([]byte(note), &decoded); err != nil {
		return fmt.Errorf("note is not JSON: %w", err)
	}
	r.mu.Lock()
	r.convID = append(r.convID, conversationID)
	r.notes = append(r.notes, decoded)
	r.raw = append(r.raw, note)
	r.mu.Unlock()
	select {
	case r.added <- struct{}{}:
	default:
	}
	return nil
}

func (r *noteRecorder) snapshot() ([]string, []signalReplyNote, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.convID...), append([]signalReplyNote(nil), r.notes...), append([]string(nil), r.raw...)
}

// scriptedRunner plays the model for one turn: its script receives the
// request as the runner sees it (after any loop preparation) and the run
// context, may call runtime tools through their real handlers, and
// returns the response the agent would have produced.
type scriptedRunner struct {
	mu     sync.Mutex
	reqs   []loop.Request
	script func(ctx context.Context, req loop.Request) (*loop.Response, error)
}

func (r *scriptedRunner) Run(ctx context.Context, req loop.Request, _ loop.StreamCallback) (*loop.Response, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	script := r.script
	r.mu.Unlock()
	return script(ctx, req)
}

func (r *scriptedRunner) requests() []loop.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]loop.Request(nil), r.reqs...)
}

// callRuntimeTool invokes a request's runtime tool the way the agent
// executor does: by name, with the run context.
func callRuntimeTool(ctx context.Context, req loop.Request, name string, args map[string]any) (string, error) {
	for _, tool := range req.RuntimeTools {
		if tool.Name == name {
			return tool.Handler(ctx, args)
		}
	}
	return "", fmt.Errorf("runtime tool %q not offered on this request", name)
}

func coreAttentionNotify(t *testing.T) messages.Envelope {
	t.Helper()
	notify, err := (messages.Envelope{
		From: messages.Identity{Kind: messages.IdentityLoop, ID: "loop-42", Name: "garage-watch"},
		To: messages.Destination{
			Kind:     messages.DestinationLoop,
			Target:   "signal",
			Selector: messages.SelectorName,
		},
		Type:    messages.TypeSignal,
		Scope:   []string{loop.CoreAttentionScope},
		Payload: messages.LoopNotifyPayload{Kind: loop.CoreAttentionRequestKind, Concern: "The reading looks wrong."},
	}).Normalize(time.Now())
	if err != nil {
		t.Fatalf("normalize notification: %v", err)
	}
	return notify
}

// holdHarness is a bridge wired to a responding signal-cli pipe, a JSON
// log sink, a note recorder, and a scripted runner.
type holdHarness struct {
	bridge *Bridge
	rpc    *rpcCapture
	logs   *syncBuffer
	notes  *noteRecorder
	runner *scriptedRunner
}

func newHoldHarness(t *testing.T, opts ...bridgeOption) *holdHarness {
	t.Helper()
	logs := &syncBuffer{}
	notes := newNoteRecorder()
	runner := &scriptedRunner{script: func(context.Context, loop.Request) (*loop.Response, error) {
		return &loop.Response{Content: "ok"}, nil
	}}
	all := append([]bridgeOption{func(cfg *BridgeConfig) {
		cfg.Logger = slog.New(slog.NewJSONHandler(logs, nil))
		cfg.Runner = runner
		cfg.RecordNote = notes.record
	}}, opts...)
	bridge, stdout, stdin, _ := bridgeHelper(t, all...)
	return &holdHarness{
		bridge: bridge,
		rpc:    captureRPC(stdin, stdout),
		logs:   logs,
		notes:  notes,
		runner: runner,
	}
}

// TestSignalResponseRunner_WakeTurnReplyOutcomes pins what reaches the
// person on the thread at the end of a loop-notification wake. The turn
// comes from the real builder, so the hold tool the script calls is the
// one the bridge attaches, and its handler finds the recorder on the run
// context exactly as it would under the agent executor.
//
// The decided behaviour for a wake that ends with neither a hold nor
// model-written text (empty, or the runtime's own empty-response
// placeholder) is: nothing is sent, a Warn is logged, and a
// signal_reply_not_sent note is stored. A wake never gets the interactive
// fallback, whose "please try again" answers a request nobody made.
func TestSignalResponseRunner_WakeTurnReplyOutcomes(t *testing.T) {
	const (
		reason        = "answered garage-watch with loop_wake; sensor glitch, nothing for a person to do"
		stageDirected = "*(silence — nothing worth waking them for at 00:30)*"
		realMessage   = "Heads up: the garage door has been open since 23:50."
	)

	type outcome struct {
		sends     []string
		note      *signalReplyNote
		logs      []string
		forbidLog []string
	}

	tests := []struct {
		name   string
		script func(t *testing.T, ctx context.Context, req loop.Request, wakes *[]string) *loop.Response
		want   outcome
	}{
		{
			name: "hold with empty final text sends nothing",
			script: func(t *testing.T, ctx context.Context, req loop.Request, _ *[]string) *loop.Response {
				if _, err := callRuntimeTool(ctx, req, HoldReplyToolName, map[string]any{"reason": reason}); err != nil {
					t.Fatalf("hold: %v", err)
				}
				return &loop.Response{RequestID: "req-hold-empty", ToolsUsed: map[string]int{HoldReplyToolName: 1}}
			},
			want: outcome{
				note: &signalReplyNote{Kind: replyNoteHeld, Reason: reason},
				logs: []string{`"msg":"signal reply held"`, `"reason":"` + reason + `"`, `"discarded_text_len":0`, `"request_id":"req-hold-empty"`, `"loop_id":"loop-signal-1"`, `"conversation_id":"signal-15551234567"`},
			},
		},
		{
			name: "hold discards a stage direction and logs only its length",
			script: func(t *testing.T, ctx context.Context, req loop.Request, _ *[]string) *loop.Response {
				if _, err := callRuntimeTool(ctx, req, HoldReplyToolName, map[string]any{"reason": reason}); err != nil {
					t.Fatalf("hold: %v", err)
				}
				return &loop.Response{Content: stageDirected, RequestID: "req-hold-text", ToolsUsed: map[string]int{HoldReplyToolName: 1}}
			},
			want: outcome{
				note:      &signalReplyNote{Kind: replyNoteHeld, Reason: reason, FinalTextWithheld: true},
				logs:      []string{`"msg":"signal reply held"`, fmt.Sprintf(`"discarded_text_len":%d`, len(stageDirected))},
				forbidLog: []string{"nothing worth waking them"},
			},
		},
		{
			name: "loop_wake reply stands and the hold still sends nothing",
			script: func(t *testing.T, ctx context.Context, req loop.Request, wakes *[]string) *loop.Response {
				// The determination goes back to the requester first, then
				// the Signal message is held — the order the prompt asks for.
				*wakes = append(*wakes, "loop-42: sensor glitch, concern closed by core")
				if _, err := callRuntimeTool(ctx, req, HoldReplyToolName, map[string]any{"reason": reason}); err != nil {
					t.Fatalf("hold: %v", err)
				}
				return &loop.Response{
					Content:   "Replied to garage-watch; nothing for the thread.",
					RequestID: "req-hold-wake",
					ToolsUsed: map[string]int{"loop_wake": 1, HoldReplyToolName: 1},
				}
			},
			want: outcome{
				note: &signalReplyNote{Kind: replyNoteHeld, Reason: reason, FinalTextWithheld: true},
				logs: []string{`"msg":"signal reply held"`},
			},
		},
		{
			name: "real text without a hold is sent as today",
			script: func(t *testing.T, _ context.Context, _ loop.Request, _ *[]string) *loop.Response {
				return &loop.Response{Content: realMessage, RequestID: "req-send"}
			},
			want: outcome{
				sends:     []string{realMessage},
				logs:      []string{`"msg":"signal reply sent"`},
				forbidLog: []string{"signal reply held", "signal wake reply not sent"},
			},
		},
		{
			name: "empty text without a hold sends nothing and is not given the fallback",
			script: func(t *testing.T, _ context.Context, _ loop.Request, _ *[]string) *loop.Response {
				return &loop.Response{RequestID: "req-empty"}
			},
			want: outcome{
				note:      &signalReplyNote{Kind: replyNoteNotSent},
				logs:      []string{`"level":"WARN"`, `"msg":"signal wake reply not sent: turn ended without reply text or a hold"`, `"runtime_fallback":false`},
				forbidLog: []string{"signal reply held"},
			},
		},
		{
			name: "the runtime's empty-response placeholder is not sent on a wake",
			script: func(t *testing.T, _ context.Context, req loop.Request, _ *[]string) *loop.Response {
				// What the engine returns after nudging an empty ending and
				// getting nothing: the request's own fallback text.
				return &loop.Response{Content: req.FallbackContent, RequestID: "req-fallback"}
			},
			want: outcome{
				note: &signalReplyNote{Kind: replyNoteNotSent, FinalTextWithheld: true},
				logs: []string{`"msg":"signal wake reply not sent: turn ended without reply text or a hold"`, `"runtime_fallback":true`},
			},
		},
		{
			name: "a hold without a reason is refused and the reply is sent",
			script: func(t *testing.T, ctx context.Context, req loop.Request, _ *[]string) *loop.Response {
				_, err := callRuntimeTool(ctx, req, HoldReplyToolName, map[string]any{"reason": "   "})
				if err == nil || !strings.Contains(err.Error(), "reason is required") {
					t.Fatalf("hold without reason error = %v, want a reason-is-required error", err)
				}
				return &loop.Response{Content: realMessage, ToolsUsed: map[string]int{HoldReplyToolName: 1}}
			},
			want: outcome{
				sends:     []string{realMessage},
				forbidLog: []string{"signal reply held"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHoldHarness(t)
			var wakes []string
			h.runner.script = func(ctx context.Context, req loop.Request) (*loop.Response, error) {
				return tt.script(t, ctx, req, &wakes), nil
			}

			turn, err := h.bridge.buildSignalTurn(context.Background(), holdTestSender, loop.TurnInput{
				NotifyEnvelopes: []messages.Envelope{coreAttentionNotify(t)},
				WakeTags:        []string{"loops", "signal"},
			})
			if err != nil || turn == nil {
				t.Fatalf("buildSignalTurn = %v, %v", turn, err)
			}
			req := turn.Request
			// The loop stamps its identity on every prepared request; the
			// held log carries it so the decision is keyed to the turn.
			req.RoutingFactors["loop_id"] = "loop-signal-1"
			req.RoutingFactors["loop_name"] = signalLoopName(holdTestSender)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			resp, err := signalResponseRunner{bridge: h.bridge, runner: h.runner}.Run(ctx, req, nil)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			if got := h.rpc.sentMessages(); !equalStrings(got, tt.want.sends) {
				t.Errorf("Signal sends = %q, want %q", got, tt.want.sends)
			}

			convIDs, notes, _ := h.notes.snapshot()
			switch {
			case tt.want.note == nil && len(notes) > 0:
				t.Errorf("notes = %+v, want none on a turn that sent its reply", notes)
			case tt.want.note != nil:
				if len(notes) != 1 {
					t.Fatalf("notes = %+v, want exactly one", notes)
				}
				got := notes[0]
				if got.Kind != tt.want.note.Kind || got.Reason != tt.want.note.Reason || got.FinalTextWithheld != tt.want.note.FinalTextWithheld || got.SignalMessageSent {
					t.Errorf("note = %+v, want kind=%q reason=%q final_text_withheld=%v signal_message_sent=false",
						got, tt.want.note.Kind, tt.want.note.Reason, tt.want.note.FinalTextWithheld)
				}
				if tt.want.note.Kind == replyNoteNotSent && got.Cause == "" {
					t.Errorf("not-sent note carries no cause: %+v", got)
				}
				if convIDs[0] != "signal-15551234567" {
					t.Errorf("note conversation = %q, want the Signal conversation", convIDs[0])
				}
			}

			logs := h.logs.String()
			for _, want := range tt.want.logs {
				if !strings.Contains(logs, want) {
					t.Errorf("log missing %s\nlogs:\n%s", want, logs)
				}
			}
			for _, forbid := range tt.want.forbidLog {
				if strings.Contains(logs, forbid) {
					t.Errorf("log contains %q, want it absent\nlogs:\n%s", forbid, logs)
				}
			}

			if tt.name == "loop_wake reply stands and the hold still sends nothing" {
				if len(wakes) != 1 {
					t.Errorf("loop_wake replies = %q, want the one determination", wakes)
				}
				if resp.ToolsUsed["loop_wake"] != 1 || resp.ToolsUsed[HoldReplyToolName] != 1 {
					t.Errorf("ToolsUsed = %v, want loop_wake and the hold both recorded", resp.ToolsUsed)
				}
				if resp.Content != "Replied to garage-watch; nothing for the thread." {
					t.Errorf("response content = %q, want the model's text returned to the loop untouched", resp.Content)
				}
			}
			if tt.name == "empty text without a hold sends nothing and is not given the fallback" && resp.Content != "" {
				t.Errorf("response content = %q, want empty: a wake turn is never given the interactive fallback", resp.Content)
			}
		})
	}
}

// TestBridge_ReplyHoldOfferedOnlyOnLoopNotificationWake pins which turn
// shapes carry the hold tool. Only the pure loop-notification wake does:
// every other shape answers something the person sent, where a reply is
// expected and an empty ending keeps the interactive fallback.
func TestBridge_ReplyHoldOfferedOnlyOnLoopNotificationWake(t *testing.T) {
	h := newHoldHarness(t)
	notify := coreAttentionNotify(t)
	mkItem := func(message string, ts int64) loop.MailboxItem {
		t.Helper()
		payload, err := json.Marshal(&Envelope{
			Source:      holdTestSender,
			Timestamp:   ts,
			DataMessage: &DataMessage{Timestamp: ts, Message: message},
		})
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		return loop.MailboxItem{ID: fmt.Sprintf("signal:%d", ts), Payload: payload}
	}

	tests := []struct {
		name  string
		build func() (*loop.AgentTurn, error)
		want  bool
	}{
		{
			name: "pure loop notification wake",
			build: func() (*loop.AgentTurn, error) {
				return h.bridge.buildSignalTurn(context.Background(), holdTestSender, loop.TurnInput{NotifyEnvelopes: []messages.Envelope{notify}})
			},
			want: true,
		},
		{
			name: "inbound message",
			build: func() (*loop.AgentTurn, error) {
				return h.bridge.buildSignalTurn(context.Background(), holdTestSender, loop.TurnInput{MailboxItems: []loop.MailboxItem{mkItem("hello", 1700000000000)}})
			},
		},
		{
			name: "inbound message with a notification riding along",
			build: func() (*loop.AgentTurn, error) {
				return h.bridge.buildSignalTurn(context.Background(), holdTestSender, loop.TurnInput{
					MailboxItems:    []loop.MailboxItem{mkItem("hello", 1700000000000)},
					NotifyEnvelopes: []messages.Envelope{notify},
				})
			},
		},
		{
			name: "reaction",
			build: func() (*loop.AgentTurn, error) {
				return h.bridge.prepareSignalTurn(context.Background(), &Envelope{
					Source:    holdTestSender,
					Timestamp: 1700000000001,
					DataMessage: &DataMessage{Reaction: &Reaction{
						Emoji:               "👍",
						TargetAuthor:        holdTestSender,
						TargetSentTimestamp: 1700000000000,
					}},
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			turn, err := tt.build()
			if err != nil || turn == nil {
				t.Fatalf("build turn = %v, %v", turn, err)
			}
			if got := offersReplyHold(turn.Request.RuntimeTools); got != tt.want {
				t.Fatalf("hold tool offered = %v, want %v", got, tt.want)
			}
			if !tt.want {
				return
			}
			// The wake prompt must name the door, or the model is left
			// with a tool it has no reason to reach for.
			if !strings.Contains(turn.Request.Messages[0].Content, HoldReplyToolName) {
				t.Errorf("wake prompt does not name %s: %q", HoldReplyToolName, turn.Request.Messages[0].Content)
			}
			if turn.Summary["fallback_suppressed"] != true || turn.Summary["reply_hold_offered"] != true {
				t.Errorf("summary = %v, want fallback_suppressed and reply_hold_offered", turn.Summary)
			}
		})
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func startHoldSenderLoop(t *testing.T) (*holdHarness, *loop.Registry) {
	t.Helper()
	registry := loop.NewRegistry()
	h := newHoldHarness(t, func(cfg *BridgeConfig) { cfg.Registry = registry })
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		registry.ShutdownAll(ctx)
	})
	h.bridge.mu.Lock()
	h.bridge.parentID = "signal-parent"
	h.bridge.mu.Unlock()
	return h, registry
}

// TestBridge_OrdinaryMessageEndToEnd drives an operator's message through
// the real sender loop: the request the runner receives (after the loop's
// own preparation) carries no hold tool, and the reply is sent.
func TestBridge_OrdinaryMessageEndToEnd(t *testing.T) {
	h, _ := startHoldSenderLoop(t)
	const reply = "Sunny and 72 this afternoon."
	h.runner.script = func(context.Context, loop.Request) (*loop.Response, error) {
		return &loop.Response{Content: reply}, nil
	}

	if err := h.bridge.dispatch(context.Background(), &Envelope{
		Source:      holdTestSender,
		SourceName:  "Alice",
		Timestamp:   1700000000000,
		DataMessage: &DataMessage{Message: "What's the weather?"},
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	waitFor(t, "the reply send", func() bool { return len(h.rpc.sentMessages()) > 0 })
	reqs := h.runner.requests()
	if len(reqs) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(reqs))
	}
	if offersReplyHold(reqs[0].RuntimeTools) {
		t.Errorf("an ordinary message was offered %s; a reply is expected there", HoldReplyToolName)
	}
	if got := h.rpc.sentMessages(); !equalStrings(got, []string{reply}) {
		t.Errorf("Signal sends = %q, want the reply", got)
	}
	if _, notes, _ := h.notes.snapshot(); len(notes) > 0 {
		t.Errorf("notes = %+v, want none on an ordinary reply", notes)
	}
}

// TestBridge_LoopNotificationHoldEndToEnd drives a core-attention wake
// through the real sender loop, so the hold tool has to survive the
// loop's request preparation to reach the runner, and the held decision
// has to survive the loop's fallback refill (the request still carries
// the interactive fallback — which is why suppression lives at the send
// gate) to leave the person's phone untouched.
func TestBridge_LoopNotificationHoldEndToEnd(t *testing.T) {
	h, registry := startHoldSenderLoop(t)
	const (
		reason = "answered garage-watch; sensor glitch, nothing for a person to do"
		text   = "*(silence — nothing worth waking them for at 00:30)*"
	)
	h.runner.script = func(ctx context.Context, req loop.Request) (*loop.Response, error) {
		if _, err := callRuntimeTool(ctx, req, HoldReplyToolName, map[string]any{"reason": reason}); err != nil {
			return nil, err
		}
		return &loop.Response{Content: text, ToolsUsed: map[string]int{"loop_wake": 1, HoldReplyToolName: 1}}, nil
	}

	loopID, _, ok := h.bridge.ensureSenderLoop(context.Background(), holdTestSender)
	if !ok {
		t.Fatal("sender loop did not start")
	}
	if _, err := registry.NotifyLoop(context.Background(), loopID, coreAttentionNotify(t)); err != nil {
		t.Fatalf("NotifyLoop: %v", err)
	}

	select {
	case <-h.notes.added:
	case <-time.After(3 * time.Second):
		t.Fatalf("no held note recorded\nlogs:\n%s", h.logs.String())
	}

	reqs := h.runner.requests()
	if len(reqs) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(reqs))
	}
	req := reqs[0]
	if !offersReplyHold(req.RuntimeTools) {
		t.Fatalf("prepared wake request lost %s", HoldReplyToolName)
	}
	if req.RoutingFactors["loop_id"] != loopID {
		t.Errorf("prepared request loop_id = %q, want %q", req.RoutingFactors["loop_id"], loopID)
	}
	if req.FallbackContent != prompts.InteractiveEmptyResponseFallback {
		t.Errorf("prepared FallbackContent = %q; the loop merge refills it, and this test documents that the send gate — not the request — suppresses it", req.FallbackContent)
	}
	if got := h.rpc.sentMessages(); len(got) != 0 {
		t.Errorf("Signal sends = %q, want none on a held wake", got)
	}
	_, notes, _ := h.notes.snapshot()
	if len(notes) != 1 || notes[0].Kind != replyNoteHeld || notes[0].Reason != reason || !notes[0].FinalTextWithheld {
		t.Errorf("notes = %+v, want one held note with the reason and final_text_withheld", notes)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, `"msg":"signal reply held"`) || !strings.Contains(logs, `"loop_id":"`+loopID+`"`) {
		t.Errorf("held log missing or not keyed to the loop\nlogs:\n%s", logs)
	}
}

// TestHandleHoldReply_OutsideAWakeTurn pins the defensive error: the tool
// is only attached to wake turns, but a handler reached without the
// bridge's recorder must say the reply will be sent, not claim a hold.
func TestHandleHoldReply_OutsideAWakeTurn(t *testing.T) {
	_, err := handleHoldReply(context.Background(), map[string]any{"reason": "nothing to add"})
	if err == nil || !strings.Contains(err.Error(), "sent as usual") {
		t.Fatalf("error = %v, want one saying the final text is sent as usual", err)
	}
}

func TestIsRuntimeFallback(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		fallback string
		want     bool
	}{
		{name: "empty is not a placeholder", content: "", fallback: prompts.InteractiveEmptyResponseFallback},
		{name: "request fallback", content: prompts.InteractiveEmptyResponseFallback, fallback: prompts.InteractiveEmptyResponseFallback, want: true},
		{name: "engine default fallback", content: " " + prompts.EmptyResponseFallback + "\n", want: true},
		{name: "model text that mentions a problem", content: "I hit a problem with the garage sensor.", fallback: prompts.InteractiveEmptyResponseFallback},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRuntimeFallback(tt.content, tt.fallback); got != tt.want {
				t.Errorf("isRuntimeFallback(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
