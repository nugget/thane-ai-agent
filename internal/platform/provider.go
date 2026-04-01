package platform

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Provider represents a connected platform provider (e.g. a macOS app instance).
type Provider struct {
	ID          string
	ClientName  string
	ClientID    string
	Conn        *websocket.Conn
	ConnectedAt time.Time

	// done is closed when the provider's connection is terminated.
	done chan struct{}
}

// ProviderInfo is a safe-to-export snapshot of a connected provider,
// without the WebSocket connection pointer.
type ProviderInfo struct {
	ID          string    `json:"id"`
	ClientName  string    `json:"client_name"`
	ClientID    string    `json:"client_id"`
	ConnectedAt time.Time `json:"connected_at"`
}

// Registry tracks connected platform providers.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]*Provider
	logger    *slog.Logger
}

// NewRegistry creates a new provider registry.
func NewRegistry(logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{
		providers: make(map[string]*Provider),
		logger:    logger,
	}
}

// Add registers a provider in the registry.
func (r *Registry) Add(p *Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.ID] = p
	r.logger.Info("platform provider connected",
		"provider_id", p.ID,
		"client_name", p.ClientName,
		"client_id", p.ClientID,
	)
}

// Remove unregisters a provider from the registry.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.providers[id]; ok {
		delete(r.providers, id)
		r.logger.Info("platform provider disconnected",
			"provider_id", p.ID,
			"client_name", p.ClientName,
			"client_id", p.ClientID,
		)
	}
}

// Count returns the number of connected providers.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.providers)
}

// List returns a snapshot of all connected providers.
func (r *Registry) List() []ProviderInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	infos := make([]ProviderInfo, 0, len(r.providers))
	for _, p := range r.providers {
		infos = append(infos, ProviderInfo{
			ID:          p.ID,
			ClientName:  p.ClientName,
			ClientID:    p.ClientID,
			ConnectedAt: p.ConnectedAt,
		})
	}
	return infos
}

// generateProviderID creates a provider ID with the prov_ prefix and a
// random hex suffix.
func generateProviderID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		// Fallback: extremely unlikely, but don't panic.
		return "prov_000000"
	}
	return "prov_" + hex.EncodeToString(b)
}
