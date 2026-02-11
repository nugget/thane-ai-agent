// Package embeddings provides vector embedding generation via Ollama.
package embeddings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/nugget/thane-ai-agent/internal/httpkit"
)

// Client generates embeddings using Ollama's embedding API.
type Client struct {
	baseURL string
	model   string
	client  *http.Client
}

// Config for embedding client.
type Config struct {
	BaseURL string // Ollama base URL (e.g., "http://localhost:11434")
	Model   string // Embedding model (e.g., "nomic-embed-text")
}

// New creates an embedding client.
func New(cfg Config) *Client {
	if cfg.Model == "" {
		cfg.Model = "nomic-embed-text"
	}
	return &Client{
		baseURL: cfg.BaseURL,
		model:   cfg.Model,
		client: httpkit.NewClient(
			httpkit.WithTimeout(30 * time.Second),
		),
	}
}

// embedRequest is the Ollama embedding API request.
type embedRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

// embedResponse is the Ollama embedding API response.
type embedResponse struct {
	Embedding []float32 `json:"embedding"`
}

// Generate creates an embedding for the given text.
func (c *Client) Generate(ctx context.Context, text string) ([]float32, error) {
	req := embedRequest{
		Model:  c.model,
		Prompt: text,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody := httpkit.ReadErrorBody(resp.Body, 512)
		return nil, fmt.Errorf("ollama returned status %d: %s", resp.StatusCode, errBody)
	}

	var embedResp embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&embedResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return embedResp.Embedding, nil
}

// GenerateBatch creates embeddings for multiple texts.
func (c *Client) GenerateBatch(ctx context.Context, texts []string) ([][]float32, error) {
	results := make([][]float32, len(texts))
	for i, text := range texts {
		emb, err := c.Generate(ctx, text)
		if err != nil {
			return nil, fmt.Errorf("embed text %d: %w", i, err)
		}
		results[i] = emb
	}
	return results, nil
}

// CosineSimilarity computes cosine similarity between two vectors.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}

	var dotProduct, normA, normB float32
	for i := range a {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}

// TopK returns indices of top k most similar vectors to query.
func TopK(query []float32, vectors [][]float32, k int) []int {
	type scored struct {
		idx   int
		score float32
	}

	scores := make([]scored, len(vectors))
	for i, v := range vectors {
		scores[i] = scored{idx: i, score: CosineSimilarity(query, v)}
	}

	// Simple selection sort for top k (fine for small k)
	for i := 0; i < k && i < len(scores); i++ {
		maxIdx := i
		for j := i + 1; j < len(scores); j++ {
			if scores[j].score > scores[maxIdx].score {
				maxIdx = j
			}
		}
		scores[i], scores[maxIdx] = scores[maxIdx], scores[i]
	}

	result := make([]int, 0, k)
	for i := 0; i < k && i < len(scores); i++ {
		result = append(result, scores[i].idx)
	}
	return result
}
