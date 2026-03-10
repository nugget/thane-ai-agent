package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
// temporal context. AgoSeconds is negative for past interactions.
type InteractionRef struct {
	AgoSeconds int64    `json:"ago_seconds"`
	Channel    string   `json:"channel,omitempty"`
	SessionID  string   `json:"session_id,omitempty"`
	Topics     []string `json:"topics,omitempty"`
}

// ContactLookup resolves a contact name to a rich context profile for
// system prompt injection. The source parameter identifies the channel
// (e.g., "signal", "email") so the implementation can gate fields by
// trust zone. Returns nil when no matching contact is found.
type ContactLookup interface {
	LookupContact(name string, source string) *ContactContext
}

// channelDefaults maps source hint values to channel-specific behavioral
// notes. These describe the channel's characteristics so the agent can
// adjust its communication style.
var channelDefaults = map[string]string{
	"signal": "Terse input is normal; typing on mobile devices is slow " +
		"and brevity is not an indicator of emotional state.",
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

// GetContext returns a channel context block if the request context
// carries a "source" hint that matches a known channel. When a contact
// lookup is available and the sender resolves to a known contact, the
// block includes a structured JSON contact profile with trust policy,
// communication channels, and interaction history. Returns an empty
// string for unrecognized sources or missing hints.
func (p *ChannelProvider) GetContext(ctx context.Context, _ string) (string, error) {
	hints := tools.HintsFromContext(ctx)
	if hints == nil {
		return "", nil
	}

	source := hints["source"]
	if source == "" {
		return "", nil
	}

	// Determine sender identity.
	senderName := hints["sender_name"]
	senderRaw := hints["sender"] // phone number, matrix ID, email, etc.

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

	// Build output: markdown header + channel note + JSON contact block.
	var sb strings.Builder
	sb.WriteString("### Channel Context\n")
	fmt.Fprintf(&sb, "- **Source:** %s\n", formatSourceName(source))
	if channelNote != "" {
		sb.WriteString(fmt.Sprintf("- **Note:** %s\n", channelNote))
	}

	envelope := map[string]*ContactContext{"contact": contactCtx}
	jsonBytes, err := json.Marshal(envelope)
	if err != nil {
		// Fall back to name-only if JSON fails (shouldn't happen).
		sb.WriteString(fmt.Sprintf("- **Participant:** %s\n", contactCtx.Name))
		return sb.String(), nil
	}

	sb.WriteString("```json\n")
	sb.Write(jsonBytes)
	sb.WriteString("\n```\n")

	return sb.String(), nil
}

// formatSourceName returns a human-readable channel name.
func formatSourceName(source string) string {
	switch source {
	case "signal":
		return "Signal"
	case "matrix":
		return "Matrix"
	case "email":
		return "Email"
	default:
		return source
	}
}
