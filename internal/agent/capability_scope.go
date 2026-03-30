// Package agent provides the core agent loop implementation.
//
// This file implements per-Run capability scoping. Each Run() call
// creates a capabilityScope seeded with always-active and channel-pinned
// tags, stored in the context. Tool handlers (request_capability,
// drop_capability) mutate the scope via context, giving each
// conversation its own isolated capability state.
package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/nugget/thane-ai-agent/internal/config"
)

type capScopeKey struct{}

// withCapabilityScope stores the scope in ctx.
func withCapabilityScope(ctx context.Context, scope *capabilityScope) context.Context {
	return context.WithValue(ctx, capScopeKey{}, scope)
}

// capabilityScopeFromContext extracts the scope, or nil if not set.
func capabilityScopeFromContext(ctx context.Context) *capabilityScope {
	if s, ok := ctx.Value(capScopeKey{}).(*capabilityScope); ok {
		return s
	}
	return nil
}

// snapshotTagsFromContext returns a copy of the active tags from the
// context-scoped capability scope. Returns nil if no scope is set.
func snapshotTagsFromContext(ctx context.Context) map[string]bool {
	if s := capabilityScopeFromContext(ctx); s != nil {
		return s.Snapshot()
	}
	return nil
}

// capabilityScope holds the active capability tags for a single Run()
// call. It is stored in the context and mutated by tool handlers.
// Each scope is independent — concurrent Run() calls get separate scopes.
type capabilityScope struct {
	mu      sync.Mutex
	active  map[string]bool                       // tags currently active
	pinned  map[string]bool                       // channel-pinned (cannot be dropped)
	capTags map[string]config.CapabilityTagConfig // read-only reference to config
}

// newCapabilityScope creates a scope seeded with always-active tags and
// global lenses. The capTags reference is stored read-only for
// configured-tag checks.
func newCapabilityScope(capTags map[string]config.CapabilityTagConfig, globalLenses []string) *capabilityScope {
	s := &capabilityScope{
		active:  make(map[string]bool),
		pinned:  make(map[string]bool),
		capTags: capTags,
	}
	for tag, cfg := range capTags {
		if cfg.AlwaysActive {
			s.active[tag] = true
		}
	}
	// Seed global lenses — these are persistent behavioral modes
	// that apply to all conversations.
	for _, lens := range globalLenses {
		s.active[lens] = true
		s.pinned[lens] = true // protect from deactivate_capability
	}
	return s
}

// PinChannelTags adds the given tags as channel-pinned. They become
// active and cannot be dropped via Drop().
func (s *capabilityScope) PinChannelTags(tags []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tag := range tags {
		s.active[tag] = true
		s.pinned[tag] = true
	}
}

// Snapshot returns a copy of the active tags map. Safe for concurrent use.
func (s *capabilityScope) Snapshot() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := make(map[string]bool, len(s.active))
	for k, v := range s.active {
		snap[k] = v
	}
	return snap
}

// Request activates a capability tag. Both configured tags (with tools
// and static context) and ad-hoc tags (KB articles, talents, live
// providers only) are accepted.
func (s *capabilityScope) Request(tag string) error {
	s.mu.Lock()
	s.active[tag] = true
	s.mu.Unlock()
	return nil
}

// Drop deactivates a capability tag. Always-active and channel-pinned
// tags cannot be dropped.
func (s *capabilityScope) Drop(tag string) error {
	if cfg, ok := s.capTags[tag]; ok && cfg.AlwaysActive {
		return fmt.Errorf("cannot drop always-active tag: %q", tag)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pinned[tag] {
		return fmt.Errorf("cannot drop channel-pinned tag %q (active for current channel)", tag)
	}
	delete(s.active, tag)
	return nil
}
