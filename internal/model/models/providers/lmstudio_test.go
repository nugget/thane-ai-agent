package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
			_ = json.NewEncoder(w).Encode(lmStudioModelsResponse{
				Data: []LMStudioModelInfo{{ID: "gpt-oss:20b"}, {ID: "qwen3:8b"}},
			})
		case "/api/v1/models":
			_ = json.NewEncoder(w).Encode(lmStudioV1ModelsResponse{
				Models: []lmStudioV1ModelInfo{
					{
						Type:             "vlm",
						Publisher:        "google",
						Key:              "google/gemma-3-4b",
						Architecture:     "gemma3",
						Quantization:     &lmStudioV1Quantization{Name: "4bit"},
						MaxContextLength: 131072,
						Format:           "mlx",
						Capabilities: &lmStudioV1ModelCapabilities{
							Vision:            true,
							TrainedForToolUse: false,
						},
						LoadedInstances: []lmStudioV1LoadedInstance{
							{ID: "google/gemma-3-4b", Config: lmStudioV1LoadConfig{ContextLength: 4096}},
							{ID: "google/gemma-3-4b:2", Config: lmStudioV1LoadConfig{ContextLength: 24000}},
						},
					},
					{
						Type:             "embedding",
						Publisher:        "nomic-ai",
						Key:              "text-embedding-nomic-embed-text-v1.5",
						Architecture:     "nomic-bert",
						Quantization:     &lmStudioV1Quantization{Name: "Q4_K_M"},
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
			_ = json.NewEncoder(w).Encode(lmStudioModelsResponse{
				Data: []LMStudioModelInfo{{ID: "qwen3:8b", Type: "llm"}},
			})
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(lmStudioModelsResponse{
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
			_ = json.NewEncoder(w).Encode(lmStudioModelsResponse{
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
		if req.TTL != 600 {
			t.Fatalf("req.TTL = %d, want 600", req.TTL)
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

	client := NewLMStudioClientWithTTL(srv.URL, "secret-token", nil, 600)
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
		_ = json.NewEncoder(w).Encode(lmStudioChatResponse{
			Model: "deepslate/qwen3:8b",
			Choices: []lmStudioChatChoice{{
				Index: 0,
				Message: &lmStudioMessageResponse{
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
	if got := resp.Message.ToolCalls[0].Function.Name; got != "get_state" {
		t.Fatalf("tool name = %q, want get_state", got)
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
		writeChunk := func(chunk lmStudioChatResponse) {
			data, err := json.Marshal(chunk)
			if err != nil {
				t.Fatalf("marshal chunk: %v", err)
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}

		writeChunk(lmStudioChatResponse{
			Model: "deepslate/google/gemma-3-4b",
			Choices: []lmStudioChatChoice{{
				Index: 0,
				Delta: &lmStudioChatDelta{
					ToolCalls: []lmStudioToolCallDelta{{
						Index: 0,
						ID:    "call_1",
						Type:  "function",
						Function: lmStudioToolFunctionDelta{
							Name:      "set_next_sleep",
							Arguments: `{"duration":"5m"}`,
						},
					}},
				},
			}},
			Usage: &lmStudioUsage{PromptTokens: 9, CompletionTokens: 4},
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
