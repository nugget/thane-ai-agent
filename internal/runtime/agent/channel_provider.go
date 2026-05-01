package agent

import (
	"context"

	"github.com/nugget/thane-ai-agent/internal/model/promptfmt"
	"github.com/nugget/thane-ai-agent/internal/runtime/agentctx"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// ContactContext holds the rich contact profile injected into the system
// prompt as structured JSON. Fields are populated by the ContactLookup
// implementation and gated by the contact's trust zone — lower-trust
// zones receive fewer fields.
type ContactContext struct {
	ID              string           `json:"id,omitempty"`
	Name            string           `json:"name"`
	GivenName       string           `json:"given_name,omitempty"`
	FamilyName      string           `json:"family_name,omitempty"`
	TrustZone       string           `json:"trust_zone"`
	TrustPolicy     *TrustPolicyView `json:"trust_policy"`
	Groups          []string         `json:"groups,omitempty"`
	Org             *string          `json:"org,omitempty"`
	Title           *string          `json:"title,omitempty"`
	Role            *string          `json:"role,omitempty"`
	Summary         string           `json:"summary,omitempty"`
	Related         []RelatedContact `json:"related,omitempty"`
	Channels        map[string]any   `json:"channels,omitempty"`
	LastInteraction *InteractionRef  `json:"last_interaction,omitempty"`
	ContactSince    string           `json:"contact_since,omitempty"`
}

type channelContextEnvelope struct {
	Source  string          `json:"source"`
	Note    string          `json:"note,omitempty"`
	Contact *ContactContext `json:"contact"`
}

// TrustPolicyView is the JSON-serializable view of a trust zone's
// capability matrix. It exposes the policy dimensions that the agent
// needs to adapt its behavior.
type TrustPolicyView struct {
	FrontierModel     bool   `json:"frontier_model"`
	ProactiveOutreach string `json:"proactive_outreach"`
	ToolAccess        string `json:"tool_access"`
	SendGating        string `json:"send_gating"`
}

// RelatedContact represents a RELATED vCard entry on a contact.
type RelatedContact struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

// InteractionRef summarizes the contact's most recent interaction for
// temporal context. Ago is a signed-second delta such as "-3600s".
type InteractionRef struct {
	Ago       string   `json:"ago"`
	Channel   string   `json:"channel,omitempty"`
	SessionID string   `json:"session_id,omitempty"`
	Topics    []string `json:"topics,omitempty"`
}

// ContactLookup resolves contact identity into trust-gated profile and
// origin-policy context for system prompt injection. The source
// parameter identifies the channel (e.g., "signal", "email") so the
// implementation can gate fields and source-specific policy by trust
// zone. Returns nil when no matching contact is found.
type ContactLookup interface {
	LookupContact(name string, source string) *ContactContext
	LookupContactByID(id string, source string) *ContactContext
	LookupContactOriginPolicy(id string, name string, source string) *ContactOriginPolicy
}

// ContactOriginPolicy is contact-owned session shaping applied when a
// contact is the request origin.
type ContactOriginPolicy struct {
	Tags        []string `json:"tags,omitempty"`
	ContextRefs []string `json:"context_refs,omitempty"`
}

// channelDefaults maps source hint values to channel-specific behavioral
// notes. These describe the channel's characteristics so the agent can
// adjust its communication style.
var channelDefaults = map[string]string{
	"signal": "Signal (mobile chat app). Plain text only — no markdown " +
		"formatting, headers, or bullet points. Write like you're texting " +
		"a friend: natural breaks instead of structured lists, emoji when " +
		"they fit the mood. One thought per message for complex topics " +
		"(Signal makes threads easy). Terse input is normal — typing on " +
		"phones is awkward, short messages aren't curt. Your responses " +
		"can be fuller but stay conversational. You are chatting, not " +
		"presenting.",
}

// ChannelProvider is a ContextProvider that injects channel-specific
// context into the system prompt based on the "source" routing hint
// attached to the request context. When a ContactLookup is configured,
// it resolves sender names to contact records and injects a structured
// JSON contact profile alongside the channel notes.
type ChannelProvider struct {
	contacts ContactLookup
}

// NewChannelProvider creates a channel awareness context provider.
// The contacts parameter is optional — pass nil to disable contact
// resolution (the provider will still emit channel notes).
func NewChannelProvider(contacts ContactLookup) *ChannelProvider {
	return &ChannelProvider{contacts: contacts}
}

// TagContext returns a channel context block if the request context
// carries a "source" hint that matches a known channel. When a contact
// lookup is available and the sender resolves to a known contact, the
// block includes a structured JSON contact profile with trust policy,
// communication channels, and interaction history. Returns an empty
// string for unrecognized sources or missing hints.
func (p *ChannelProvider) TagContext(ctx context.Context, _ agentctx.ContextRequest) (string, error) {
	hints := tools.HintsFromContext(ctx)
	if hints == nil {
		hints = map[string]string{}
	}

	source := hints["source"]
	binding := tools.ChannelBindingFromContext(ctx)
	if source == "" && binding != nil {
		source = binding.Channel
	}
	if source == "" {
		return "", nil
	}

	// Determine sender identity.
	senderName := hints["sender_name"]
	senderRaw := hints["sender"] // phone number, matrix ID, email, etc.
	if senderName == "" && binding != nil {
		senderName = binding.ContactName
	}
	if senderRaw == "" && binding != nil {
		senderRaw = binding.Address
	}

	// Only emit context for recognized channels.
	channelNote, knownChannel := channelDefaults[source]
	if !knownChannel {
		return "", nil
	}

	// Try contact resolution when we have a sender name.
	var contactCtx *ContactContext
	if senderName != "" && p.contacts != nil {
		contactCtx = p.contacts.LookupContact(senderName, source)
	}
	if contactCtx == nil && binding != nil {
		contactCtx = contactContextFromBinding(binding, source)
	}

	// Synthesize unknown-sender context when resolution fails.
	if contactCtx == nil {
		displayName := senderName
		if displayName == "" {
			displayName = senderRaw
		}
		if displayName == "" {
			displayName = "unknown sender"
		}
		contactCtx = &ContactContext{
			Name:      displayName,
			TrustZone: "unknown",
			TrustPolicy: &TrustPolicyView{
				FrontierModel:     false,
				ProactiveOutreach: "none",
				ToolAccess:        "none",
				SendGating:        "blocked",
			},
		}
	}

	envelope := channelContextEnvelope{
		Source:  source,
		Note:    channelNote,
		Contact: contactCtx,
	}
	payload := promptfmt.MarshalCompact(envelope)
	if promptfmt.HasMarshalError(payload) {
		envelope.Contact = contactContextWithoutChannels(contactCtx)
		payload = promptfmt.MarshalCompact(envelope)
	}

	return "### Channel Context\n\n" + payload + "\n", nil
}

func contactContextFromBinding(binding *memory.ChannelBinding, source string) *ContactContext {
	binding = binding.Normalize()
	if binding == nil {
		return nil
	}
	displayName := binding.ContactName
	if displayName == "" {
		displayName = binding.Address
	}
	if displayName == "" {
		return nil
	}
	policy := contacts.Policy(binding.TrustZone)
	if binding.TrustZone == "" {
		policy = contacts.Policy(contacts.ZoneUnknown)
	}
	ctx := &ContactContext{
		ID:        binding.ContactID,
		Name:      displayName,
		TrustZone: binding.TrustZone,
		TrustPolicy: &TrustPolicyView{
			FrontierModel:     policy.FrontierModelAccess,
			ProactiveOutreach: policy.ProactiveOutreach,
			ToolAccess:        policy.ToolAccess,
			SendGating:        policy.SendGating,
		},
	}
	if ctx.TrustZone == "" {
		ctx.TrustZone = string(contacts.ZoneUnknown)
	}
	if source != "" {
		if binding.Address == "" {
			ctx.Channels = map[string]any{source: true}
		} else {
			ctx.Channels = map[string]any{source: binding.Address}
		}
	}
	return ctx
}

func contactContextWithoutChannels(contactCtx *ContactContext) *ContactContext {
	if contactCtx == nil {
		return nil
	}
	clone := *contactCtx
	clone.Channels = nil
	return &clone
}
