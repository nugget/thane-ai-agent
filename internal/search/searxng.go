package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nugget/thane-ai-agent/internal/httpkit"
)

// SearXNG implements the Provider interface for a SearXNG instance.
type SearXNG struct {
	baseURL    string
	httpClient *http.Client
}

// NewSearXNG creates a SearXNG provider. The baseURL should be the root
// URL of the SearXNG instance (e.g., "http://localhost:8080").
func NewSearXNG(baseURL string) *SearXNG {
	return &SearXNG{
		baseURL: baseURL,
		httpClient: httpkit.NewClient(
			httpkit.WithTimeout(15 * time.Second),
		),
	}
}

func (s *SearXNG) Name() string { return "searxng" }

// searxngResponse is the JSON response from SearXNG's /search endpoint.
type searxngResponse struct {
	Results []searxngResult `json:"results"`
}

type searxngResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

func (s *SearXNG) Search(ctx context.Context, query string, opts Options) ([]Result, error) {
	params := url.Values{
		"q":      {query},
		"format": {"json"},
	}

	if opts.Language != "" {
		params.Set("language", opts.Language)
	}

	count := opts.Count
	if count == 0 {
		count = 5
	}

	reqURL := fmt.Sprintf("%s/search?%s", s.baseURL, params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("searxng: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body := httpkit.ReadErrorBody(resp.Body, 512)
		return nil, fmt.Errorf("searxng: HTTP %d: %s", resp.StatusCode, body)
	}

	var sr searxngResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("searxng: decode response: %w", err)
	}

	results := make([]Result, 0, count)
	for i, r := range sr.Results {
		if i >= count {
			break
		}
		results = append(results, Result{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Content,
		})
	}

	return results, nil
}

// SearXNGConfig holds configuration for the SearXNG provider.
type SearXNGConfig struct {
	URL string `yaml:"url"`
}

// Configured reports whether a SearXNG URL is set.
func (c SearXNGConfig) Configured() bool {
	return c.URL != ""
}

// FormatResults builds a human-readable result string.
func FormatResults(results []Result, count int) string {
	if len(results) == 0 {
		return "No results found."
	}

	var buf []byte
	for i, r := range results {
		if i > 0 {
			buf = append(buf, '\n', '\n')
		}
		buf = append(buf, strconv.Itoa(i+1)...)
		buf = append(buf, ". "...)
		buf = append(buf, r.Title...)
		buf = append(buf, '\n')
		buf = append(buf, "   "...)
		buf = append(buf, r.URL...)
		if r.Snippet != "" {
			buf = append(buf, '\n')
			buf = append(buf, "   "...)
			buf = append(buf, r.Snippet...)
		}
	}
	return string(buf)
}
