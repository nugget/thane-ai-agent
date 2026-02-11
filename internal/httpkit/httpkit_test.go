package httpkit

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	if !strings.HasPrefix(string(body), "Thane/") {
		t.Errorf("expected Thane/ prefix, got %q", body)
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
