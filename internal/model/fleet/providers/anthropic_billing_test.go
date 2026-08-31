package providers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

const billingRefusalBody = `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`

// scriptedRoundTripper serves canned responses and counts requests.
type scriptedRoundTripper struct {
	calls     int
	responses []*http.Response
}

func (rt *scriptedRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	i := rt.calls
	rt.calls++
	if i >= len(rt.responses) {
		i = len(rt.responses) - 1
	}
	resp := *rt.responses[i]
	resp.Body = io.NopCloser(strings.NewReader(rt.responses[i].Header.Get("X-Test-Body")))
	return &resp, nil
}

func cannedResponse(status int, body string) *http.Response {
	h := http.Header{}
	h.Set("X-Test-Body", body)
	return &http.Response{StatusCode: status, Header: h}
}

func billingClientUnderTest(rt *scriptedRoundTripper) *AnthropicClient {
	c := NewAnthropicClient("test-key", slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.httpClient = &http.Client{Transport: rt}
	return c
}

// TestAnthropicBillingLifecycle drives the whole state machine through
// ChatStream against a scripted transport: classification into a typed
// billing error, one hook call per edge, fast-fail without HTTP between
// probes, a probe that discovers recovery, and the cleared state.
func TestAnthropicBillingLifecycle(t *testing.T) {
	t.Parallel()

	rt := &scriptedRoundTripper{responses: []*http.Response{
		cannedResponse(400, billingRefusalBody),
		cannedResponse(200, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`),
	}}
	c := billingClientUnderTest(rt)

	// The hook runs synchronously on the request path, so the slice
	// needs no synchronization in this single-goroutine test.
	type edge struct {
		blocked bool
		detail  string
	}
	var edges []edge
	c.SetBillingTransitionHook(func(blocked bool, detail string) {
		edges = append(edges, edge{blocked, detail})
	})

	ctx := context.Background()
	msgs := []llm.Message{{Role: "user", Content: "hi"}}

	// 1. The billing 400 classifies as a typed billing error and fires
	// the blocked edge exactly once.
	_, err := c.ChatStream(ctx, "claude-test", msgs, nil, nil)
	if err == nil {
		t.Fatal("want billing error, got nil")
	}
	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) || !apiErr.Billing || apiErr.StatusCode != 400 {
		t.Fatalf("error = %#v, want typed billing APIError with status 400", err)
	}
	if !strings.HasPrefix(err.Error(), "anthropic API error 400: ") {
		t.Fatalf("error text %q lost the historical prefix", err.Error())
	}
	if !llm.IsBillingBlocked(err) {
		t.Fatal("IsBillingBlocked(err) = false, want true")
	}
	if rt.calls != 1 {
		t.Fatalf("HTTP calls = %d, want 1", rt.calls)
	}

	// 2. Calls inside the probe window fail fast with the same typed
	// error and no HTTP round-trip, and the hook stays quiet.
	for i := 0; i < 5; i++ {
		_, err = c.ChatStream(ctx, "claude-test", msgs, nil, nil)
		if !llm.IsBillingBlocked(err) {
			t.Fatalf("fast-fail call %d: err = %v, want billing block", i, err)
		}
	}
	if rt.calls != 1 {
		t.Fatalf("HTTP calls after fast-fail window = %d, want still 1", rt.calls)
	}

	// 3. Snapshot reports the standing state, carrying the extracted
	// upstream sentence — operators read this in the annunciator and
	// the core wake, and a raw JSON blob is not a sentence.
	snap := c.BillingSnapshot()
	if snap == nil || !snap.Blocked || !strings.Contains(snap.Detail, "credit balance") {
		t.Fatalf("BillingSnapshot = %+v, want blocked with detail", snap)
	}
	if strings.Contains(snap.Detail, "{") {
		t.Fatalf("snapshot detail is raw JSON, want the extracted sentence: %q", snap.Detail)
	}
	if len(edges) != 1 || strings.Contains(edges[0].detail, "{") {
		t.Fatalf("hook detail should be the extracted sentence: %+v", edges)
	}

	// 4. Once the probe window lapses, the next call goes through,
	// discovers recovery, clears the state, and fires the cleared edge.
	c.billing.mu.Lock()
	c.billing.retryAt = time.Now().Add(-time.Second)
	c.billing.mu.Unlock()
	if _, err := c.ChatStream(ctx, "claude-test", msgs, nil, nil); err != nil {
		t.Fatalf("recovery probe: %v", err)
	}
	if rt.calls != 2 {
		t.Fatalf("HTTP calls after probe = %d, want 2", rt.calls)
	}
	if c.BillingSnapshot() != nil {
		t.Fatal("BillingSnapshot still non-nil after recovery")
	}

	if len(edges) != 2 || !edges[0].blocked || edges[1].blocked {
		t.Fatalf("hook edges = %+v, want exactly [blocked, cleared]", edges)
	}
}

// TestAnthropicBillingRepeatRefusalIsNotANewEdge: a probe that finds
// the account still blocked re-arms the window without firing the hook
// again.
func TestAnthropicBillingRepeatRefusalIsNotANewEdge(t *testing.T) {
	t.Parallel()

	rt := &scriptedRoundTripper{responses: []*http.Response{
		cannedResponse(400, billingRefusalBody),
	}}
	c := billingClientUnderTest(rt)
	hookCalls := 0
	c.SetBillingTransitionHook(func(bool, string) { hookCalls++ })

	ctx := context.Background()
	msgs := []llm.Message{{Role: "user", Content: "hi"}}
	_, _ = c.ChatStream(ctx, "claude-test", msgs, nil, nil)
	c.billing.mu.Lock()
	c.billing.retryAt = time.Now().Add(-time.Second)
	c.billing.mu.Unlock()
	_, _ = c.ChatStream(ctx, "claude-test", msgs, nil, nil) // probe, still blocked

	if rt.calls != 2 {
		t.Fatalf("HTTP calls = %d, want 2 (initial + one probe)", rt.calls)
	}
	if hookCalls != 1 {
		t.Fatalf("hook calls = %d, want 1 (edges only)", hookCalls)
	}
}

// TestAnthropicBillingBodyClassification pins the narrow marker: only
// a 400 carrying the credit-balance message is billing state.
func TestAnthropicBillingBodyClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"credit balance 400", 400, billingRefusalBody, true},
		{"case insensitive", 400, `Credit Balance too low`, true},
		{"other 400", 400, `{"error":{"type":"invalid_request_error","message":"max_tokens required"}}`, false},
		{"credit text on 429 is not billing", 429, billingRefusalBody, false},
		{"500", 500, "internal", false},
	}
	for _, tt := range tests {
		if got := anthropicBillingBody(tt.status, tt.body); got != tt.want {
			t.Errorf("%s: anthropicBillingBody = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestAnthropicErrorMessageExtraction: the envelope yields its
// sentence; unfamiliar shapes fall back to the raw (trimmed) body.
func TestAnthropicErrorMessageExtraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, body, want string
	}{
		{"envelope", billingRefusalBody, "Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."},
		{"plain text", "  credit balance too low  ", "credit balance too low"},
		{"json without message", `{"error":{}}`, `{"error":{}}`},
	}
	for _, tt := range tests {
		if got := anthropicErrorMessage(tt.body); got != tt.want {
			t.Errorf("%s: anthropicErrorMessage = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestAnthropicPingDrivesBillingState pins the active probe's engine:
// Ping clears the blocked state on success (firing the recovery edge)
// and re-arms it quietly when the wall is still up — this is what keeps
// recovery detection traffic-independent while every loop is backed
// off past the probe window.
func TestAnthropicPingDrivesBillingState(t *testing.T) {
	t.Parallel()

	rt := &scriptedRoundTripper{responses: []*http.Response{
		cannedResponse(400, billingRefusalBody),                  // ChatStream: enter blocked
		cannedResponse(400, billingRefusalBody),                  // Ping: wall still up
		cannedResponse(200, `{"content":[],"stop_reason":null}`), // Ping: recovered
	}}
	c := billingClientUnderTest(rt)
	hookEdges := 0
	c.SetBillingTransitionHook(func(bool, string) { hookEdges++ })

	ctx := context.Background()
	_, _ = c.ChatStream(ctx, "claude-test", []llm.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if c.BillingSnapshot() == nil {
		t.Fatal("setup: not blocked after billing 400")
	}

	_ = c.Ping(ctx) // still walled: re-arms, no new edge
	if c.BillingSnapshot() == nil {
		t.Fatal("Ping against the wall must keep the blocked state")
	}
	if hookEdges != 1 {
		t.Fatalf("hook edges after walled probe = %d, want 1", hookEdges)
	}

	if err := c.Ping(ctx); err != nil {
		t.Fatalf("recovered Ping: %v", err)
	}
	if c.BillingSnapshot() != nil {
		t.Fatal("Ping success must clear the blocked state")
	}
	if hookEdges != 2 {
		t.Fatalf("hook edges after recovery = %d, want 2", hookEdges)
	}
}
