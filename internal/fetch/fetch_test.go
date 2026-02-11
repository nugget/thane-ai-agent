package fetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractHTML(t *testing.T) {
	html := `<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
<nav>Navigation stuff</nav>
<script>var x = 1;</script>
<style>.foo { color: red; }</style>
<main>
<h1>Hello World</h1>
<p>This is a test paragraph with <strong>bold text</strong>.</p>
<p>Second paragraph.</p>
</main>
<footer>Footer stuff</footer>
</body>
</html>`

	title, content := extractHTML(html)

	if title != "Test Page" {
		t.Errorf("expected title 'Test Page', got %q", title)
	}
	if !strings.Contains(content, "Hello World") {
		t.Errorf("expected content to contain 'Hello World', got %q", content)
	}
	if !strings.Contains(content, "bold text") {
		t.Errorf("expected content to contain 'bold text', got %q", content)
	}
	if strings.Contains(content, "var x = 1") {
		t.Error("content should not contain script text")
	}
	if strings.Contains(content, "Navigation stuff") {
		t.Error("content should not contain nav text")
	}
	if strings.Contains(content, "Footer stuff") {
		t.Error("content should not contain footer text")
	}
}

func TestFetch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify User-Agent is set
		ua := r.Header.Get("User-Agent")
		if !strings.HasPrefix(ua, "Thane/") {
			t.Errorf("expected Thane User-Agent, got %q", ua)
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>Test</title></head><body><p>Hello from test server</p></body></html>`))
	}))
	defer ts.Close()

	f := New()
	result, err := f.Fetch(context.Background(), ts.URL, 0)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	if result.Title != "Test" {
		t.Errorf("expected title 'Test', got %q", result.Title)
	}
	if !strings.Contains(result.Content, "Hello from test server") {
		t.Errorf("expected content to contain 'Hello from test server', got %q", result.Content)
	}
	if result.StatusCode != 200 {
		t.Errorf("expected status 200, got %d", result.StatusCode)
	}
}

func TestFetchPlainText(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("Just plain text content"))
	}))
	defer ts.Close()

	f := New()
	result, err := f.Fetch(context.Background(), ts.URL, 0)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	if result.Content != "Just plain text content" {
		t.Errorf("expected plain text content, got %q", result.Content)
	}
}

func TestFetchTruncation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(strings.Repeat("x", 1000)))
	}))
	defer ts.Close()

	f := New()
	result, err := f.Fetch(context.Background(), ts.URL, 100)
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	if !result.Truncated {
		t.Error("expected truncated=true")
	}
	if result.Length > 100 {
		t.Errorf("expected length <= 100, got %d", result.Length)
	}
}

func TestFetchURLNormalization(t *testing.T) {
	f := New()
	_, err := f.Fetch(context.Background(), "", 0)
	if err == nil {
		t.Error("expected error for empty URL")
	}
}

func TestCleanWhitespace(t *testing.T) {
	input := "  Hello   world  \n\n\n\n  Second line  \n\n\n Third  "
	got := cleanWhitespace(input)
	if strings.Contains(got, "\n\n\n") {
		t.Errorf("should not have triple newlines: %q", got)
	}
}

func TestTruncateUTF8(t *testing.T) {
	// Ensure we don't break multi-byte characters
	s := "Héllo wörld café"
	truncated := truncateUTF8(s, 5)
	if len([]rune(truncated)) > 5 {
		t.Errorf("expected at most 5 runes, got %d: %q", len([]rune(truncated)), truncated)
	}
}

func TestToolHandler(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>Tool Test</title></head><body><p>Content here</p></body></html>`))
	}))
	defer ts.Close()

	f := New()
	handler := ToolHandler(f)

	result, err := handler(context.Background(), map[string]any{"url": ts.URL})
	if err != nil {
		t.Fatalf("handler failed: %v", err)
	}
	if !strings.Contains(result, "Content here") {
		t.Errorf("expected result to contain 'Content here', got %q", result)
	}
}

func TestToolHandlerMissingURL(t *testing.T) {
	f := New()
	handler := ToolHandler(f)

	_, err := handler(context.Background(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing URL")
	}
}
