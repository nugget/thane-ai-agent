package llm

import (
	"context"
	"fmt"
	"sync"
)

// DynamicClient is a concurrency-safe wrapper around a swappable
// underlying llm.Client. In-flight requests continue using the client
// they started with while future requests see the new client after
// Swap.
type DynamicClient struct {
	mu      sync.RWMutex
	current Client
}

// NewDynamicClient wraps the initial client.
func NewDynamicClient(initial Client) *DynamicClient {
	return &DynamicClient{current: initial}
}

// Swap replaces the underlying client used for future requests.
func (c *DynamicClient) Swap(next Client) error {
	if next == nil {
		return fmt.Errorf("nil llm client")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = next
	return nil
}

func (c *DynamicClient) currentClient() (Client, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.current == nil {
		return nil, fmt.Errorf("no llm client configured")
	}
	return c.current, nil
}

// Chat delegates to the current client.
func (c *DynamicClient) Chat(ctx context.Context, model string, messages []Message, tools []map[string]any) (*ChatResponse, error) {
	client, err := c.currentClient()
	if err != nil {
		return nil, err
	}
	return client.Chat(ctx, model, messages, tools)
}

// ChatStream delegates to the current client.
func (c *DynamicClient) ChatStream(ctx context.Context, model string, messages []Message, tools []map[string]any, callback StreamCallback) (*ChatResponse, error) {
	client, err := c.currentClient()
	if err != nil {
		return nil, err
	}
	return client.ChatStream(ctx, model, messages, tools, callback)
}

// Ping delegates to the current client.
func (c *DynamicClient) Ping(ctx context.Context) error {
	client, err := c.currentClient()
	if err != nil {
		return err
	}
	return client.Ping(ctx)
}
