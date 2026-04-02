package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var errProviderDisconnected = errors.New("platform provider disconnected")

type capabilityState struct {
	version string
	methods map[string]bool
}

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

	writeMu sync.Mutex

	stateMu         sync.RWMutex
	capabilities    []Capability
	capabilityIndex map[string]capabilityState
	pending         map[int64]chan Message
	nextRequestID   atomic.Int64
}

// ProviderInfo is a safe-to-export snapshot of a connected provider,
// without the WebSocket connection pointer.
type ProviderInfo struct {
	ID           string       `json:"id"`
	Account      string       `json:"account"`
	ClientName   string       `json:"client_name"`
	ClientID     string       `json:"client_id"`
	ConnectedAt  time.Time    `json:"connected_at"`
	Capabilities []Capability `json:"capabilities,omitempty"`
}

// CallRequest describes a routed request to a connected platform provider.
type CallRequest struct {
	Account    string
	ClientID   string
	Capability string
	Method     string
	Params     json.RawMessage
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
	p.stateMu.Lock()
	if p.capabilityIndex == nil {
		p.capabilityIndex = make(map[string]capabilityState)
	}
	if p.pending == nil {
		p.pending = make(map[int64]chan Message)
	}
	p.stateMu.Unlock()

	r.mu.Lock()
	r.providers[p.ID] = p
	r.byAccount[p.Account] = append(r.byAccount[p.Account], p)
	r.mu.Unlock()

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
	p, ok := r.providers[id]
	if !ok {
		r.mu.Unlock()
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
	r.mu.Unlock()

	p.failPending(Message{
		Type: typeResult,
		Error: &Error{
			Code:    "provider_disconnected",
			Message: errProviderDisconnected.Error(),
		},
	})

	r.logger.Info("platform provider disconnected",
		"provider_id", p.ID,
		"account", p.Account,
		"client_name", p.ClientName,
		"client_id", p.ClientID,
	)
}

// RegisterCapabilities replaces the capability set registered for a provider.
func (r *Registry) RegisterCapabilities(providerID string, capabilities []Capability) error {
	r.mu.RLock()
	p := r.providers[providerID]
	r.mu.RUnlock()
	if p == nil {
		return fmt.Errorf("provider %q not found", providerID)
	}
	p.setCapabilities(capabilities)
	return nil
}

// ResolveResult completes a pending call for the given provider.
func (r *Registry) ResolveResult(providerID string, msg Message) bool {
	r.mu.RLock()
	p := r.providers[providerID]
	r.mu.RUnlock()
	if p == nil {
		return false
	}
	return p.resolvePending(msg)
}

// Call dispatches a request to a connected provider and waits for the
// corresponding result or context cancellation.
func (r *Registry) Call(ctx context.Context, req CallRequest) (json.RawMessage, error) {
	if strings.TrimSpace(req.Capability) == "" {
		return nil, fmt.Errorf("capability is required")
	}
	if strings.TrimSpace(req.Method) == "" {
		return nil, fmt.Errorf("method is required")
	}

	provider, err := r.selectProvider(req)
	if err != nil {
		return nil, err
	}

	id, resultCh := provider.newPendingRequest()
	defer provider.cancelPending(id)

	if err := provider.writeJSON(platformRequestMessage{
		ID:         id,
		Type:       typePlatformReq,
		Capability: req.Capability,
		Method:     req.Method,
		Params:     req.Params,
	}); err != nil {
		return nil, fmt.Errorf("send platform request to %s: %w", provider.ClientName, err)
	}

	select {
	case msg, ok := <-resultCh:
		if !ok {
			return nil, errProviderDisconnected
		}
		if !msg.Success {
			if msg.Error != nil {
				return nil, fmt.Errorf("%s: %s", msg.Error.Code, msg.Error.Message)
			}
			return nil, fmt.Errorf("platform request failed")
		}
		return msg.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-provider.done:
		return nil, errProviderDisconnected
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
	sort.Strings(accounts)
	return accounts
}

func (r *Registry) selectProvider(req CallRequest) (*Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if req.Account != "" {
		provider := firstCapableProvider(r.byAccount[req.Account], req.ClientID, req.Capability, req.Method)
		if provider == nil {
			if req.ClientID != "" {
				return nil, fmt.Errorf("no connected platform provider for account %q and client_id %q supports %s/%s", req.Account, req.ClientID, req.Capability, req.Method)
			}
			return nil, fmt.Errorf("no connected platform provider for account %q supports %s/%s", req.Account, req.Capability, req.Method)
		}
		return provider, nil
	}

	var (
		selected        *Provider
		matchedAccounts []string
	)
	for account, providers := range r.byAccount {
		if provider := firstCapableProvider(providers, req.ClientID, req.Capability, req.Method); provider != nil {
			matchedAccounts = append(matchedAccounts, account)
			if selected == nil {
				selected = provider
			}
		}
	}

	switch len(matchedAccounts) {
	case 0:
		if req.ClientID != "" {
			return nil, fmt.Errorf("no connected platform provider with client_id %q supports %s/%s", req.ClientID, req.Capability, req.Method)
		}
		return nil, fmt.Errorf("no connected platform provider supports %s/%s", req.Capability, req.Method)
	case 1:
		return selected, nil
	default:
		sort.Strings(matchedAccounts)
		return nil, fmt.Errorf("multiple accounts have connected platform providers for %s/%s (%s); specify account", req.Capability, req.Method, strings.Join(matchedAccounts, ", "))
	}
}

func firstCapableProvider(providers []*Provider, clientID, capability, method string) *Provider {
	for _, p := range providers {
		if clientID != "" && p.ClientID != clientID {
			continue
		}
		if p.supports(capability, method) {
			return p
		}
	}
	return nil
}

func (p *Provider) setCapabilities(capabilities []Capability) {
	normalized := make([]Capability, 0, len(capabilities))
	index := make(map[string]capabilityState, len(capabilities))
	for _, cap := range capabilities {
		name := strings.TrimSpace(cap.Name)
		if name == "" {
			continue
		}

		methods := make([]string, 0, len(cap.Methods))
		methodIndex := make(map[string]bool, len(cap.Methods))
		for _, method := range cap.Methods {
			method = strings.TrimSpace(method)
			if method == "" || methodIndex[method] {
				continue
			}
			methodIndex[method] = true
			methods = append(methods, method)
		}

		index[name] = capabilityState{
			version: strings.TrimSpace(cap.Version),
			methods: methodIndex,
		}
		normalized = append(normalized, Capability{
			Name:    name,
			Version: strings.TrimSpace(cap.Version),
			Methods: methods,
		})
	}

	p.stateMu.Lock()
	p.capabilities = normalized
	p.capabilityIndex = index
	p.stateMu.Unlock()
}

func (p *Provider) capabilitiesSnapshot() []Capability {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()
	if len(p.capabilities) == 0 {
		return nil
	}
	clone := make([]Capability, len(p.capabilities))
	for i, cap := range p.capabilities {
		methods := append([]string(nil), cap.Methods...)
		clone[i] = Capability{
			Name:    cap.Name,
			Version: cap.Version,
			Methods: methods,
		}
	}
	return clone
}

func (p *Provider) supports(capability, method string) bool {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()

	state, ok := p.capabilityIndex[capability]
	if !ok {
		return false
	}
	if method == "" || len(state.methods) == 0 {
		return true
	}
	return state.methods[method]
}

func (p *Provider) newPendingRequest() (int64, chan Message) {
	id := p.nextRequestID.Add(1)
	ch := make(chan Message, 1)

	p.stateMu.Lock()
	if p.pending == nil {
		p.pending = make(map[int64]chan Message)
	}
	p.pending[id] = ch
	p.stateMu.Unlock()

	return id, ch
}

func (p *Provider) cancelPending(id int64) {
	if ch := p.takePending(id); ch != nil {
		close(ch)
	}
}

func (p *Provider) resolvePending(msg Message) bool {
	ch := p.takePending(msg.ID)
	if ch == nil {
		return false
	}
	ch <- msg
	close(ch)
	return true
}

func (p *Provider) failPending(msg Message) {
	p.stateMu.Lock()
	pending := p.pending
	p.pending = make(map[int64]chan Message)
	p.stateMu.Unlock()

	for _, ch := range pending {
		ch <- msg
		close(ch)
	}
}

func (p *Provider) takePending(id int64) chan Message {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	ch := p.pending[id]
	if ch != nil {
		delete(p.pending, id)
	}
	return ch
}

func (p *Provider) writeJSON(msg any) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	return writeJSONWithDeadline(p.Conn, writeWait, msg)
}

func providerToInfo(p *Provider) ProviderInfo {
	return ProviderInfo{
		ID:           p.ID,
		Account:      p.Account,
		ClientName:   p.ClientName,
		ClientID:     p.ClientID,
		ConnectedAt:  p.ConnectedAt,
		Capabilities: p.capabilitiesSnapshot(),
	}
}

// generateProviderID creates a provider ID with the prov_ prefix and a
// random hex suffix.
func generateProviderID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use time-based suffix to avoid collisions.
		slog.Error("crypto/rand.Read failed generating provider ID; falling back to time-based ID", "err", err)
		ts := time.Now().UnixNano()
		for i := 0; i < 6; i++ {
			b[5-i] = byte(ts & 0xff)
			ts >>= 8
		}
	}
	return "prov_" + hex.EncodeToString(b)
}
