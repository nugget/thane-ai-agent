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
// Account is the server-assigned identity resolved from the token at auth time.
type Provider struct {
	ID          string
	Account     string
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
	Account     string    `json:"account"`
	ClientName  string    `json:"client_name"`
	ClientID    string    `json:"client_id"`
	ConnectedAt time.Time `json:"connected_at"`
}

// Registry tracks connected platform providers. It maintains a primary
// index by provider ID and a secondary index by account name for
// dispatching requests to the right identity.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]*Provider   // provider ID → Provider
	byAccount map[string][]*Provider // account name → Providers
	logger    *slog.Logger
}

// NewRegistry creates a new provider registry.
func NewRegistry(logger *slog.Logger) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	return &Registry{
		providers: make(map[string]*Provider),
		byAccount: make(map[string][]*Provider),
		logger:    logger,
	}
}

// Add registers a provider in the registry.
func (r *Registry) Add(p *Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.ID] = p
	r.byAccount[p.Account] = append(r.byAccount[p.Account], p)
	r.logger.Info("platform provider connected",
		"provider_id", p.ID,
		"account", p.Account,
		"client_name", p.ClientName,
		"client_id", p.ClientID,
	)
}

// Remove unregisters a provider from the registry.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.providers[id]
	if !ok {
		return
	}
	delete(r.providers, id)

	// Remove from account index.
	acct := r.byAccount[p.Account]
	for i, pp := range acct {
		if pp.ID == id {
			r.byAccount[p.Account] = append(acct[:i], acct[i+1:]...)
			break
		}
	}
	if len(r.byAccount[p.Account]) == 0 {
		delete(r.byAccount, p.Account)
	}

	r.logger.Info("platform provider disconnected",
		"provider_id", p.ID,
		"account", p.Account,
		"client_name", p.ClientName,
		"client_id", p.ClientID,
	)
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
		infos = append(infos, providerToInfo(p))
	}
	return infos
}

// ByAccount returns snapshots of all providers connected under the given
// account name. Returns nil if no providers are connected for that account.
func (r *Registry) ByAccount(account string) []ProviderInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	providers := r.byAccount[account]
	if len(providers) == 0 {
		return nil
	}
	infos := make([]ProviderInfo, len(providers))
	for i, p := range providers {
		infos[i] = providerToInfo(p)
	}
	return infos
}

// Accounts returns the names of all accounts that have at least one
// connected provider.
func (r *Registry) Accounts() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	accounts := make([]string, 0, len(r.byAccount))
	for a := range r.byAccount {
		accounts = append(accounts, a)
	}
	return accounts
}

func providerToInfo(p *Provider) ProviderInfo {
	return ProviderInfo{
		ID:          p.ID,
		Account:     p.Account,
		ClientName:  p.ClientName,
		ClientID:    p.ClientID,
		ConnectedAt: p.ConnectedAt,
	}
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
