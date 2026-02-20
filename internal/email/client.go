package email

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Client is a single-account IMAP client that wraps go-imap/v2 with
// automatic reconnection and mutex-serialized access. All public
// methods are goroutine-safe.
type Client struct {
	cfg    IMAPConfig
	logger *slog.Logger

	mu     sync.Mutex
	client *imapclient.Client
}

// NewClient creates an IMAP client for the given account configuration.
// The connection is established lazily on first use.
func NewClient(cfg IMAPConfig, logger *slog.Logger) *Client {
	return &Client{
		cfg:    cfg,
		logger: logger,
	}
}

// Connect establishes the IMAP connection and authenticates. It is
// called automatically by ensureConnected but can be called explicitly
// for eager initialization.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connectLocked(ctx)
}

// connectLocked performs the actual connection. Caller must hold c.mu.
func (c *Client) connectLocked(ctx context.Context) error {
	// Close any existing stale connection.
	if c.client != nil {
		_ = c.client.Close()
		c.client = nil
	}

	addr := net.JoinHostPort(c.cfg.Host, fmt.Sprintf("%d", c.cfg.Port))

	var opts imapclient.Options
	if c.cfg.TLS {
		opts.TLSConfig = &tls.Config{
			ServerName: c.cfg.Host,
		}
	}

	c.logger.Debug("connecting to IMAP server", "host", c.cfg.Host, "port", c.cfg.Port, "tls", c.cfg.TLS)

	var client *imapclient.Client
	var err error
	if c.cfg.TLS {
		client, err = imapclient.DialTLS(addr, &opts)
	} else {
		client, err = imapclient.DialInsecure(addr, &opts)
	}
	if err != nil {
		return fmt.Errorf("dial IMAP %s: %w", addr, err)
	}

	loginCmd := client.Login(c.cfg.Username, c.cfg.Password)
	if err := loginCmd.Wait(); err != nil {
		_ = client.Close()
		return fmt.Errorf("login as %s: %w", c.cfg.Username, err)
	}

	c.client = client
	c.logger.Info("IMAP connected", "host", c.cfg.Host, "user", c.cfg.Username)
	return nil
}

// ensureConnected checks the connection and reconnects if needed.
// Caller must hold c.mu.
func (c *Client) ensureConnected(ctx context.Context) error {
	if c.client != nil {
		// Quick liveness check via NOOP.
		if err := c.client.Noop().Wait(); err == nil {
			return nil
		}
		c.logger.Debug("IMAP connection stale, reconnecting", "host", c.cfg.Host)
	}
	return c.connectLocked(ctx)
}

// Ping checks that the IMAP connection is alive. Used by connwatch
// for health monitoring.
func (c *Client) Ping(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ensureConnected(ctx)
}

// Close logs out and closes the IMAP connection.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return nil
	}

	err := c.client.Close()
	c.client = nil
	return err
}

// selectFolder selects a mailbox. Caller must hold c.mu.
func (c *Client) selectFolder(folder string) (*imap.SelectData, error) {
	if folder == "" {
		folder = "INBOX"
	}
	cmd := c.client.Select(folder, nil)
	data, err := cmd.Wait()
	if err != nil {
		return nil, fmt.Errorf("select %s: %w", folder, err)
	}
	return data, nil
}
