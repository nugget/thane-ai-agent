package httpkit

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/buildinfo"
)

func TestNewClient_DefaultTimeout(t *testing.T) {
	c := NewClient()
	if c.Timeout != 30*time.Second {
		t.Errorf("expected 30s timeout, got %v", c.Timeout)
	}
}

func TestNewClient_CustomTimeout(t *testing.T) {
	c := NewClient(WithTimeout(5 * time.Second))
	if c.Timeout != 5*time.Second {
		t.Errorf("expected 5s timeout, got %v", c.Timeout)
	}
}

func TestNewClient_ZeroTimeout(t *testing.T) {
	c := NewClient(WithTimeout(0))
	if c.Timeout != 0 {
		t.Errorf("expected 0 timeout for streaming, got %v", c.Timeout)
	}
}

func TestNewClient_UserAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Header.Get("User-Agent")))
	}))
	defer srv.Close()

	c := NewClient(WithUserAgent("TestBot/1.0"))
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "TestBot/1.0" {
		t.Errorf("expected TestBot/1.0, got %q", body)
	}
}

func TestNewClient_DefaultUserAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Header.Get("User-Agent")))
	}))
	defer srv.Close()

	c := NewClient()
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if got, want := string(body), buildinfo.UserAgent(); got != want {
		t.Errorf("expected default UA %q, got %q", want, got)
	}
}

func TestNewClient_TruthfulUserAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Header.Get("User-Agent")))
	}))
	defer srv.Close()

	c := NewClient(WithTruthfulUserAgent(AgentSurfaceForge))
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if got, want := string(body), buildinfo.UserAgentFor(buildinfo.AgentSurfaceForge); got != want {
		t.Errorf("expected truthful forge UA %q, got %q", want, got)
	}
}

func TestNewClient_WithoutUserAgent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Without our roundtripper, Go sets its default UA
		w.Write([]byte(r.Header.Get("User-Agent")))
	}))
	defer srv.Close()

	c := NewClient(WithoutUserAgent())
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.HasPrefix(string(body), "Thane/") {
		t.Errorf("expected no Thane/ prefix with WithoutUserAgent, got %q", body)
	}
}

func TestNewClient_ExistingUserAgentNotOverwritten(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Header.Get("User-Agent")))
	}))
	defer srv.Close()

	c := NewClient()
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("User-Agent", "CustomBot/2.0")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "CustomBot/2.0" {
		t.Errorf("expected CustomBot/2.0, got %q", body)
	}
}

func TestNewTransport_HasTimeouts(t *testing.T) {
	tr := NewTransport()
	if tr.TLSHandshakeTimeout != DefaultTLSHandshakeTimeout {
		t.Errorf("TLSHandshakeTimeout: got %v, want %v", tr.TLSHandshakeTimeout, DefaultTLSHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout != DefaultResponseHeader {
		t.Errorf("ResponseHeaderTimeout: got %v, want %v", tr.ResponseHeaderTimeout, DefaultResponseHeader)
	}
	if tr.IdleConnTimeout != DefaultIdleConnTimeout {
		t.Errorf("IdleConnTimeout: got %v, want %v", tr.IdleConnTimeout, DefaultIdleConnTimeout)
	}
	if tr.MaxIdleConns != DefaultMaxIdleConns {
		t.Errorf("MaxIdleConns: got %d, want %d", tr.MaxIdleConns, DefaultMaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost != DefaultMaxIdleConnsPerHost {
		t.Errorf("MaxIdleConnsPerHost: got %d, want %d", tr.MaxIdleConnsPerHost, DefaultMaxIdleConnsPerHost)
	}
}

func TestDrainAndClose(t *testing.T) {
	rc := io.NopCloser(strings.NewReader("hello world"))
	DrainAndClose(rc, 1024)  // should not panic
	DrainAndClose(nil, 1024) // nil should not panic
}

func TestReadErrorBody(t *testing.T) {
	rc := io.NopCloser(strings.NewReader("error details here"))
	got := ReadErrorBody(rc, 512)
	if got != "error details here" {
		t.Errorf("expected error body, got %q", got)
	}
}

func TestReadErrorBody_Truncated(t *testing.T) {
	long := strings.Repeat("x", 1000)
	rc := io.NopCloser(strings.NewReader(long))
	got := ReadErrorBody(rc, 10)
	if len(got) != 10 {
		t.Errorf("expected 10 bytes, got %d", len(got))
	}
}

func TestReadErrorBody_Nil(t *testing.T) {
	got := ReadErrorBody(nil, 512)
	if got != "" {
		t.Errorf("expected empty string for nil, got %q", got)
	}
}

func TestNewClient_DisableKeepAlives(t *testing.T) {
	c := NewClient(WithDisableKeepAlives())
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestNewClient_WithTransport(t *testing.T) {
	custom := NewTransport()
	custom.MaxIdleConns = 99
	c := NewClient(WithTransport(custom))
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	// Verify it actually used our transport by making a request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestNewClient_TLSInsecureSkipVerify(t *testing.T) {
	// Create an HTTPS server with a self-signed cert
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("secure"))
	}))
	defer srv.Close()

	// Without skip-verify, request should fail
	strict := NewClient(WithTimeout(2 * time.Second))
	_, err := strict.Get(srv.URL)
	if err == nil {
		t.Fatal("expected TLS error with strict client")
	}

	// With skip-verify, request should succeed
	insecure := NewClient(
		WithTimeout(2*time.Second),
		WithTLSInsecureSkipVerify(),
	)
	resp, err := insecure.Get(srv.URL)
	if err != nil {
		t.Fatalf("expected success with insecure client, got: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "secure" {
		t.Errorf("expected 'secure', got %q", body)
	}
}

func TestReadErrorBody_Error(t *testing.T) {
	// Test with a reader that errors
	rc := io.NopCloser(&failReader{})
	got := ReadErrorBody(rc, 512)
	if !strings.Contains(got, "failed to read") {
		t.Errorf("expected failure message, got %q", got)
	}
}

type failReader struct{}

func (f *failReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("simulated read error")
}

func TestDrainAndClose_LimitsReading(t *testing.T) {
	// Verify DrainAndClose doesn't read more than limit
	data := strings.Repeat("x", 10000)
	rc := io.NopCloser(strings.NewReader(data))
	DrainAndClose(rc, 100) // should drain at most 100 bytes
}

// --- Retry transport tests ---

// failingRoundTripper simulates transient errors then succeeds.
type failingRoundTripper struct {
	failures int
	calls    int
}

func (f *failingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	f.calls++
	if f.calls <= f.failures {
		return nil, &net.OpError{
			Op:  "dial",
			Net: "tcp",
			Err: &net.OpError{Op: "connect", Err: syscall.EHOSTUNREACH},
		}
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader("ok")),
	}, nil
}

func TestRetryTransport_RetriesOnEHOSTUNREACH(t *testing.T) {
	ft := &failingRoundTripper{failures: 1}
	rt := &retryTransport{
		base:  ft,
		count: 2,
		delay: 10 * time.Millisecond,
	}

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ft.calls != 2 {
		t.Fatalf("expected 2 calls (1 fail + 1 success), got %d", ft.calls)
	}
}

func TestRetryTransport_NoRetryOnSuccess(t *testing.T) {
	ft := &failingRoundTripper{failures: 0}
	rt := &retryTransport{
		base:  ft,
		count: 2,
		delay: 10 * time.Millisecond,
	}

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ft.calls != 1 {
		t.Fatalf("expected 1 call, got %d", ft.calls)
	}
}

func TestRetryTransport_ExhaustsRetries(t *testing.T) {
	ft := &failingRoundTripper{failures: 10} // always fails
	rt := &retryTransport{
		base:  ft,
		count: 2,
		delay: 10 * time.Millisecond,
	}

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	_, err := rt.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	// 1 initial + 2 retries = 3
	if ft.calls != 3 {
		t.Fatalf("expected 3 calls, got %d", ft.calls)
	}
}

func TestRetryTransport_RespectsContextCancellation(t *testing.T) {
	ft := &failingRoundTripper{failures: 10}
	rt := &retryTransport{
		base:  ft,
		count: 5,
		delay: 5 * time.Second, // long delay
	}

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://example.com", nil)

	// Cancel during the retry delay
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := rt.RoundTrip(req)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	// Should have only made 1 call (initial failure), then cancelled during delay
	if ft.calls != 1 {
		t.Fatalf("expected 1 call before cancellation, got %d", ft.calls)
	}
}

// nonRetryableRoundTripper returns a non-retryable error.
type nonRetryableRoundTripper struct {
	calls int
}

func (f *nonRetryableRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls++
	return nil, fmt.Errorf("some non-retryable error")
}

func TestRetryTransport_NoRetryOnNonRetryableError(t *testing.T) {
	ft := &nonRetryableRoundTripper{}
	rt := &retryTransport{
		base:  ft,
		count: 2,
		delay: 10 * time.Millisecond,
	}

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	_, err := rt.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error")
	}
	if ft.calls != 1 {
		t.Fatalf("expected 1 call (no retry), got %d", ft.calls)
	}
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil", nil, false},
		{"generic", fmt.Errorf("oops"), false},
		{"EHOSTUNREACH", syscall.EHOSTUNREACH, true},
		{"ENETUNREACH", syscall.ENETUNREACH, true},
		{"ECONNREFUSED", syscall.ECONNREFUSED, true},
		{"ECONNRESET", syscall.ECONNRESET, false},
		{"wrapped EHOSTUNREACH", fmt.Errorf("connect: %w", syscall.EHOSTUNREACH), true},
		{"OpError wrapping EHOSTUNREACH", &net.OpError{
			Op: "dial", Net: "tcp",
			Err: &net.OpError{Op: "connect", Err: syscall.EHOSTUNREACH},
		}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableError(tt.err)
			if got != tt.expected {
				t.Errorf("isRetryableError(%v) = %v, want %v", tt.err, got, tt.expected)
			}
		})
	}
}

func TestRetryTransport_WithBody(t *testing.T) {
	ft := &failingRoundTripper{failures: 1}
	rt := &retryTransport{
		base:  ft,
		count: 2,
		delay: 10 * time.Millisecond,
	}

	body := strings.NewReader(`{"key":"value"}`)
	req, _ := http.NewRequest("POST", "http://example.com", body)
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(`{"key":"value"}`)), nil
	}

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected success after retry, got: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestRetryTransport_NoRetryWithoutGetBody(t *testing.T) {
	ft := &failingRoundTripper{failures: 1}
	rt := &retryTransport{
		base:  ft,
		count: 2,
		delay: 10 * time.Millisecond,
	}

	// POST with body but no GetBody — cannot safely retry.
	// Must nil out GetBody since http.NewRequest auto-sets it for some body types.
	body := strings.NewReader(`{"key":"value"}`)
	req, _ := http.NewRequest("POST", "http://example.com", body)
	req.GetBody = nil

	_, err := rt.RoundTrip(req)
	if err == nil {
		t.Fatal("expected error (should not retry without GetBody)")
	}
	if ft.calls != 1 {
		t.Fatalf("expected 1 call (no retry), got %d", ft.calls)
	}
}

// statusRoundTripper returns the given statuses in order across calls.
// After the list is exhausted it returns 200. Used to exercise
// status-based retry behavior.
type statusRoundTripper struct {
	statuses    []int
	calls       int
	retryAfter  string // if set, included on the first response
	bodyCloseCt *int   // if non-nil, increments on each body Close() call
}

func (s *statusRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	idx := s.calls
	s.calls++
	status := http.StatusOK
	if idx < len(s.statuses) {
		status = s.statuses[idx]
	}

	body := io.NopCloser(strings.NewReader("payload"))
	if s.bodyCloseCt != nil {
		body = &countingReadCloser{r: strings.NewReader("payload"), closed: s.bodyCloseCt}
	}

	resp := &http.Response{
		StatusCode: status,
		Body:       body,
		Header:     make(http.Header),
	}
	if idx == 0 && s.retryAfter != "" {
		resp.Header.Set("Retry-After", s.retryAfter)
	}
	return resp, nil
}

type countingReadCloser struct {
	r      io.Reader
	closed *int
}

func (c *countingReadCloser) Read(p []byte) (int, error) { return c.r.Read(p) }
func (c *countingReadCloser) Close() error {
	(*c.closed)++
	return nil
}

func TestRetryTransport_RetriesOn429ThenSucceeds(t *testing.T) {
	rt := &retryTransport{
		base:          &statusRoundTripper{statuses: []int{http.StatusTooManyRequests}},
		count:         2,
		delay:         10 * time.Millisecond,
		retryStatuses: map[int]bool{http.StatusTooManyRequests: true},
	}

	req, _ := http.NewRequest("POST", "http://example.com", strings.NewReader("body"))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("body")), nil }

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if calls := rt.base.(*statusRoundTripper).calls; calls != 2 {
		t.Errorf("expected 2 calls (1 throttle + 1 success), got %d", calls)
	}
}

func TestRetryTransport_RetriesOn5xxThenSucceeds(t *testing.T) {
	rt := &retryTransport{
		base: &statusRoundTripper{
			statuses: []int{http.StatusBadGateway, http.StatusServiceUnavailable},
		},
		count: 3,
		delay: 10 * time.Millisecond,
		retryStatuses: map[int]bool{
			http.StatusBadGateway:         true,
			http.StatusServiceUnavailable: true,
		},
	}

	req, _ := http.NewRequest("POST", "http://example.com", strings.NewReader("body"))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("body")), nil }

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestRetryTransport_DoesNotRetryUnlistedStatus(t *testing.T) {
	rt := &retryTransport{
		base:          &statusRoundTripper{statuses: []int{http.StatusBadRequest}},
		count:         2,
		delay:         10 * time.Millisecond,
		retryStatuses: map[int]bool{http.StatusTooManyRequests: true},
	}

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (no retry for non-listed status)", resp.StatusCode)
	}
	if calls := rt.base.(*statusRoundTripper).calls; calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestRetryTransport_StatusRetryClosesPriorBody(t *testing.T) {
	closes := 0
	rt := &retryTransport{
		base: &statusRoundTripper{
			statuses:    []int{http.StatusTooManyRequests},
			bodyCloseCt: &closes,
		},
		count:         2,
		delay:         1 * time.Millisecond,
		retryStatuses: map[int]bool{http.StatusTooManyRequests: true},
	}

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if closes < 1 {
		t.Errorf("prior response body was never closed (close count = %d)", closes)
	}
}

func TestRetryTransport_HonorsRetryAfterSeconds(t *testing.T) {
	rt := &retryTransport{
		base: &statusRoundTripper{
			statuses:   []int{http.StatusTooManyRequests},
			retryAfter: "0", // zero-second Retry-After, keeps the test fast
		},
		count:         2,
		delay:         1 * time.Hour, // would make the test hang if not overridden
		retryStatuses: map[int]bool{http.StatusTooManyRequests: true},
	}

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	done := make(chan struct{})
	go func() {
		_, _ = rt.RoundTrip(req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RoundTrip did not honor Retry-After: 0 (waited on configured 1h delay instead)")
	}
}
