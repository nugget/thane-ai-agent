package providers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/platform/httpkit"
)

func partialUsageServer(t *testing.T, body string, truncated bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "header-request")
		if truncated {
			// A declared body longer than the bytes sent makes the real HTTP
			// reader fail after delivering the preceding complete usage frame.
			w.Header().Set("Content-Length", strconv.Itoa(len(body)+64))
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func assertPartialUsage(t *testing.T, response *llm.ChatResponse, err error, wantError string, reported bool) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), wantError) {
		t.Fatalf("error = %v, want %q", err, wantError)
	}
	if !reported {
		if response != nil {
			t.Fatalf("invented usage response: %+v", response)
		}
		return
	}
	if response == nil {
		t.Fatal("discarded provider-reported usage")
	}
	if response.Done || response.Message.Content != "" || len(response.Message.ToolCalls) != 0 {
		t.Fatalf("error returned assistant output: %+v", response)
	}
	if response.InputTokens != 50 || response.OutputTokens != 23 || response.Model != "wire-model" || response.UpstreamRequestID != "header-request" {
		t.Fatalf("lost usage/provenance: %+v", response)
	}
}

func TestAnthropicPartialUsageSurvivesFailure(t *testing.T) {
	usage := `"usage":{"input_tokens":50,"output_tokens":23,"cache_creation_input_tokens":100,"cache_read_input_tokens":200,"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":70}}`
	for _, tc := range []struct {
		name, body, want            string
		stream, truncated, reported bool
	}{
		{"interrupted stream", "data: {\"type\":\"message_start\",\"message\":{\"model\":\"wire-model\"," + usage + "}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"partial answer\"}}\n\n", "read stream", true, true, true},
		{"decoded type error", `{"model":"wire-model","content":42,` + usage + `}`, "decode response", false, false, true},
		{"no usage before interruption", "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n", "read stream", true, true, false},
		{"incomplete JSON is not reported usage", `{"model":"wire-model",` + usage, "decode response", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := partialUsageServer(t, tc.body, tc.truncated)
			target, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			client := NewAnthropicClient("test-key", slog.New(slog.NewTextHandler(io.Discard, nil)))
			transport := httpkit.NewTransport()
			t.Cleanup(transport.CloseIdleConnections)
			client.httpClient = httpkit.NewClient()
			client.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				local := req.Clone(req.Context())
				local.URL.Scheme, local.URL.Host = target.Scheme, target.Host
				return transport.RoundTrip(local)
			})
			var stream llm.StreamCallback
			if tc.stream {
				stream = func(llm.StreamEvent) {}
			}
			response, err := client.ChatStream(context.Background(), "requested-model", []llm.Message{{Role: "user", Content: "source"}}, nil, stream)
			assertPartialUsage(t, response, err, tc.want, tc.reported)
			if tc.reported && (response.CacheCreationInputTokens != 100 || response.CacheCreation5mInputTokens != 30 || response.CacheCreation1hInputTokens != 70 || response.CacheReadInputTokens != 200) {
				t.Fatalf("lost cache duration buckets: %+v", response)
			}
		})
	}
}

func TestOpenAICompatPartialUsageSurvivesFailure(t *testing.T) {
	usage := `"id":"body-request","model":"wire-model","usage":{"prompt_tokens":50,"completion_tokens":23}`
	prefix := "data: {" + usage + ",\"choices\":[]}\n\n"
	text := "data: {\"choices\":[{\"delta\":{\"content\":\"partial answer\"}}]}\n\n"
	for _, tc := range []struct {
		name, body, want            string
		stream, truncated, reported bool
	}{
		{"read failure after usage", prefix + text, "read stream", true, true, true},
		{"error frame after usage", prefix + `data: {"error":{"message":"runner failed"}}` + "\n\n", "runner failed", true, false, true},
		{"usage in frame with type error", "data: {" + usage + ",\"choices\":42}\n\n", "decode stream chunk", true, false, true},
		{"partial usage type error keeps earlier counts", prefix + `data: {"usage":{"prompt_tokens":50,"completion_tokens":"bad"},"choices":[]}` + "\n\n", "decode stream chunk", true, false, true},
		{"empty usage in malformed frame keeps earlier counts", prefix + `data: {"usage":{},"choices":42}` + "\n\n", "decode stream chunk", true, false, true},
		{"malformed frame after usage", prefix + "data: {invalid}\n\n", "decode stream chunk", true, false, true},
		{"silent truncation after usage", prefix + text, "truncated response", true, false, true},
		{"reasoning only after usage", prefix + `data: {"choices":[{"delta":{"reasoning":"work"},"finish_reason":"length"}]}` + "\n\ndata: [DONE]\n\n", "produced only reasoning", true, false, true},
		{"malformed tool after usage", prefix + `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call","function":{"name":"inspect","arguments":"{bad"}}]}}]}` + "\n\ndata: [DONE]\n\n", "arguments", true, false, true},
		{"no usage before read failure", text, "read stream", true, true, false},
		{"nonstream missing choices", `{` + usage + `,"choices":[]}`, "no choices", false, false, true},
		{"nonstream reasoning only", `{` + usage + `,"choices":[{"message":{"role":"assistant","reasoning":"work"},"finish_reason":"length"}]}`, "produced only reasoning", false, false, true},
		{"nonstream malformed tool", `{` + usage + `,"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call","function":{"name":"inspect","arguments":"{bad"}}]}}]}`, "arguments", false, false, true},
		{"nonstream decoded type error", `{` + usage + `,"created":"invalid","choices":[]}`, "decode response", false, false, true},
		{"incomplete JSON is not reported usage", `{` + usage, "decode response", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := partialUsageServer(t, tc.body, tc.truncated)
			client := NewOpenAICompatClient(server.URL, "", "test", "resource", nil, 0)
			var stream llm.StreamCallback
			if tc.stream {
				stream = func(llm.StreamEvent) {}
			}
			response, err := client.ChatStream(context.Background(), "requested-model", []llm.Message{{Role: "user", Content: "source"}}, nil, stream)
			assertPartialUsage(t, response, err, tc.want, tc.reported)
		})
	}
}

func TestOllamaPartialUsageSurvivesDecodeFailure(t *testing.T) {
	usage := `"model":"wire-model","prompt_eval_count":50,"eval_count":23`
	for _, tc := range []struct {
		name, body, want string
		stream, reported bool
	}{
		{"terminal chunk type error", `{` + usage + `,"done":true,"message":{"content":42}}` + "\n", "decode stream chunk", true, true},
		{"usage followed by invalid chunk", `{` + usage + `,"done":false,"message":{"content":"partial"}}` + "\n{invalid}\n", "decode stream chunk", true, true},
		{"partial usage type error keeps earlier counts", `{` + usage + `,"done":false,"message":{"content":"partial"}}` + "\n" + `{"model":"wire-model","prompt_eval_count":50,"eval_count":"bad","done":true}` + "\n", "decode stream chunk", true, true},
		{"nonstream type error", `{` + usage + `,"done":true,"message":{"content":42}}`, "decode response", false, true},
		{"no usage before error", "{invalid}\n", "decode stream chunk", true, false},
		{"incomplete JSON is not reported usage", `{` + usage, "decode response", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := partialUsageServer(t, tc.body, false)
			client := NewOllamaClient(server.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
			var stream llm.StreamCallback
			if tc.stream {
				stream = func(llm.StreamEvent) {}
			}
			response, err := client.ChatStream(context.Background(), "requested-model", []llm.Message{{Role: "user", Content: "source"}}, nil, stream)
			assertPartialUsage(t, response, err, tc.want, tc.reported)
		})
	}
}

func TestOllamaCleanEOFKeepsReportedUsage(t *testing.T) {
	for _, reported := range []bool{false, true} {
		t.Run(strconv.FormatBool(reported), func(t *testing.T) {
			body := `{"model":"wire-model","done":false,"message":{"content":"partial answer"}`
			if reported {
				body += `,"prompt_eval_count":50,"eval_count":23`
			}
			body += "}\n"
			server := partialUsageServer(t, body, false)
			client := NewOllamaClient(server.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
			response, err := client.ChatStream(context.Background(), "requested-model", []llm.Message{{Role: "user", Content: "source"}}, nil, func(llm.StreamEvent) {})
			if err != nil || response == nil || !response.Done || response.Message.Content != "partial answer" {
				t.Fatalf("clean EOF completion policy changed: response=%+v, error=%v", response, err)
			}
			if response.UpstreamRequestID != "header-request" {
				t.Fatalf("lost upstream request ID: %+v", response)
			}
			if reported {
				if response.InputTokens != 50 || response.OutputTokens != 23 || response.Model != "wire-model" {
					t.Fatalf("discarded reported usage: %+v", response)
				}
			} else if response.InputTokens != 0 || response.OutputTokens != 0 {
				t.Fatalf("invented usage: %+v", response)
			}
		})
	}
}
