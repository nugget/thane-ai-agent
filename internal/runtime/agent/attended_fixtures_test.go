package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/runtime/iterate"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// The attended fixtures characterise what an interactive conversation
// keeps of the work a turn did. They are the before-state of the
// regression fixtures #1626 calls for, pinned with neither completion
// enforcement (#1626) nor memory of action (#1628) in place: a turn
// that changed three of six requested loop definitions and then claimed
// all six is a clean stop, and a turn that only narrated an action it
// never took is a clean stop too. In both, the next turn's model input carries the
// narration and nothing that records what was actually done; the
// action record exists only in the tool_calls table. Memory of action
// (#1628) is expected to change that, and it edits these expectations
// rather than replacing them.

const attendedTool = "loop_definition_update"

// attendedResultMarker is the stub handler's result prefix. It shows up
// in a later turn's model input only if the tool result is carried
// forward there.
const attendedResultMarker = "fixture-updated:"

// attendedLLM is a strict scripted model. It errors past the end of its
// script, so an unexpected extra call fails the turn, and it records a
// copy of every call's messages and tool definitions. The copy matters:
// agent.Loop rewrites the first message of its slice between
// iterations, so keeping the caller's slice would show later prompts.
type attendedLLM struct {
	mu     sync.Mutex
	script []*llm.ChatResponse
	calls  []attendedCall
}

type attendedCall struct {
	messages []llm.Message
	tools    []map[string]any
}

func (m *attendedLLM) Chat(ctx context.Context, model string, msgs []llm.Message, td []map[string]any) (*llm.ChatResponse, error) {
	return m.ChatStream(ctx, model, msgs, td, nil)
}

func (m *attendedLLM) ChatStream(_ context.Context, _ string, msgs []llm.Message, td []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	copied := make([]llm.Message, len(msgs))
	for i, msg := range msgs {
		msg.ToolCalls = slices.Clone(msg.ToolCalls)
		copied[i] = msg
	}
	m.calls = append(m.calls, attendedCall{messages: copied, tools: slices.Clone(td)})
	if len(m.calls) > len(m.script) {
		return nil, fmt.Errorf("attendedLLM: unscripted call %d (script has %d)", len(m.calls), len(m.script))
	}
	return m.script[len(m.calls)-1], nil
}

func (m *attendedLLM) Ping(context.Context) error { return nil }

func (m *attendedLLM) recorded() []attendedCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

// registeredLoopDefinitionUpdate returns loop_definition_update exactly
// as the production registry builds it: its real description,
// parameters and catalog metadata. Each call builds a fresh registry, so
// two results share no maps and comparing them is a real comparison.
func registeredLoopDefinitionUpdate(t *testing.T) *tools.Tool {
	t.Helper()
	defs, err := looppkg.NewDefinitionRegistry(nil)
	if err != nil {
		t.Fatalf("NewDefinitionRegistry: %v", err)
	}
	reg := tools.NewEmptyRegistry()
	reg.ConfigureLoopDefinitionTools(tools.LoopDefinitionToolDeps{Registry: defs})
	tool := reg.Get(attendedTool)
	if tool == nil {
		t.Fatalf("%s is not registered by ConfigureLoopDefinitionTools", attendedTool)
	}
	return tool
}

// updateRecorder is the stub handler behind the real registration. It
// records the name argument of every call and changes nothing.
type updateRecorder struct {
	mu    sync.Mutex
	names []string
}

func (r *updateRecorder) handle(_ context.Context, args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names = append(r.names, name)
	return attendedResultMarker + name, nil
}

func (r *updateRecorder) updated() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.names)
}

// updateCalls is one assistant message calling loop_definition_update
// once per name, switching each definition to task prompt mode.
func updateCalls(names ...string) *llm.ChatResponse {
	calls := make([]llm.ToolCall, 0, len(names))
	for i, name := range names {
		var tc llm.ToolCall
		tc.ID = fmt.Sprintf("call-%d", i+1)
		tc.Function.Name = attendedTool
		tc.Function.Arguments = map[string]any{"name": name, "prompt_mode": "task"}
		calls = append(calls, tc)
	}
	return &llm.ChatResponse{Model: "test-model", Message: llm.Message{Role: "assistant", ToolCalls: calls}}
}

func attendedText(content string) *llm.ChatResponse {
	return &llm.ChatResponse{Model: "test-model", Message: llm.Message{Role: "assistant", Content: content}}
}

// attendedTurn is one user message and the model responses scripted
// for it, with what the turn reports today.
type attendedTurn struct {
	user   string
	script []*llm.ChatResponse
	// wantContent is the turn's final reply.
	wantContent string
	// wantOutcomes is the turn's per-tool ledger; nil means the
	// Response carries no ledger at all.
	wantOutcomes map[string]iterate.ToolOutcome
}

func newAttendedStore(t *testing.T) *memory.SQLiteStore {
	t.Helper()
	store, err := memory.NewSQLiteStore(filepath.Join(t.TempDir(), "thane.db"), 100)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

// offeredTool returns the definition of name in one call's tools.
func offeredTool(defs []map[string]any, name string) (map[string]any, bool) {
	for _, def := range defs {
		fn, ok := def["function"].(map[string]any)
		if ok && fn["name"] == name {
			return fn, true
		}
	}
	return nil, false
}

// actionRecord reports the first trace of turn-1 tool activity in a
// model input: a tool call, a tool result, or text naming the tool or
// carrying its result. Empty means the input records no action.
func actionRecord(msgs []llm.Message) string {
	for i, msg := range msgs {
		switch {
		case len(msg.ToolCalls) > 0:
			return fmt.Sprintf("message %d carries %d tool calls", i, len(msg.ToolCalls))
		case msg.Role == "tool" || msg.ToolCallID != "":
			return fmt.Sprintf("message %d is a tool result", i)
		case strings.Contains(msg.Content, attendedTool):
			return fmt.Sprintf("message %d (%s) names %s", i, msg.Role, attendedTool)
		case strings.Contains(msg.Content, attendedResultMarker):
			return fmt.Sprintf("message %d (%s) carries a tool result", i, msg.Role)
		}
	}
	return ""
}

// hasAssistantReply reports whether an assistant message in msgs
// carries reply. Stored history is framed with metadata on its way back
// into the model input, so the reply is a substring, not the whole.
func hasAssistantReply(msgs []llm.Message, reply string) bool {
	for _, msg := range msgs {
		if msg.Role == "assistant" && strings.Contains(msg.Content, reply) {
			return true
		}
	}
	return false
}

// TestAttendedFixtures pins today's behaviour for two attended failure
// shapes. In the first, asked to change six loop definitions, the model
// changes three and claims all six. In the second, the model says it
// will act and stops without a call, twice.
func TestAttendedFixtures(t *testing.T) {
	const narratedDone = "Done — all six now use task mode."
	cases := []struct {
		name  string
		turns []attendedTurn
		// wantUpdated is the name argument of every handler call, in
		// call order, across all turns.
		wantUpdated []string
		// wantAssistant is every assistant message stored in the
		// conversation, in order.
		wantAssistant []string
	}{
		{
			name: "six definitions asked, three changed, all claimed",
			turns: []attendedTurn{
				{
					user: "Switch the loop definitions alpha, bravo, charlie, delta, echo and foxtrot to task prompt mode.",
					script: []*llm.ChatResponse{
						updateCalls("alpha", "bravo", "charlie"),
						attendedText(narratedDone),
					},
					wantContent: narratedDone,
					wantOutcomes: map[string]iterate.ToolOutcome{
						attendedTool: {
							Calls: 3, Successes: 3,
							Targets: map[string]iterate.TargetOutcome{"": {Calls: 3, Successes: 3}},
						},
					},
				},
				{
					user:         "Did foxtrot change?",
					script:       []*llm.ChatResponse{attendedText("Yes, foxtrot now uses task mode.")},
					wantContent:  "Yes, foxtrot now uses task mode.",
					wantOutcomes: nil,
				},
			},
			wantUpdated:   []string{"alpha", "bravo", "charlie"},
			wantAssistant: []string{narratedDone, "Yes, foxtrot now uses task mode."},
		},
		{
			name: "narrate-then-stop twice",
			turns: []attendedTurn{
				{
					user:         "Please switch the loop definition alpha to task prompt mode.",
					script:       []*llm.ChatResponse{attendedText("I'll update alpha to task mode now.")},
					wantContent:  "I'll update alpha to task mode now.",
					wantOutcomes: nil,
				},
				{
					user:         "Go ahead with alpha.",
					script:       []*llm.ChatResponse{attendedText("Updating it now.")},
					wantContent:  "Updating it now.",
					wantOutcomes: nil,
				},
			},
			wantUpdated:   nil,
			wantAssistant: []string{"I'll update alpha to task mode now.", "Updating it now."},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registered := registeredLoopDefinitionUpdate(t)
			recorder := &updateRecorder{}
			stub := *registeredLoopDefinitionUpdate(t)
			stub.Handler = recorder.handle
			reg := tools.NewEmptyRegistry()
			reg.Register(&stub)

			model := &attendedLLM{}
			for _, turn := range tc.turns {
				model.script = append(model.script, turn.script...)
			}
			store := newAttendedStore(t)
			// No SetCapabilityTags: with no capTags the loop applies no
			// tag filtering, so the tool's 'loops' tag does not hide it.
			loop := &Loop{
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				memory: store,
				llm:    model,
				tools:  reg,
				model:  "test-model",
			}

			const convID = "attended-fixture"
			var history []string // assistant replies of earlier turns
			for i, turn := range tc.turns {
				before := len(model.recorded())
				// Stamp the origin the REST, OpenAI-compatible and
				// Ollama-compatible surfaces declare, so the turn is
				// classified as attended (replyAwaited) and its user
				// message is persisted with the attended origin.
				resp, err := loop.Run(context.Background(), &Request{
					ConversationID: convID,
					MessageOrigin:  memory.OriginAPI,
					Messages:       []Message{{Role: "user", Content: turn.user}},
				}, nil)
				if err != nil {
					t.Fatalf("turn %d: Run: %v", i+1, err)
				}
				calls := model.recorded()
				if got := len(calls) - before; got != len(turn.script) {
					t.Errorf("turn %d: model calls = %d, want %d", i+1, got, len(turn.script))
				}
				if resp.Content != turn.wantContent {
					t.Errorf("turn %d: Content = %q, want %q", i+1, resp.Content, turn.wantContent)
				}
				if resp.FinishReason != "stop" || resp.Exhausted {
					t.Errorf("turn %d: FinishReason = %q, Exhausted = %v; want a clean stop", i+1, resp.FinishReason, resp.Exhausted)
				}
				if !reflect.DeepEqual(resp.ToolOutcomes, turn.wantOutcomes) {
					t.Errorf("turn %d: ToolOutcomes = %+v, want %+v", i+1, resp.ToolOutcomes, turn.wantOutcomes)
				}

				// The next turn is the customer: its first model input
				// carries every earlier reply and no record of what
				// those turns actually did.
				if i > 0 && len(calls) > before {
					input := calls[before].messages
					for _, reply := range history {
						if !hasAssistantReply(input, reply) {
							t.Errorf("turn %d: model input lacks earlier reply %q", i+1, reply)
						}
					}
					if trace := actionRecord(input); trace != "" {
						t.Errorf("turn %d: model input records an action: %s", i+1, trace)
					}
				}
				history = append(history, resp.Content)
			}

			calls := model.recorded()
			if len(calls) != len(model.script) {
				t.Errorf("model calls = %d, script has %d", len(calls), len(model.script))
			}
			for i, call := range calls {
				offered, ok := offeredTool(call.tools, attendedTool)
				if !ok {
					t.Errorf("call %d: %s not offered", i+1, attendedTool)
					continue
				}
				if offered["description"] != registered.Description {
					t.Errorf("call %d: offered description differs from the registration:\n got %q\nwant %q", i+1, offered["description"], registered.Description)
				}
				if !reflect.DeepEqual(offered["parameters"], registered.Parameters) {
					t.Errorf("call %d: offered parameters differ from the registration", i+1)
				}
			}

			if got := recorder.updated(); !slices.Equal(got, tc.wantUpdated) {
				t.Errorf("handler calls = %v, want %v", got, tc.wantUpdated)
			}

			var rowNames []string
			for _, row := range store.GetToolCalls(convID, 0) {
				if row.ToolName != attendedTool {
					t.Errorf("tool_calls row %s names %q, want %q", row.ID, row.ToolName, attendedTool)
					continue
				}
				var args map[string]any
				if err := json.Unmarshal([]byte(row.Arguments), &args); err != nil {
					t.Errorf("tool_calls row %s arguments %q: %v", row.ID, row.Arguments, err)
					continue
				}
				name, _ := args["name"].(string)
				rowNames = append(rowNames, name)
			}
			slices.Sort(rowNames)
			wantRows := slices.Sorted(slices.Values(tc.wantUpdated))
			if !slices.Equal(rowNames, wantRows) {
				t.Errorf("tool_calls rows name %v, want %v", rowNames, wantRows)
			}

			stored, err := store.GetMessages(context.Background(), convID)
			if err != nil {
				t.Fatalf("GetMessages: %v", err)
			}
			var assistant []string
			for _, msg := range stored {
				if msg.Role == "assistant" {
					assistant = append(assistant, msg.Content)
				}
				if msg.Role == "user" && msg.Origin != memory.OriginAPI {
					t.Errorf("stored user message %q has origin %q, want %q", msg.Content, msg.Origin, memory.OriginAPI)
				}
			}
			if !slices.Equal(assistant, tc.wantAssistant) {
				t.Errorf("stored assistant messages = %q, want %q", assistant, tc.wantAssistant)
			}
		})
	}
}
