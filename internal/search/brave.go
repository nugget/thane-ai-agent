package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Brave implements the Provider interface for the Brave Search API.
type Brave struct {
	apiKey     string
	httpClient *http.Client
}

// NewBrave creates a Brave Search provider.
func NewBrave(apiKey string) *Brave {
	return &Brave{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (b *Brave) Name() string { return "brave" }

// braveResponse is the JSON response from Brave's web search API.
type braveResponse struct {
	Web struct {
		Results []braveResult `json:"results"`
	} `json:"web"`
}

type braveResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

func (b *Brave) Search(ctx context.Context, query string, opts Options) ([]Result, error) {
	count := opts.Count
	if count == 0 {
		count = 5
	}

	params := url.Values{
		"q":     {query},
		"count": {strconv.Itoa(count)},
	}

	if opts.Language != "" {
		params.Set("search_lang", opts.Language)
	}

	reqURL := "https://api.search.brave.com/res/v1/web/search?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("brave: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Subscription-Token", b.apiKey)

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("brave: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var br braveResponse
	if err := json.NewDecoder(resp.Body).Decode(&br); err != nil {
		return nil, fmt.Errorf("brave: decode response: %w", err)
	}

	results := make([]Result, 0, len(br.Web.Results))
	for _, r := range br.Web.Results {
		results = append(results, Result{
			Title:   r.Title,
			URL:     r.URL,
			Snippet: r.Description,
		})
	}

	return results, nil
}

// BraveConfig holds configuration for the Brave Search provider.
type BraveConfig struct {
	APIKey string `yaml:"api_key"`
}

// Configured reports whether a Brave API key is set.
func (c BraveConfig) Configured() bool {
	return c.APIKey != ""
}
