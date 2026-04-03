package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/llm"
)

func TestLMStudioPingAndListModelInfos(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("Authorization = %q, want Bearer token", got)
		}
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(lmStudioModelsResponse{
				Data: []LMStudioModelInfo{
					{ID: "gpt-oss:20b"},
					{ID: "qwen3:8b"},
				},
			})
		case "/api/v0/models":
			_ = json.NewEncoder(w).Encode(lmStudioModelsResponse{
				Data: []LMStudioModelInfo{
					{
						ID:                  "google/gemma-3-4b",
						Type:                "vlm",
						Publisher:           "google",
						Arch:                "gemma3",
						CompatibilityType:   "mlx",
						Quantization:        "4bit",
						State:               "loaded",
						MaxContextLength:    131072,
						LoadedContextLength: 4096,
					},
					{
						ID:                "text-embedding-nomic-embed-text-v1.5",
						Type:              "embeddings",
						Publisher:         "nomic-ai",
						Arch:              "nomic-bert",
						CompatibilityType: "gguf",
						Quantization:      "Q4_K_M",
						State:             "not-loaded",
						MaxContextLength:  2048,
					},
				},
			})
		default:
			t.Fatalf("path = %q, want /v1/models or /api/v0/models", r.URL.Path)
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
	if models[0].LoadedContextLength != 4096 || models[0].MaxContextLength != 131072 {
		t.Fatalf("gemma context metadata = %+v, want loaded=4096 max=131072", models[0])
	}
	if models[1].Type != "embeddings" {
		t.Fatalf("embedding model type = %q, want embeddings", models[1].Type)
	}
}

func TestLMStudioListModelInfos_FallsBackToOpenAIEndpoint(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/models":
			http.Error(w, `{"error":"Unexpected endpoint or method."}`, http.StatusNotFound)
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(lmStudioModelsResponse{
				Data: []LMStudioModelInfo{{ID: "qwen3:8b"}},
			})
		default:
			t.Fatalf("path = %q, want /api/v0/models or /v1/models", r.URL.Path)
		}
	}))
	defer srv.Close()

	client := NewLMStudioClient(srv.URL, "", nil)
	models, err := client.ListModelInfos(context.Background())
	if err != nil {
		t.Fatalf("ListModelInfos() error = %v", err)
	}
	if len(models) != 1 || models[0].ID != "qwen3:8b" {
		t.Fatalf("models = %+v, want v1 fallback result", models)
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
		var req lmStudioChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Stream {
			t.Fatal("expected non-streaming request")
		}
		if len(req.Tools) != 1 {
			t.Fatalf("len(req.Tools) = %d, want 1", len(req.Tools))
		}
		_ = json.NewEncoder(w).Encode(lmStudioChatResponse{
			Model:   "deepslate/qwen3:8b",
			Created: 1712160000,
			Choices: []lmStudioChatChoice{
				{
					Index: 0,
					Message: &lmStudioMessageResponse{
						Role: "assistant",
						ToolCalls: []lmStudioToolCallDelta{
							{
								ID:   "call_1",
								Type: "function",
								Function: lmStudioToolFunctionDelta{
									Name:      "get_state",
									Arguments: `{"entity_id":"sun.sun"}`,
								},
							},
						},
					},
				},
			},
			Usage: &lmStudioUsage{PromptTokens: 42, CompletionTokens: 5},
		})
	}))
	defer srv.Close()

	client := NewLMStudioClient(srv.URL, "secret-token", nil)
	resp, err := client.Chat(context.Background(), "qwen3:8b", []llm.Message{{Role: "user", Content: "check the sun"}}, []map[string]any{
		{"type": "function", "function": map[string]any{"name": "get_state"}},
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
	if got := resp.Message.ToolCalls[0].Function.Name; got != "get_state" {
		t.Fatalf("tool name = %q, want get_state", got)
	}
	if got := resp.Message.ToolCalls[0].Function.Arguments["entity_id"]; got != "sun.sun" {
		t.Fatalf("tool args entity_id = %v, want sun.sun", got)
	}
}

func TestLMStudioChat_NonStreamingContent(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(lmStudioChatResponse{
			Model:   "deepslate/google/gemma-3-4b",
			Created: 1712160000,
			Choices: []lmStudioChatChoice{
				{
					Index: 0,
					Message: &lmStudioMessageResponse{
						Role:    "assistant",
						Content: "ok\n",
					},
				},
			},
			Usage: &lmStudioUsage{PromptTokens: 13, CompletionTokens: 3},
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
	if resp.Message.Content != "ok\n" {
		t.Fatalf("resp.Message.Content = %q, want %q", resp.Message.Content, "ok\n")
	}
	if resp.InputTokens != 13 || resp.OutputTokens != 3 {
		t.Fatalf("usage = in:%d out:%d, want 13/3", resp.InputTokens, resp.OutputTokens)
	}
}

func TestLMStudioChat_NonStreamingEmptyCompletionErrors(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(lmStudioChatResponse{
			Model: "deepslate/google/gemma-3-4b",
			Choices: []lmStudioChatChoice{
				{
					Index: 0,
					Message: &lmStudioMessageResponse{
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
		var req lmStudioChatRequest
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
		writeChunk := func(chunk lmStudioChatResponse) {
			data, err := json.Marshal(chunk)
			if err != nil {
				t.Fatalf("marshal chunk: %v", err)
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		writeChunk(lmStudioChatResponse{
			Model:   "deepslate/qwen3:8b",
			Created: 1712160000,
			Choices: []lmStudioChatChoice{{
				Index: 0,
				Delta: &lmStudioChatDelta{Role: "assistant", Content: "hel"},
			}},
		})
		writeChunk(lmStudioChatResponse{
			Model: "deepslate/qwen3:8b",
			Choices: []lmStudioChatChoice{{
				Index: 0,
				Delta: &lmStudioChatDelta{Content: "lo"},
			}},
		})
		writeChunk(lmStudioChatResponse{
			Choices: []lmStudioChatChoice{{
				Index: 0,
				Delta: &lmStudioChatDelta{
					ToolCalls: []lmStudioToolCallDelta{{
						Index: 0,
						ID:    "call_1",
						Type:  "function",
						Function: lmStudioToolFunctionDelta{
							Name:      "get_state",
							Arguments: `{"entity_id":"`,
						},
					}},
				},
			}},
		})
		writeChunk(lmStudioChatResponse{
			Choices: []lmStudioChatChoice{{
				Index: 0,
				Delta: &lmStudioChatDelta{
					ToolCalls: []lmStudioToolCallDelta{{
						Index: 0,
						Function: lmStudioToolFunctionDelta{
							Arguments: `sun.sun"}`,
						},
					}},
				},
			}},
			Usage: &lmStudioUsage{PromptTokens: 11, CompletionTokens: 7},
		})
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	client := NewLMStudioClient(srv.URL, "", nil)
	var tokens []string
	resp, err := client.ChatStream(context.Background(), "qwen3:8b", []llm.Message{{Role: "user", Content: "say hello and plan a tool call"}}, []map[string]any{
		{"type": "function", "function": map[string]any{"name": "get_state"}},
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
	if got := resp.Message.ToolCalls[0].Function.Name; got != "get_state" {
		t.Fatalf("tool name = %q, want get_state", got)
	}
	if got := resp.Message.ToolCalls[0].Function.Arguments["entity_id"]; got != "sun.sun" {
		t.Fatalf("tool args entity_id = %v, want sun.sun", got)
	}
}
