// Package fetch provides web page fetching and content extraction.
// It downloads a URL's HTML and extracts readable text content,
// stripping navigation, ads, and other boilerplate.
package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/nugget/thane-ai-agent/internal/httpkit"
)

// DefaultTimeout is the HTTP request timeout for fetching pages.
const DefaultTimeout = 30 * 1e9 // 30s in nanoseconds, same as time.Duration

// DefaultMaxBytes is the maximum response body size (5 MB).
const DefaultMaxBytes int64 = 5 * 1024 * 1024

// DefaultMaxChars is the default character limit for extracted text.
const DefaultMaxChars = 50000

// Result holds the fetched and extracted content from a URL.
type Result struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Content     string `json:"content"`
	ContentType string `json:"content_type,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	Length      int    `json:"length"`
	StatusCode  int    `json:"status_code"`
}

// Fetcher downloads and extracts readable content from web pages.
type Fetcher struct {
	client   *http.Client
	maxBytes int64
}

// New creates a Fetcher with default settings.
func New() *Fetcher {
	return &Fetcher{
		client: httpkit.NewClient(
			httpkit.WithTimeout(DefaultTimeout),
		),
		maxBytes: DefaultMaxBytes,
	}
}

// Fetch downloads the URL and extracts readable text content.
// maxChars limits the output length; 0 uses DefaultMaxChars.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, maxChars int) (*Result, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("web_fetch: url is required")
	}

	// Normalize URL
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}

	if maxChars <= 0 {
		maxChars = DefaultMaxChars
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("web_fetch: invalid url: %w", err)
	}

	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8,*/*;q=0.7")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web_fetch: request failed: %w", err)
	}
	defer resp.Body.Close()

	// Limit body size
	limited := io.LimitReader(resp.Body, f.maxBytes)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("web_fetch: failed to read response: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")

	// Extract readable text based on content type
	var title, content string
	if isHTML(contentType) {
		title, content = extractHTML(string(body))
	} else if isPlainText(contentType) {
		content = string(body)
	} else {
		// For other types, try to use as text if it's valid UTF-8
		if utf8.Valid(body) {
			content = string(body)
		} else {
			return &Result{
				URL:         rawURL,
				ContentType: contentType,
				StatusCode:  resp.StatusCode,
				Content:     fmt.Sprintf("Binary content (%s), %d bytes", contentType, len(body)),
				Length:      len(body),
			}, nil
		}
	}

	// Truncate if needed
	truncated := false
	if len(content) > maxChars {
		content = truncateUTF8(content, maxChars)
		truncated = true
	}

	return &Result{
		URL:         rawURL,
		Title:       title,
		Content:     content,
		ContentType: contentType,
		Truncated:   truncated,
		Length:      len(content),
		StatusCode:  resp.StatusCode,
	}, nil
}

func isHTML(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

func isPlainText(ct string) bool {
	return strings.Contains(strings.ToLower(ct), "text/plain")
}

// truncateUTF8 truncates a string to maxChars, ensuring it doesn't
// break in the middle of a multi-byte character.
func truncateUTF8(s string, maxChars int) string {
	if len(s) <= maxChars {
		return s
	}
	// Walk runes to find safe cut point
	count := 0
	for i := range s {
		if count >= maxChars {
			return s[:i]
		}
		count++
	}
	return s
}
