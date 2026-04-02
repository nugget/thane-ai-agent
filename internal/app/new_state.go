package app

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/contacts"
	"github.com/nugget/thane-ai-agent/internal/forge"
	"github.com/nugget/thane-ai-agent/internal/homeassistant"
	"github.com/nugget/thane-ai-agent/internal/knowledge"
	"github.com/nugget/thane-ai-agent/internal/paths"
	"github.com/nugget/thane-ai-agent/internal/talents"
)

// newState carries local variables from [New] that are shared across
// initialization phases but not stored on [App]. Passed by pointer
// through each init* method so closures that capture s see later
// assignments (e.g., personTracker is forward-declared in [initStores]
// and constructed in [initAwareness]).
type newState struct {
	ctx context.Context

	// Loaded in initStores, consumed by initAgentLoop and initDelegation.
	parsedTalents  []talents.Talent
	personaContent string

	// Forward-declared in initStores (for connwatch OnReady closure),
	// constructed in initAwareness.
	personTracker *contacts.PresenceTracker

	// Built in initAgentLoop, used by initChannels and initDelegation.
	resolver *paths.Resolver

	// Built in initChannels, used by initAwareness and initServers.
	wakeStore *homeassistant.WakeStore
	embClient knowledge.EmbeddingClient

	// Built in initChannels, used by initDelegation.
	forgeOpLog *forge.OperationLog

	// Tools registered by deferred workers (e.g., Signal tools). The
	// capability-tag validation in initDelegation skips these names so
	// it doesn't warn about tools that will appear after StartWorkers.
	deferredTools map[string]bool
}
