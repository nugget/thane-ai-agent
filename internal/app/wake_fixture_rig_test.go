package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/events"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/documents"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

// The wake rig runs a real loop.Loop through loopAdapter, agent.Loop
// and iterate.Engine. Output tools come from the real hydrateLoopOutputs
// over a TempDir document store, memory is SQLite (so tool_calls rows
// are real), and the model is a strict script that records what every
// call was offered. It lives in package app because the loop package
// cannot import the engine and loopAdapter is unexported.

// fixtureModel is the model name every scripted response reports.
const fixtureModel = "fixture-model"

// wakeBarrierTimeout bounds how long the rig waits for a loop event. It
// is a failsafe against a hung loop, not a synchronization point: every
// step of the protocol waits on an event, never on time.
const wakeBarrierTimeout = 30 * time.Second

// recordedCall is one model call as the fixture model received it.
type recordedCall struct {
	// messages is a deep copy of the call's messages. agent.Loop
	// rewrites msgs[0] in place on later iterations, so a reference
	// would show prompts the call never saw.
	messages []llm.Message
	// tools is a deep copy of the tool definitions the call offered.
	tools []map[string]any
	// response is the scripted response the call returned, or nil when
	// the call ran past the end of the script.
	response *llm.ChatResponse
}

// fixtureLLM is a strict scripted llm.Client. It answers calls in
// script order, errors on any call past the end of the script, and
// records a deep copy of each call's messages and tool definitions.
type fixtureLLM struct {
	mu     sync.Mutex
	script []*llm.ChatResponse
	next   int
	// overrun counts calls made after the script ran out.
	overrun int
	calls   []recordedCall
}

// extend appends responses to the script.
func (f *fixtureLLM) extend(responses []*llm.ChatResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script = append(f.script, responses...)
}

// consumed reports how many scripted responses have been served and
// how many calls ran past the end of the script.
func (f *fixtureLLM) consumed() (served, overrun int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.next, f.overrun
}

// recorded returns the calls from index from onward.
func (f *fixtureLLM) recorded(from int) []recordedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if from >= len(f.calls) {
		return nil
	}
	return append([]recordedCall(nil), f.calls[from:]...)
}

func (f *fixtureLLM) Chat(ctx context.Context, model string, msgs []llm.Message, defs []map[string]any) (*llm.ChatResponse, error) {
	return f.ChatStream(ctx, model, msgs, defs, nil)
}

func (f *fixtureLLM) ChatStream(_ context.Context, _ string, msgs []llm.Message, defs []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := len(f.calls)
	f.calls = append(f.calls, recordedCall{messages: deepCopy(msgs), tools: deepCopy(defs)})
	if f.next >= len(f.script) {
		f.overrun++
		return nil, fmt.Errorf("fixtureLLM: call %d is past the end of its %d-response script", i, len(f.script))
	}
	resp := f.script[f.next]
	f.next++
	f.calls[i].response = resp
	// The caller gets its own copy, so nothing it does to the response
	// can change what the surface invariant later reads as emitted.
	return deepCopy(resp), nil
}

func (f *fixtureLLM) Ping(context.Context) error { return nil }

// deepCopy copies maps, slices, pointers and interfaces all the way
// down, keeping every concrete type, so a recorded tool schema stays
// deep-equal to the hydrated one it came from.
func deepCopy[T any](v T) T {
	return deepCopyValue(reflect.ValueOf(&v).Elem()).Interface().(T)
}

func deepCopyValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), deepCopyValue(iter.Value()))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(deepCopyValue(v.Index(i)))
		}
		return out
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(deepCopyValue(v.Elem()))
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(deepCopyValue(v.Elem()))
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := range v.NumField() {
			if field := out.Field(i); field.CanSet() {
				field.Set(deepCopyValue(v.Field(i)))
			}
		}
		return out
	default:
		return v
	}
}

// runRecord is one Run of the loop's runner, as the loop received it.
type runRecord struct {
	conversationID string
	response       *looppkg.Response
	err            error
}

// recordingRunner passes each wake through the real loopAdapter and
// keeps what the loop was handed back, so a fixture can read the
// per-tool ledger the loop judged the wake by.
type recordingRunner struct {
	inner looppkg.Runner
	mu    sync.Mutex
	runs  []runRecord
}

func (r *recordingRunner) Run(ctx context.Context, req looppkg.Request, stream looppkg.StreamCallback) (*looppkg.Response, error) {
	resp, err := r.inner.Run(ctx, req, stream)
	r.mu.Lock()
	r.runs = append(r.runs, runRecord{conversationID: req.ConversationID, response: resp, err: err})
	r.mu.Unlock()
	return resp, err
}

// lastRun returns the most recent run and how many runs there were.
func (r *recordingRunner) lastRun() (runRecord, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.runs) == 0 {
		return runRecord{}, 0
	}
	return r.runs[len(r.runs)-1], len(r.runs)
}

// fixedRand pins the loop's dice. The rig runs with Supervisor off, so
// nothing should roll them; a fixed value keeps that true even if a
// fixture turns it on.
type fixedRand float64

func (r fixedRand) Float64() float64 { return float64(r) }

// wakeRigSpec is what a fixture tells the rig to build.
type wakeRigSpec struct {
	// name is the loop name.
	name string
	// task is the loop's standing task.
	task string
	// outputs are hydrated for real.
	outputs []looppkg.OutputSpec
	// wakes is how many wakes the rig will drive; it is the loop's
	// MaxIter, so the loop exits on its own after the last one.
	wakes int
}

// wakeRig is one live loop wired for scripted wakes.
type wakeRig struct {
	loop   *looppkg.Loop
	reg    *looppkg.Registry
	llm    *fixtureLLM
	mem    *memory.SQLiteStore
	runner *recordingRunner
	events <-chan events.Event
	// hydrated is every runtime tool hydration generated, by name: the
	// reference the tool-surface invariant compares offered tools to.
	hydrated map[string]looppkg.RuntimeTool
}

// snapshotRuntimeTools records every hydrated runtime tool by name as an
// independent deep copy. Hydration, the loop's runtime-tool compilation
// and the registry all keep the same Parameters maps, so a by-value copy
// would move with any in-place schema change and let the tool-surface
// invariant accept the drift it exists to catch.
func snapshotRuntimeTools(tools []looppkg.RuntimeTool) map[string]looppkg.RuntimeTool {
	out := make(map[string]looppkg.RuntimeTool, len(tools))
	for _, tool := range tools {
		out[tool.Name] = deepCopy(tool)
	}
	return out
}

// newWakeRig builds, registers and starts an event-driven loop over
// real output hydration and SQLite memory, and returns once the loop is
// waiting for its first wake. Cleanup stops the loop before closing
// anything it uses.
func newWakeRig(t *testing.T, rs wakeRigSpec) *wakeRig {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	store, _ := newLoopOutputDocumentStore(t)
	app := &App{documentStore: store, documentTools: documents.NewTools(store)}
	spec, err := app.hydrateLoopOutputs(looppkg.Spec{
		Name:       rs.name,
		Enabled:    true,
		Task:       rs.task,
		Operation:  looppkg.OperationEventDriven,
		Completion: looppkg.CompletionNone,
		Outputs:    rs.outputs,
		MaxIter:    rs.wakes,
	})
	if err != nil {
		t.Fatalf("hydrateLoopOutputs: %v", err)
	}
	hydrated := snapshotRuntimeTools(spec.RuntimeTools)

	mem, err := memory.NewSQLiteStoreWithLogger(filepath.Join(t.TempDir(), "mem.db"), 100, logger)
	if err != nil {
		t.Fatalf("NewSQLiteStoreWithLogger: %v", err)
	}
	t.Cleanup(func() {
		if err := mem.Close(); err != nil {
			t.Errorf("close memory: %v", err)
		}
	})

	model := &fixtureLLM{}
	agentLoop, err := agent.NewLoop(agent.LoopOptions{
		Logger: logger,
		Memory: mem,
		LLM:    model,
		Model:  fixtureModel,
	})
	if err != nil {
		t.Fatalf("agent.NewLoop: %v", err)
	}

	bus := events.New()
	ch := bus.Subscribe(4096)
	t.Cleanup(func() { bus.Unsubscribe(ch) })

	runner := &recordingRunner{inner: &loopAdapter{agentLoop: agentLoop}}
	l, err := looppkg.NewFromSpec(spec, looppkg.Deps{
		Runner:   runner,
		Logger:   logger,
		EventBus: bus,
		Rand:     fixedRand(0.99),
	})
	if err != nil {
		t.Fatalf("NewFromSpec: %v", err)
	}
	reg := looppkg.NewRegistry()
	if err := reg.Register(l); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(l.Stop)

	rig := &wakeRig{
		loop:     l,
		reg:      reg,
		llm:      model,
		mem:      mem,
		runner:   runner,
		events:   ch,
		hydrated: hydrated,
	}
	rig.awaitEvent(t, events.KindLoopWaitStart)
	return rig
}

// awaitEvent returns this loop's next event of one of the given kinds,
// skipping everything else.
func (r *wakeRig) awaitEvent(t *testing.T, kinds ...string) events.Event {
	t.Helper()
	deadline := time.After(wakeBarrierTimeout)
	for {
		select {
		case evt, ok := <-r.events:
			if !ok {
				t.Fatal("event subscription closed while waiting")
			}
			if evt.Data["loop_id"] != r.loop.ID() {
				continue
			}
			for _, kind := range kinds {
				if evt.Kind == kind {
					return evt
				}
			}
		case <-deadline:
			t.Fatalf("no %v event from loop %s within %s", kinds, r.loop.Name(), wakeBarrierTimeout)
		}
	}
}

// wakeObservation is everything a fixture reads after one wake.
type wakeObservation struct {
	// conversationID is the wake's own conversation.
	conversationID string
	// scripted and served are the responses the wake was scripted with
	// and the ones the model actually served; overrun counts calls past
	// the end of the script.
	scripted, served, overrun int
	// calls are the wake's model calls as the model received them.
	calls []recordedCall
	// outcomes is the per-tool ledger the loop judged the wake by.
	outcomes map[string]looppkg.ToolOutcome
	// status is read after the barrier, so it already reflects the wake.
	status   looppkg.Status
	degraded string
	// event is the wake's iteration_complete (or loop_error) event.
	event events.Event
	// toolCalls are the wake's tool_calls rows.
	toolCalls []memory.ToolCall
	// stored are the wake conversation's stored messages.
	stored []memory.Message
}

// wake delivers one notification, runs the wake to completion, and
// observes it. The notify is the only thing that wakes the loop: there
// is no mailbox in the rig. The barrier is the wake's
// iteration_complete (or loop_error) event, which the loop publishes
// after it updates Status.
//
// wake does not wait for the loop to re-enter its wait first. Only
// newWakeRig waits for wait_start, once, so the first notify reaches a
// loop that is already running. After that a notify needs no
// barrier: the loop queues it in pendingNotifies and pokes its wake
// channel even while the previous wake is still finishing, so the next
// wait drains it.
func (r *wakeRig) wake(t *testing.T, notify string, script []*llm.ChatResponse) wakeObservation {
	t.Helper()
	servedBefore, overrunBefore := r.llm.consumed()
	callsBefore := len(r.llm.recorded(0))
	_, runsBefore := r.runner.lastRun()
	r.llm.extend(script)

	env, err := (messages.Envelope{
		From: messages.Identity{Kind: messages.IdentitySystem, Name: "fixture"},
		To: messages.Destination{
			Kind:     messages.DestinationLoop,
			Target:   r.loop.Name(),
			Selector: messages.SelectorName,
		},
		Type:    messages.TypeSignal,
		Payload: messages.LoopNotifyPayload{Message: notify},
	}).Normalize(time.Now())
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if _, err := r.reg.NotifyLoop(context.Background(), r.loop.ID(), env); err != nil {
		t.Fatalf("NotifyLoop: %v", err)
	}
	evt := r.awaitEvent(t, events.KindLoopIterationComplete, events.KindLoopError)

	served, overrun := r.llm.consumed()
	run, runs := r.runner.lastRun()
	if runs != runsBefore+1 {
		t.Fatalf("runner ran %d times this wake, want 1", runs-runsBefore)
	}
	obs := wakeObservation{
		conversationID: run.conversationID,
		scripted:       len(script),
		served:         served - servedBefore,
		overrun:        overrun - overrunBefore,
		calls:          r.llm.recorded(callsBefore),
		status:         r.loop.Status(),
		event:          evt,
		toolCalls:      r.mem.GetToolCalls(run.conversationID, 0),
	}
	if run.response != nil {
		obs.outcomes = run.response.ToolOutcomes
	}
	obs.degraded = looppkg.DegradedReason(obs.status, time.Now())
	stored, err := r.mem.GetAllMessages(context.Background(), run.conversationID)
	if err != nil {
		t.Fatalf("GetAllMessages(%s): %v", run.conversationID, err)
	}
	obs.stored = stored
	return obs
}

// checkScriptConsumed is the strict-script invariant: a wake must use
// every response it was scripted with and ask for none beyond them, so
// a bounce that starts or stops firing fails loudly either way.
func checkScriptConsumed(obs wakeObservation) error {
	if obs.overrun > 0 {
		return fmt.Errorf("model called %d time(s) past the end of a %d-response script", obs.overrun, obs.scripted)
	}
	if obs.served != obs.scripted {
		return fmt.Errorf("model served %d of %d scripted responses", obs.served, obs.scripted)
	}
	return nil
}

// checkToolSurface is the tool-surface invariant: at the call that
// emits a tool call, that tool must be among the call's offered
// definitions, with the description and parameters hydration generated
// for it. A tool the rig does not know as hydrated fails too, so a
// stub can never stand in for a real tool unnoticed. It returns one
// problem per violation, in call order.
func checkToolSurface(calls []recordedCall, hydrated map[string]looppkg.RuntimeTool) []string {
	var problems []string
	for i, call := range calls {
		if call.response == nil {
			continue
		}
		offered := offeredTools(call.tools)
		for _, tc := range call.response.Message.ToolCalls {
			name := tc.Function.Name
			want, ok := hydrated[name]
			if !ok {
				problems = append(problems, fmt.Sprintf("call %d emits %s, which hydration did not generate", i, name))
				continue
			}
			fn, ok := offered[name]
			if !ok {
				problems = append(problems, fmt.Sprintf("call %d emits %s but was not offered it (offered %v)", i, name, sortedKeys(offered)))
				continue
			}
			if got, _ := fn["description"].(string); got != want.Description {
				problems = append(problems, fmt.Sprintf("call %d offered %s with description %q, want the hydrated description", i, name, got))
			}
			if got := fn["parameters"]; !reflect.DeepEqual(got, want.Parameters) {
				problems = append(problems, fmt.Sprintf("call %d offered %s with parameters %v, want the hydrated parameters", i, name, got))
			}
		}
	}
	return problems
}

// offeredTools indexes a call's tool definitions by function name.
func offeredTools(defs []map[string]any) map[string]map[string]any {
	out := make(map[string]map[string]any, len(defs))
	for _, def := range defs {
		fn, _ := def["function"].(map[string]any)
		if name, _ := fn["name"].(string); name != "" {
			out[name] = fn
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// userContent joins the content of a call's user messages.
func userContent(msgs []llm.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Role == "user" {
			b.WriteString(m.Content)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// scriptedToolCall is a scripted assistant turn that emits one tool call.
func scriptedToolCall(id, name string, args map[string]any) *llm.ChatResponse {
	var call llm.ToolCall
	call.ID = id
	call.Function.Name = name
	call.Function.Arguments = args
	return &llm.ChatResponse{Model: fixtureModel, Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{call}}}
}

// scriptedText is a scripted text-only assistant turn.
func scriptedText(content string) *llm.ChatResponse {
	return &llm.ChatResponse{Model: fixtureModel, Message: llm.Message{Role: "assistant", Content: content}}
}

// TestWakeRigInvariantsCatchDrift is the rig's permanent negative
// control: each invariant must fail on the defect it exists to catch,
// so a fixture cannot stay green because an invariant went blind.
func TestWakeRigInvariantsCatchDrift(t *testing.T) {
	t.Parallel()

	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{"status_line": map[string]any{"type": "string"}},
		"required":   []string{"status_line"},
	}
	hydrated := map[string]looppkg.RuntimeTool{
		"publish_output_x": {Name: "publish_output_x", Description: "Publish the x digest.", Parameters: params},
	}
	offer := func(desc string, parameters map[string]any) []map[string]any {
		return []map[string]any{{
			"type":     "function",
			"function": map[string]any{"name": "publish_output_x", "description": desc, "parameters": parameters},
		}}
	}
	emit := scriptedToolCall("call-x", "publish_output_x", map[string]any{"status_line": "Current."})

	surface := []struct {
		name  string
		calls []recordedCall
		want  string // substring of the one problem; "" = none
	}{
		{
			name:  "hydrated tool offered verbatim",
			calls: []recordedCall{{tools: deepCopy(offer("Publish the x digest.", params)), response: emit}},
		},
		{
			name:  "placeholder stub description",
			calls: []recordedCall{{tools: offer("Thane tool publish_output_x", params), response: emit}},
			want:  "with description \"Thane tool publish_output_x\"",
		},
		{
			name:  "parameters drifted from hydration",
			calls: []recordedCall{{tools: offer("Publish the x digest.", map[string]any{"type": "object"}), response: emit}},
			want:  "want the hydrated parameters",
		},
		{
			name:  "tool not offered at the call that emits it",
			calls: []recordedCall{{response: emit}},
			want:  "was not offered it",
		},
		{
			name:  "emitted tool hydration never generated",
			calls: []recordedCall{{tools: offer("Publish the x digest.", params), response: scriptedToolCall("call-y", "publish_output_y", nil)}},
			want:  "hydration did not generate",
		},
	}
	for _, tc := range surface {
		t.Run("surface/"+tc.name, func(t *testing.T) {
			problems := checkToolSurface(tc.calls, hydrated)
			switch {
			case tc.want == "" && len(problems) != 0:
				t.Errorf("problems = %q, want none", problems)
			case tc.want != "" && (len(problems) != 1 || !strings.Contains(problems[0], tc.want)):
				t.Errorf("problems = %q, want one naming %q", problems, tc.want)
			}
		})
	}

	script := []struct {
		name    string
		obs     wakeObservation
		wantErr bool
	}{
		{name: "fully consumed", obs: wakeObservation{scripted: 3, served: 3}},
		{name: "an extra scripted response left unused", obs: wakeObservation{scripted: 4, served: 3}, wantErr: true},
		{name: "a call past the end of the script", obs: wakeObservation{scripted: 3, served: 3, overrun: 1}, wantErr: true},
	}
	for _, tc := range script {
		t.Run("script/"+tc.name, func(t *testing.T) {
			if err := checkScriptConsumed(tc.obs); (err != nil) != tc.wantErr {
				t.Errorf("checkScriptConsumed() = %v, want error %t", err, tc.wantErr)
			}
		})
	}
}

// TestSnapshotRuntimeToolsIsIndependent pins that the tool-surface
// reference cannot move with the tools it describes: an in-place change
// to a hydrated schema after the snapshot must leave the snapshot as it
// was, or the surface invariant would compare a drifted schema against
// itself and pass.
func TestSnapshotRuntimeToolsIsIndependent(t *testing.T) {
	t.Parallel()

	tools := []looppkg.RuntimeTool{{
		Name:        "publish_output_fixture",
		Description: "Publish the fixture output.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status_line": map[string]any{"type": "string", "description": "One line."},
			},
			"required": []any{"status_line"},
		},
		ContentResolveExempt: []string{"status_line"},
	}}
	want := deepCopy(tools[0])

	got := snapshotRuntimeTools(tools)

	props := tools[0].Parameters["properties"].(map[string]any)
	props["status_line"].(map[string]any)["description"] = "Drifted."
	props["digest"] = map[string]any{"type": "string"}
	tools[0].Parameters["required"].([]any)[0] = "digest"
	tools[0].ContentResolveExempt[0] = "digest"

	snap, ok := got["publish_output_fixture"]
	if !ok {
		t.Fatal("snapshot lacks publish_output_fixture")
	}
	if !reflect.DeepEqual(snap.Parameters, want.Parameters) {
		t.Errorf("snapshot parameters moved with the source:\n got %v\nwant %v", snap.Parameters, want.Parameters)
	}
	if !slices.Equal(snap.ContentResolveExempt, want.ContentResolveExempt) {
		t.Errorf("snapshot exemptions moved with the source: got %v, want %v", snap.ContentResolveExempt, want.ContentResolveExempt)
	}
}
