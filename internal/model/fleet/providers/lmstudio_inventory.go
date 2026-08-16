package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/httpkit"
)

// Ping checks whether the endpoint is reachable, using the one route
// every OpenAI-compatible server is required to expose.
func (c *OpenAICompatClient) Ping(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/v1/models", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.setAuth(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	// Drain, not just close: an unread body keeps the connection out of
	// the pool, and connwatch probes this endpoint every minute for the
	// life of the process.
	defer httpkit.DrainAndClose(resp.Body, 4096)

	if resp.StatusCode != http.StatusOK {
		errBody := httpkit.ReadErrorBody(resp.Body, 4096)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, errBody)
	}
	return nil
}

// ListModelInfos returns available LM Studio model names.
func (c *LMStudioClient) ListModelInfos(ctx context.Context) ([]LMStudioModelInfo, error) {
	models, err := c.listModelInfosV1(ctx)
	if err == nil {
		return models, nil
	}
	var endpointErr *openAICompatEndpointError
	if !errors.As(err, &endpointErr) || !endpointErr.FallbackOK {
		return nil, err
	}
	models, err = c.listModelInfosV0(ctx)
	if err == nil {
		return models, nil
	}
	if !errors.As(err, &endpointErr) || !endpointErr.FallbackOK {
		return nil, err
	}
	return c.listModelInfosOpenAI(ctx)
}

type openAICompatEndpointError struct {
	Status     int
	Body       string
	FallbackOK bool
}

func (e *openAICompatEndpointError) Error() string {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("API error %d", e.Status)
	}
	return fmt.Sprintf("API error %d: %s", e.Status, body)
}

func (c *LMStudioClient) listModelInfosV1(ctx context.Context) ([]LMStudioModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setAuth(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody := httpkit.ReadErrorBody(resp.Body, 4096)
		return nil, &openAICompatEndpointError{
			Status:     resp.StatusCode,
			Body:       errBody,
			FallbackOK: lmStudioSupportsModelFallback(resp.StatusCode, errBody),
		}
	}

	var result openAICompatV1ModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	models := make([]LMStudioModelInfo, 0, len(result.Models))
	for _, model := range result.Models {
		models = append(models, model.toModelInfo())
	}
	return models, nil
}

func (c *LMStudioClient) listModelInfosV0(ctx context.Context) ([]LMStudioModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/v0/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setAuth(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody := httpkit.ReadErrorBody(resp.Body, 4096)
		return nil, &openAICompatEndpointError{
			Status:     resp.StatusCode,
			Body:       errBody,
			FallbackOK: lmStudioSupportsModelFallback(resp.StatusCode, errBody),
		}
	}

	var result openAICompatModelsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxModelListBytes)).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Data) > maxModelListEntries {
		// Reported, never silently clipped: a listing this long means
		// the endpoint is not what the operator thinks it is, and a
		// quietly truncated inventory would route against a fiction.
		return nil, fmt.Errorf("model listing has %d entries, more than the %d this client accepts", len(result.Data), maxModelListEntries)
	}
	return result.Data, nil
}

// ListModelInfos returns the models the endpoint advertises on
// /v1/models. The OpenAI schema carries only an id, so most fields come
// back zero; servers that extend it (vLLM's max_model_len, LM Studio's
// native inventory) fill in more. A zero context length here means "the
// server did not say", not "no context" — the caller falls back to
// configuration rather than inventing a window.
func (c *OpenAICompatClient) ListModelInfos(ctx context.Context) ([]OpenAICompatModelInfo, error) {
	return c.listModelInfosOpenAI(ctx)
}

func (c *OpenAICompatClient) listModelInfosOpenAI(ctx context.Context) ([]OpenAICompatModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setAuth(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody := httpkit.ReadErrorBody(resp.Body, 4096)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, errBody)
	}

	var result openAICompatModelsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxModelListBytes)).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Data) > maxModelListEntries {
		// Reported, never silently clipped: a listing this long means
		// the endpoint is not what the operator thinks it is, and a
		// quietly truncated inventory would route against a fiction.
		return nil, fmt.Errorf("model listing has %d entries, more than the %d this client accepts", len(result.Data), maxModelListEntries)
	}
	return result.Data, nil
}

func lmStudioSupportsModelFallback(status int, body string) bool {
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented {
		return true
	}
	body = strings.ToLower(strings.TrimSpace(body))
	return strings.Contains(body, "unexpected endpoint or method")
}

const (
	// maxModelListBytes bounds a /v1/models body. Real listings are a
	// few KB; anything near this is a misconfigured or hostile endpoint,
	// and this host has been OOM-killed once already.
	maxModelListBytes = 4 << 20
	// maxModelListEntries bounds the model count for the same reason.
	maxModelListEntries = 2048
)
