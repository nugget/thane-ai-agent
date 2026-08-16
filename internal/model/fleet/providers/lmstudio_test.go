package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

func TestLMStudioPingAndListModelInfos(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("Authorization = %q, want Bearer token", got)
		}
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(openAICompatModelsResponse{
				Data: []LMStudioModelInfo{{ID: "gpt-oss:20b"}, {ID: "qwen3:8b"}},
			})
		case "/api/v1/models":
			_ = json.NewEncoder(w).Encode(openAICompatV1ModelsResponse{
				Models: []openAICompatV1ModelInfo{
					{
						Type:             "vlm",
						Publisher:        "google",
						Key:              "google/gemma-3-4b",
						Architecture:     "gemma3",
						Quantization:     &openAICompatV1Quantization{Name: "4bit"},
						MaxContextLength: 131072,
						Format:           "mlx",
						Capabilities: &openAICompatV1ModelCapabilities{
							Vision:            true,
							TrainedForToolUse: false,
						},
						LoadedInstances: []openAICompatV1LoadedInstance{
							{ID: "google/gemma-3-4b", Config: openAICompatV1LoadConfig{ContextLength: 4096}},
							{ID: "google/gemma-3-4b:2", Config: openAICompatV1LoadConfig{ContextLength: 24000}},
						},
					},
					{
						Type:             "embedding",
						Publisher:        "nomic-ai",
						Key:              "text-embedding-nomic-embed-text-v1.5",
						Architecture:     "nomic-bert",
						Quantization:     &openAICompatV1Quantization{Name: "Q4_K_M"},
						MaxContextLength: 2048,
						Format:           "gguf",
					},
				},
			})
		default:
			t.Fatalf("path = %q, want /v1/models or /api/v1/models", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := NewLMStudioClient(srv.URL, "secret-token", nil)
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}

	models, err := client.ListModelInfos(context.Background())
	if err != nil {
		t.Fatalf("ListModelInfos() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].ID != "google/gemma-3-4b" || models[1].ID != "text-embedding-nomic-embed-text-v1.5" {
		t.Fatalf("models = %+v", models)
	}
	if models[0].LoadedContextLength != 24000 || models[0].MaxContextLength != 131072 {
		t.Fatalf("gemma context metadata = %+v, want loaded=24000 max=131072", models[0])
	}
	if !models[0].Vision {
		t.Fatalf("gemma vision metadata = %+v, want vision=true", models[0])
	}
	if models[1].Type != "embedding" {
		t.Fatalf("embedding model type = %q, want embedding", models[1].Type)
	}
}

func TestLMStudioListModelInfos_FallsBackToV0Endpoint(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			http.Error(w, `{"error":"Unexpected endpoint or method."}`, http.StatusNotFound)
		case "/api/v0/models":
			_ = json.NewEncoder(w).Encode(openAICompatModelsResponse{
				Data: []LMStudioModelInfo{{ID: "qwen3:8b", Type: "llm"}},
			})
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(openAICompatModelsResponse{
				Data: []LMStudioModelInfo{{ID: "qwen3:8b"}},
			})
		default:
			t.Fatalf("path = %q, want /api/v1/models, /api/v0/models, or /v1/models", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := NewLMStudioClient(srv.URL, "", nil)
	models, err := client.ListModelInfos(context.Background())
	if err != nil {
		t.Fatalf("ListModelInfos() error = %v", err)
	}
	if len(models) != 1 || models[0].ID != "qwen3:8b" {
		t.Fatalf("models = %+v, want v0 fallback result", models)
	}
}

func TestLMStudioListModelInfos_FallsBackToOpenAIEndpoint(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models", "/api/v0/models":
			http.Error(w, `{"error":"Unexpected endpoint or method."}`, http.StatusNotFound)
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(openAICompatModelsResponse{
				Data: []LMStudioModelInfo{{ID: "qwen3:8b"}},
			})
		default:
			t.Fatalf("path = %q, want /api/v1/models, /api/v0/models, or /v1/models", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := NewLMStudioClient(srv.URL, "", nil)
	models, err := client.ListModelInfos(context.Background())
	if err != nil {
		t.Fatalf("ListModelInfos() error = %v", err)
	}
	if len(models) != 1 || models[0].ID != "qwen3:8b" {
		t.Fatalf("models = %+v, want openai fallback result", models)
	}
}

func TestToLMStudioMessages_EmptyContentIsEmittedNotOmitted(t *testing.T) {
	t.Parallel()

	// An assistant message carrying only tool_calls and a tool result with
	// empty output must still serialize a `content` field. Omitting it (the
	// prior behavior: unset `any` + `omitempty`) made LM Studio reject the
	// request with 400 "content field must be a string or an array of
	// objects".
	var tc llm.ToolCall
	tc.ID = "tc1"
	tc.Function.Name = "replace_output_climate_bench_qwen"
	tc.Function.Arguments = map[string]any{"body": "..."}

	msgs := []llm.Message{
		{Role: "user", Content: "update the doc"},
		{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{tc}}, // tool call, no text
		{Role: "tool", Content: "", ToolCallID: "tc1"},                  // empty tool result
	}

	wire, err := toOpenAICompatMessages(msgs)
	if err != nil {
		t.Fatalf("toOpenAICompatMessages: %v", err)
	}
	b, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Inspect the actual top-level message objects rather than substring-matching
	// the whole payload — a "content" key nested inside tool-call arguments must
	// not be mistaken for a message-level content field.
	var decoded []map[string]json.RawMessage
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal wire: %v", err)
	}
	if len(decoded) != len(msgs) {
		t.Fatalf("decoded %d messages, want %d", len(decoded), len(msgs))
	}
	for i, m := range decoded {
		raw, ok := m["content"]
		if !ok {
			t.Errorf("message %d (role %q) has no content field — LM Studio 400s on this", i, msgs[i].Role)
			continue
		}
		// The empty-content assistant tool-call and tool result must serialize an
		// empty string, not a dropped field or a JSON null.
		if msgs[i].Content == "" {
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				t.Errorf("message %d (role %q) content is not a string: %s", i, msgs[i].Role, raw)
			} else if s != "" {
				t.Errorf("message %d (role %q) content = %q, want empty string", i, msgs[i].Role, s)
			}
		}
	}
}

func TestDecodeLMStudioToolCalls_SynthesizesUniqueMissingIDs(t *testing.T) {
	t.Parallel()

	calls, err := decodeOpenAICompatToolCalls(map[int]*openAICompatToolAccumulator{
		0: {Name: "first"},
		1: {Name: "second"},
		2: {ID: "runner_call_3", Name: "third"},
	})
	if err != nil {
		t.Fatalf("decodeOpenAICompatToolCalls() error = %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("tool calls = %d, want 3", len(calls))
	}
	for i := range 2 {
		if !strings.HasPrefix(calls[i].ID, "call_") {
			t.Errorf("tool call %d ID = %q, want generated call_ prefix", i, calls[i].ID)
		}
	}
	if calls[0].ID == calls[1].ID {
		t.Errorf("generated tool call IDs are not unique: %q", calls[0].ID)
	}
	if calls[2].ID != "runner_call_3" {
		t.Errorf("runner-supplied tool call ID = %q, want runner_call_3", calls[2].ID)
	}
}

func TestApplyTextToolFallback_SynthesizesMissingID(t *testing.T) {
	t.Parallel()

	resp := &llm.ChatResponse{
		Message: llm.Message{
			Content: `{"name":"echo","arguments":{"text":"probe"}}`,
		},
	}
	if err := applyTextToolFallback(resp, []string{"echo"}); err != nil {
		t.Fatalf("applyTextToolFallback() error = %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.Message.ToolCalls))
	}
	if got := resp.Message.ToolCalls[0].ID; !strings.HasPrefix(got, "call_") {
		t.Fatalf("tool call ID = %q, want generated call_ prefix", got)
	}
	if resp.Message.Content != "" {
		t.Fatalf("content = %q, want empty after text fallback", resp.Message.Content)
	}
}

func TestLMStudioChat_NonStreamingToolCalls(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("Authorization = %q, want Bearer token", got)
		}
		var req openAICompatChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Stream {
			t.Fatal("expected non-streaming request")
		}
		if len(req.Tools) != 1 {
			t.Fatalf("len(req.Tools) = %d, want 1", len(req.Tools))
		}
		if req.TTL != 600 {
			t.Fatalf("req.TTL = %d, want 600", req.TTL)
		}
		_ = json.NewEncoder(w).Encode(openAICompatChatResponse{
			Model:   "deepslate/qwen3:8b",
			Created: 1712160000,
			Choices: []openAICompatChatChoice{
				{
					Index: 0,
					Message: &openAICompatMessageResponse{
						Role: "assistant",
						ToolCalls: []openAICompatToolCallDelta{
							{
								ID:   "call_1",
								Type: "function",
								Function: openAICompatToolFunctionDelta{
									Name:      "ha_get_state",
									Arguments: `{"entity_id":"sun.sun"}`,
								},
							},
						},
					},
				},
			},
			Usage: &openAICompatUsage{PromptTokens: 42, CompletionTokens: 5},
		})
	}))
	defer srv.Close()

	client := NewLMStudioClientWithTTL(srv.URL, "secret-token", nil, 600)
	resp, err := client.Chat(context.Background(), "qwen3:8b", []llm.Message{{Role: "user", Content: "check the sun"}}, []map[string]any{
		{"type": "function", "function": map[string]any{"name": "ha_get_state"}},
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Model != "deepslate/qwen3:8b" {
		t.Fatalf("resp.Model = %q, want %q", resp.Model, "deepslate/qwen3:8b")
	}
	if resp.InputTokens != 42 || resp.OutputTokens != 5 {
		t.Fatalf("usage = in:%d out:%d, want 42/5", resp.InputTokens, resp.OutputTokens)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("len(tool_calls) = %d, want 1", len(resp.Message.ToolCalls))
	}
	if got := resp.Message.ToolCalls[0].ID; got != "call_1" {
		t.Fatalf("tool ID = %q, want call_1", got)
	}
	if got := resp.Message.ToolCalls[0].Function.Name; got != "ha_get_state" {
		t.Fatalf("tool name = %q, want ha_get_state", got)
	}
	if got := resp.Message.ToolCalls[0].Function.Arguments["entity_id"]; got != "sun.sun" {
		t.Fatalf("tool args entity_id = %v, want sun.sun", got)
	}
}

func TestLMStudioChat_DefaultIdleTTLOmitsRequestField(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if strings.Contains(string(body), `"ttl":`) {
			t.Fatalf("request body unexpectedly contained ttl field: %s", string(body))
		}
		_ = json.NewEncoder(w).Encode(openAICompatChatResponse{
			Model: "deepslate/qwen3:8b",
			Choices: []openAICompatChatChoice{{
				Index: 0,
				Message: &openAICompatMessageResponse{
					Role:    "assistant",
					Content: "ok",
				},
			}},
		})
	}))
	defer srv.Close()

	client := NewLMStudioClient(srv.URL, "", nil)
	resp, err := client.Chat(context.Background(), "qwen3:8b", []llm.Message{{Role: "user", Content: "ok?"}}, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Message.Content != "ok" {
		t.Fatalf("resp.Message.Content = %q, want ok", resp.Message.Content)
	}
}

func TestLMStudioChat_NonStreamingContent(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(openAICompatChatResponse{
			Model:   "deepslate/google/gemma-3-4b",
			Created: 1712160000,
			Choices: []openAICompatChatChoice{
				{
					Index: 0,
					Message: &openAICompatMessageResponse{
						Role:    "assistant",
						Content: "ok\n",
					},
				},
			},
			Usage: &openAICompatUsage{PromptTokens: 13, CompletionTokens: 3},
		})
	}))
	defer srv.Close()

	client := NewLMStudioClient(srv.URL, "", nil)
	resp, err := client.Chat(context.Background(), "google/gemma-3-4b", []llm.Message{{Role: "user", Content: "Reply with exactly ok"}}, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if resp.Model != "deepslate/google/gemma-3-4b" {
		t.Fatalf("resp.Model = %q, want %q", resp.Model, "deepslate/google/gemma-3-4b")
	}
	if resp.Message.Role != "assistant" {
		t.Fatalf("resp.Message.Role = %q, want assistant", resp.Message.Role)
	}
	if resp.Message.Content != "ok\n" {
		t.Fatalf("resp.Message.Content = %q, want %q", resp.Message.Content, "ok\n")
	}
	if resp.InputTokens != 13 || resp.OutputTokens != 3 {
		t.Fatalf("usage = in:%d out:%d, want 13/3", resp.InputTokens, resp.OutputTokens)
	}
}

func TestLMStudioLoadModel(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models/load" {
			t.Fatalf("path = %q, want /api/v1/models/load", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("Authorization = %q, want Bearer token", got)
		}
		var req lmStudioLoadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "google/gemma-3-4b" {
			t.Fatalf("req.Model = %q, want google/gemma-3-4b", req.Model)
		}
		if req.ContextLength != 12288 {
			t.Fatalf("req.ContextLength = %d, want 12288", req.ContextLength)
		}
		if !req.EchoLoadConfig {
			t.Fatal("EchoLoadConfig = false, want true")
		}
		_ = json.NewEncoder(w).Encode(LMStudioLoadResponse{
			Type:            "llm",
			InstanceID:      "google/gemma-3-4b",
			LoadTimeSeconds: 1.25,
			Status:          "loaded",
			LoadConfig: map[string]any{
				"context_length": float64(12288),
			},
		})
	}))
	defer srv.Close()

	client := NewLMStudioClient(srv.URL, "secret-token", nil)
	resp, err := client.LoadModel(context.Background(), "google/gemma-3-4b", 12288)
	if err != nil {
		t.Fatalf("LoadModel() error = %v", err)
	}
	if resp.Status != "loaded" {
		t.Fatalf("resp.Status = %q, want loaded", resp.Status)
	}
	if resp.InstanceID != "google/gemma-3-4b" {
		t.Fatalf("resp.InstanceID = %q, want google/gemma-3-4b", resp.InstanceID)
	}
	if got := resp.LoadConfig["context_length"]; got != float64(12288) {
		t.Fatalf("resp.LoadConfig[context_length] = %v, want 12288", got)
	}
}

func TestLMStudioChat_NonStreamingEmptyCompletionErrors(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(openAICompatChatResponse{
			Model: "deepslate/google/gemma-3-4b",
			Choices: []openAICompatChatChoice{
				{
					Index: 0,
					Message: &openAICompatMessageResponse{
						Role:    "assistant",
						Content: "",
					},
				},
			},
		})
	}))
	defer srv.Close()

	client := NewLMStudioClient(srv.URL, "", nil)
	_, err := client.Chat(context.Background(), "google/gemma-3-4b", []llm.Message{{Role: "user", Content: "Reply with exactly ok"}}, nil)
	if err == nil {
		t.Fatal("Chat() error = nil, want empty completion error")
	}
	if !strings.Contains(err.Error(), "empty assistant completion") {
		t.Fatalf("Chat() error = %q, want empty completion message", err)
	}
}

func TestLMStudioChatStream_ContentAndToolCalls(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		var req openAICompatChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !req.Stream {
			t.Fatal("expected streaming request")
		}
		if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
			t.Fatal("expected stream_options.include_usage")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeChunk := func(chunk openAICompatChatResponse) {
			data, err := json.Marshal(chunk)
			if err != nil {
				t.Fatalf("marshal chunk: %v", err)
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		writeChunk(openAICompatChatResponse{
			Model:   "deepslate/qwen3:8b",
			Created: 1712160000,
			Choices: []openAICompatChatChoice{{
				Index: 0,
				Delta: &openAICompatChatDelta{Role: "assistant", Content: "hel"},
			}},
		})
		writeChunk(openAICompatChatResponse{
			Model: "deepslate/qwen3:8b",
			Choices: []openAICompatChatChoice{{
				Index: 0,
				Delta: &openAICompatChatDelta{Content: "lo"},
			}},
		})
		writeChunk(openAICompatChatResponse{
			Choices: []openAICompatChatChoice{{
				Index: 0,
				Delta: &openAICompatChatDelta{
					ToolCalls: []openAICompatToolCallDelta{{
						Index: 0,
						ID:    "call_1",
						Type:  "function",
						Function: openAICompatToolFunctionDelta{
							Name:      "ha_get_state",
							Arguments: `{"entity_id":"`,
						},
					}},
				},
			}},
		})
		writeChunk(openAICompatChatResponse{
			Choices: []openAICompatChatChoice{{
				Index: 0,
				Delta: &openAICompatChatDelta{
					ToolCalls: []openAICompatToolCallDelta{{
						Index: 0,
						Function: openAICompatToolFunctionDelta{
							Arguments: `sun.sun"}`,
						},
					}},
				},
			}},
			Usage: &openAICompatUsage{PromptTokens: 11, CompletionTokens: 7},
		})
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := NewLMStudioClient(srv.URL, "", nil)
	var tokens []string
	resp, err := client.ChatStream(context.Background(), "qwen3:8b", []llm.Message{{Role: "user", Content: "say hello and plan a tool call"}}, []map[string]any{
		{"type": "function", "function": map[string]any{"name": "ha_get_state"}},
	}, func(event llm.StreamEvent) {
		if event.Kind == llm.KindToken {
			tokens = append(tokens, event.Token)
		}
	})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if got := strings.Join(tokens, ""); got != "hello" {
		t.Fatalf("streamed tokens = %q, want %q", got, "hello")
	}
	if resp.Message.Role != "assistant" {
		t.Fatalf("resp.Message.Role = %q, want assistant", resp.Message.Role)
	}
	if resp.Message.Content != "hello" {
		t.Fatalf("resp content = %q, want %q", resp.Message.Content, "hello")
	}
	if resp.Model != "deepslate/qwen3:8b" {
		t.Fatalf("resp.Model = %q, want %q", resp.Model, "deepslate/qwen3:8b")
	}
	if resp.InputTokens != 11 || resp.OutputTokens != 7 {
		t.Fatalf("usage = in:%d out:%d, want 11/7", resp.InputTokens, resp.OutputTokens)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("len(tool_calls) = %d, want 1", len(resp.Message.ToolCalls))
	}
	if got := resp.Message.ToolCalls[0].ID; got != "call_1" {
		t.Fatalf("tool ID = %q, want call_1", got)
	}
	if got := resp.Message.ToolCalls[0].Function.Name; got != "ha_get_state" {
		t.Fatalf("tool name = %q, want ha_get_state", got)
	}
	if got := resp.Message.ToolCalls[0].Function.Arguments["entity_id"]; got != "sun.sun" {
		t.Fatalf("tool args entity_id = %v, want sun.sun", got)
	}
}

func TestLMStudioChatStream_DefaultsAssistantRoleWhenStreamOmitsIt(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeChunk := func(chunk openAICompatChatResponse) {
			data, err := json.Marshal(chunk)
			if err != nil {
				t.Fatalf("marshal chunk: %v", err)
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		writeChunk(openAICompatChatResponse{
			Model: "deepslate/google/gemma-3-4b",
			Choices: []openAICompatChatChoice{{
				Index: 0,
				Delta: &openAICompatChatDelta{
					ToolCalls: []openAICompatToolCallDelta{{
						Index: 0,
						ID:    "call_1",
						Type:  "function",
						Function: openAICompatToolFunctionDelta{
							Name:      "set_next_sleep",
							Arguments: `{"duration":"5m"}`,
						},
					}},
				},
			}},
			Usage: &openAICompatUsage{PromptTokens: 9, CompletionTokens: 4},
		})
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := NewLMStudioClient(srv.URL, "", nil)
	resp, err := client.ChatStream(context.Background(), "google/gemma-3-4b", []llm.Message{{Role: "user", Content: "choose a sleep time"}}, []map[string]any{
		{"type": "function", "function": map[string]any{"name": "set_next_sleep"}},
	}, func(llm.StreamEvent) {})
	if err != nil {
		t.Fatalf("ChatStream() error = %v", err)
	}
	if resp.Message.Role != "assistant" {
		t.Fatalf("resp.Message.Role = %q, want assistant", resp.Message.Role)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("len(tool_calls) = %d, want 1", len(resp.Message.ToolCalls))
	}
}

// The failure this guards against was observed in production: LM Studio
// answers a streaming request it cannot serve with HTTP 200 and an
// `event: error` frame, where the non-streaming call would have answered
// 400. The frame decodes cleanly into openAICompatChatResponse, so before this
// was handled the upstream failure surfaced as a successful zero-token
// completion and the agent's context-reload recovery never fired.
func TestLMStudioChatStream_SurfacesErrorFrame(t *testing.T) {
	t.Parallel()

	const upstream = "The number of tokens to keep from the initial prompt is greater than the context length. Try to load the model with a larger context length, or provide a shorter input"

	tests := []struct {
		name string
		body string
	}{
		{
			name: "event error frame with object payload",
			body: "event: error\ndata: {\"error\":{\"message\":" + strconv.Quote(upstream) + "},\"message\":" + strconv.Quote(upstream) + "}\n\n",
		},
		{
			name: "data frame with string payload",
			body: "data: {\"error\":" + strconv.Quote(upstream) + "}\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()

			client := NewLMStudioClient(srv.URL, "", nil)
			resp, err := client.ChatStream(context.Background(), "qwen/qwen3-coder-next",
				[]llm.Message{{Role: "user", Content: "summarize the repository"}}, nil,
				func(llm.StreamEvent) {})
			if err == nil {
				t.Fatalf("ChatStream() error = nil, want stream error (resp = %+v)", resp)
			}
			if !strings.Contains(err.Error(), upstream) {
				t.Fatalf("ChatStream() error = %q, want it to carry the upstream text verbatim", err)
			}
		})
	}
}

// A stream can also close cleanly having delivered nothing at all. That is
// not a completion either, and reporting success would hand the caller a
// well-formed zero-token response.
func TestLMStudioChatStream_ChunklessStreamErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "empty body", body: ""},
		{name: "done with no chunks", body: "data: [DONE]\n\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()

			client := NewLMStudioClient(srv.URL, "", nil)
			_, err := client.ChatStream(context.Background(), "qwen/qwen3-coder-next",
				[]llm.Message{{Role: "user", Content: "summarize the repository"}}, nil,
				func(llm.StreamEvent) {})
			if err == nil {
				t.Fatal("ChatStream() error = nil, want empty stream error")
			}
			if !strings.Contains(err.Error(), "empty stream") {
				t.Fatalf("ChatStream() error = %q, want empty stream message", err)
			}
		})
	}
}

func TestLMStudioUnloadModel(t *testing.T) {
	t.Parallel()

	var gotPath, gotInstance string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var req struct {
			InstanceID string `json:"instance_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode unload request: %v", err)
		}
		gotInstance = req.InstanceID
		_ = json.NewEncoder(w).Encode(map[string]any{"instance_id": req.InstanceID})
	}))
	defer srv.Close()

	client := NewLMStudioClient(srv.URL, "", nil)
	if err := client.UnloadModel(context.Background(), "google/gemma-3-4b:2"); err != nil {
		t.Fatalf("UnloadModel() error = %v", err)
	}
	if gotPath != "/api/v1/models/unload" {
		t.Fatalf("path = %q, want /api/v1/models/unload", gotPath)
	}
	if gotInstance != "google/gemma-3-4b:2" {
		t.Fatalf("instance_id = %q, want google/gemma-3-4b:2", gotInstance)
	}

	// An instance id is required: LM Studio releases an instance, not a
	// model, and a model may have several loaded at once.
	if err := client.UnloadModel(context.Background(), "  "); err == nil {
		t.Fatal("UnloadModel(blank) error = nil, want instance id required")
	}
}
