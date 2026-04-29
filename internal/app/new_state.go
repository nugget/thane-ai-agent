package app

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/integrations/forge"
	"github.com/nugget/thane-ai-agent/internal/model/talents"
	"github.com/nugget/thane-ai-agent/internal/platform/paths"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/knowledge"
)

// newState carries local variables from [New] that are shared across
// initialization phases but not stored on [App]. Passed by pointer
// through each init* method so closures that capture s see later
// assignments (e.g., personTracker is forward-declared in [initStores]
// and constructed in [initAwareness]).
type newState struct {
	ctx context.Context

	// Loaded in initChannels after document-root verification wiring,
	// consumed by finalizeCapabilityTags.
	parsedTalents  []talents.Talent
	personaContent string

	// Resolved in initAgentLoop, consumed by initChannels for startup
	// verification once the document store exists.
	resolvedInjectFiles []string

	// Forward-declared in initStores (for connwatch OnReady closure),
	// constructed in initAwareness.
	personTracker *contacts.PresenceTracker

	// Built in initAgentLoop, used by initChannels and initDelegation.
	resolver *paths.Resolver

	// Built in initChannels, used by initAwareness.
	embClient knowledge.EmbeddingClient

	// Built in initChannels, used by initDelegation.
	forgeOpLog *forge.OperationLog

	// Tools registered by deferred workers (e.g., Signal tools). The
	// capability-tag validation in initDelegation skips these names so
	// it doesn't warn about tools that will appear after StartWorkers.
	deferredTools map[string]bool
}
