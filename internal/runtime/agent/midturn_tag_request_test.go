package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/logging"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

func textResponse(content string) *llm.ChatResponse {
	return &llm.ChatResponse{Model: "test-model", Message: llm.Message{Role: "assistant", Content: content}, InputTokens: 10, OutputTokens: 5}
}

func probeCallResponse() *llm.ChatResponse {
	resp := textResponse("")
	resp.Message.ToolCalls = []llm.ToolCall{{ID: "call-probe"}}
	resp.Message.ToolCalls[0].Function.Name = "probe_tool"
	resp.Message.ToolCalls[0].Function.Arguments = map[string]any{}
	return resp
}

// TestPullInputTagRequestReachesNextModelCall checks that a tag requested
// through the context a mid-turn pull receives activates on the live run
// for the next model call, whether the pull happens at the first
// iteration, at an iteration boundary, or at closure; that a protected
// tag is refused; that a pull which requests nothing changes nothing; and
// that the activation is logged on the run's logger.
func TestPullInputTagRequestReachesNextModelCall(t *testing.T) {
	capTags := map[string]config.CapabilityTagConfig{
		"base":  {Description: "base", Tools: []string{"probe_tool"}, Core: true},
		"forge": {Description: "forge", Tools: []string{"forge_tool"}},
		"owner": {Description: "owner", Tools: []string{"owner_tool"}, Protected: true},
	}
	tests := []struct {
		name        string
		responses   []*llm.ChatResponse
		requestPoll int    // poll on which the pull requests requestTag and delivers a message
		requestTag  string // "" delivers a message without requesting
		before      int    // model call that must not offer wantTool yet (-1: none)
		after       int    // model call that must offer wantTool when granted
		wantTool    string
		wantGranted bool
	}{
		{name: "first iteration", responses: []*llm.ChatResponse{textResponse("done"), textResponse("done")}, requestPoll: 1, requestTag: "forge", before: -1, after: 0, wantTool: "forge_tool", wantGranted: true},
		{name: "iteration boundary", responses: []*llm.ChatResponse{probeCallResponse(), textResponse("done"), textResponse("done")}, requestPoll: 2, requestTag: "forge", before: 0, after: 1, wantTool: "forge_tool", wantGranted: true},
		{name: "closure hold", responses: []*llm.ChatResponse{textResponse("ok"), textResponse("done"), textResponse("done")}, requestPoll: 2, requestTag: "forge", before: 0, after: 1, wantTool: "forge_tool", wantGranted: true},
		{name: "pull that requests nothing", responses: []*llm.ChatResponse{textResponse("ok"), textResponse("done"), textResponse("done")}, requestPoll: 2, before: 0, after: 1, wantTool: "forge_tool"},
		{name: "protected tag refused", responses: []*llm.ChatResponse{textResponse("ok"), textResponse("done"), textResponse("done")}, requestPoll: 2, requestTag: "owner", before: 0, after: 1, wantTool: "owner_tool"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockLLM{responses: tt.responses}
			loop := setupCapabilityLoop(mock, []string{"forge_tool", "owner_tool"}, capTags)
			var probeErr error
			loop.Tools().Register(&tools.Tool{
				Name:        "probe_tool",
				Description: "probe",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
				Handler: func(ctx context.Context, _ map[string]any) (string, error) {
					probeErr = agentctx.RequestTag(ctx, "forge")
					return "ok", nil
				},
			})

			var logs bytes.Buffer
			ctx := logging.WithLogger(context.Background(), slog.New(slog.NewTextHandler(&logs, nil)))
			polls := 0
			var requestErr error
			resp, err := loop.Run(ctx, &Request{
				ConversationID: "conv-1",
				Messages:       []Message{{Role: "user", Content: "run the front-door diagnostic and report back"}},
				PullInput: func(pctx context.Context) []llm.Message {
					polls++
					if polls != tt.requestPoll {
						return nil
					}
					if tt.requestTag != "" {
						requestErr = agentctx.RequestTag(pctx, tt.requestTag)
					}
					return []llm.Message{{Role: "user", Content: "a photo arrived"}}
				},
			}, nil)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if polls < tt.requestPoll {
				t.Fatalf("PullInput polled %d times, never reached poll %d", polls, tt.requestPoll)
			}
			if len(mock.calls) <= tt.after {
				t.Fatalf("model calls = %d, need call %d", len(mock.calls), tt.after)
			}
			if tt.before >= 0 && slices.Contains(toolNames(mock.calls[tt.before].Tools), tt.wantTool) {
				t.Errorf("call %d already offered %s before the pull", tt.before, tt.wantTool)
			}
			// The prompt of the call that offers the tool must also list the
			// tag as active, including call 0, whose prompt was built before
			// the pull at its top.
			wantEntry := `"tag":"` + tt.requestTag + `"`
			if tt.requestTag != "" {
				listed := strings.Contains(mock.calls[tt.after].Messages[0].Content, wantEntry)
				if listed != tt.wantGranted {
					t.Errorf("call %d system prompt lists %s as active = %v, want %v", tt.after, tt.requestTag, listed, tt.wantGranted)
				}
			}
			offered := slices.Contains(toolNames(mock.calls[tt.after].Tools), tt.wantTool)
			if offered != tt.wantGranted {
				t.Errorf("call %d offers %s = %v, want %v (tools %v)", tt.after, tt.wantTool, offered, tt.wantGranted, toolNames(mock.calls[tt.after].Tools))
			}
			switch {
			case tt.wantGranted && requestErr != nil:
				t.Errorf("RequestTag = %v, want nil", requestErr)
			case tt.requestTag == "owner" && requestErr == nil:
				t.Error("RequestTag(owner) succeeded on a protected tag")
			}
			if tt.wantGranted {
				if !slices.Contains(resp.ActiveTags, tt.requestTag) {
					t.Errorf("ActiveTags = %v, want %s", resp.ActiveTags, tt.requestTag)
				}
				if !loop.LastRunTags()[tt.requestTag] {
					t.Errorf("dashboard tag snapshot %v lacks %s", loop.LastRunTags(), tt.requestTag)
				}
				line := ""
				for _, l := range strings.Split(logs.String(), "\n") {
					if strings.Contains(l, `msg="capability activated"`) && strings.Contains(l, "via=mid_turn_input") {
						line = l
					}
				}
				if line == "" || !strings.Contains(line, "request_id=") || !strings.Contains(line, "tag="+tt.requestTag) {
					t.Errorf("activation not logged on the run logger:\n%s", logs.String())
				}
			}
			if tt.name == "iteration boundary" && !errors.Is(probeErr, agentctx.ErrNoTagRequester) {
				t.Errorf("a tool call's context carries a tag requester (RequestTag = %v)", probeErr)
			}
		})
	}
}
