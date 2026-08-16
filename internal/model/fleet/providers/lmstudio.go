package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/platform/httpkit"
)

// LMStudioClient is an [OpenAICompatClient] plus the two things LM
// Studio adds beyond the OpenAI protocol: explicit model load/unload,
// and the richer native model inventory that reports per-model context
// lengths. The chat path is not LM Studio's own — it is the shared
// OpenAI-compatible implementation, which is why a vLLM or llama-server
// endpoint gets the same streaming, tool-call, usage, and error handling
// this client has been proving out in production.
type LMStudioClient struct {
	*OpenAICompatClient
}

// NewLMStudioClient creates a new LM Studio client.
func NewLMStudioClient(baseURL, apiKey string, logger *slog.Logger) *LMStudioClient {
	return NewLMStudioClientWithTTL(baseURL, apiKey, "", logger, 0)
}

// NewLMStudioClientWithTTL creates an LM Studio client bound to a named
// resource, with a resource-level idle TTL hint for inference requests.
// resource may be empty for ad-hoc clients that no configured endpoint
// backs; it only names the endpoint in log lines.
func NewLMStudioClientWithTTL(baseURL, apiKey, resource string, logger *slog.Logger, idleTTLSeconds int) *LMStudioClient {
	return &LMStudioClient{
		OpenAICompatClient: NewOpenAICompatClient(baseURL, apiKey, "lmstudio", resource, logger, idleTTLSeconds),
	}
}

// LoadModel asks LM Studio to load or reload a model with the requested
// inference context length.
func (c *LMStudioClient) LoadModel(ctx context.Context, model string, contextLength int) (*LMStudioLoadResponse, error) {
	reqBody := lmStudioLoadRequest{
		Model:          strings.TrimSpace(model),
		ContextLength:  contextLength,
		EchoLoadConfig: true,
	}
	if reqBody.Model == "" {
		return nil, fmt.Errorf("model is required")
	}
	if reqBody.ContextLength < 0 {
		reqBody.ContextLength = 0
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v1/models/load", bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.setAuth(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody := httpkit.ReadErrorBody(resp.Body, 4096)
		c.logger.Error("load model API error", "status", resp.StatusCode, "body", errBody, "model", reqBody.Model, "context_length", reqBody.ContextLength)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, errBody)
	}

	var result LMStudioLoadResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	c.logger.Info("model loaded",
		"model", reqBody.Model,
		"context_length", reqBody.ContextLength,
		"status", result.Status,
		"instance_id", result.InstanceID,
	)
	return &result, nil
}

// UnloadModel asks LM Studio to release a loaded model instance.
//
// LM Studio has no reconfigure-in-place operation: every load starts a new
// instance, so growing a model's context window means holding two copies of
// its weights unless the resident one is released first. A host without room
// for the second copy refuses the load outright — and refuses a smaller
// window as readily as a larger one, because what does not fit is the
// weights, not the window.
func (c *LMStudioClient) UnloadModel(ctx context.Context, instanceID string) error {
	reqBody := lmStudioUnloadRequest{InstanceID: strings.TrimSpace(instanceID)}
	if reqBody.InstanceID == "" {
		return fmt.Errorf("instance id is required")
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/v1/models/unload", bytes.NewReader(jsonData))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.setAuth(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	// The success body is not read for its content, but it still has to be
	// drained for the connection to return to this shared client's pool.
	defer httpkit.DrainAndClose(resp.Body, 4096)

	if resp.StatusCode != http.StatusOK {
		errBody := httpkit.ReadErrorBody(resp.Body, 4096)
		c.logger.Error("unload model API error", "status", resp.StatusCode, "body", errBody, "instance_id", reqBody.InstanceID)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, errBody)
	}

	c.logger.Info("model unloaded", "instance_id", reqBody.InstanceID)
	return nil
}
