package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// ContactSummary holds the subset of contact information injected into
// the channel context block. This is intentionally a small struct to
// avoid coupling ChannelProvider to the full contacts package.
type ContactSummary struct {
	Name         string
	Relationship string
	Summary      string
	Facts        map[string][]string // key→values, e.g. "timezone"→["America/Chicago"]
}

// ContactLookup resolves a contact name to a summary for system prompt
// injection. Returns nil when no matching contact is found.
type ContactLookup interface {
	LookupContactByName(name string) *ContactSummary
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
// it resolves sender names to contact records and injects relationship
// details, summaries, and relevant facts alongside the channel notes.
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
// block includes relationship details and context notes. Returns an
// empty string for unrecognized sources or missing hints.
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

	var sb strings.Builder
	sb.WriteString("### Channel Context\n")
	sb.WriteString(fmt.Sprintf("- **Source:** %s\n", formatSourceName(source)))

	// Try contact resolution when we have a sender name.
	var contact *ContactSummary
	if senderName != "" && p.contacts != nil {
		contact = p.contacts.LookupContactByName(senderName)
	}

	if contact != nil {
		// Known contact — rich context.
		sb.WriteString(fmt.Sprintf("- **Participant:** %s", contact.Name))
		if contact.Relationship != "" {
			sb.WriteString(fmt.Sprintf(" (%s)", contact.Relationship))
		}
		sb.WriteString("\n")
		if contact.Summary != "" {
			sb.WriteString(fmt.Sprintf("  - Context: %s\n", contact.Summary))
		}
		// Include relevant facts (timezone, preferences, etc.).
		for _, key := range sortedFactKeys(contact.Facts) {
			values := contact.Facts[key]
			sb.WriteString(fmt.Sprintf("  - %s: %s\n", key, strings.Join(values, ", ")))
		}
	} else {
		// Unknown contact — minimal context.
		displayName := senderName
		if displayName == "" {
			displayName = senderRaw
		}
		if displayName == "" {
			displayName = "unknown sender"
		}
		sb.WriteString(fmt.Sprintf("- **Participant:** %s (unknown contact)\n", displayName))
	}

	if channelNote != "" {
		sb.WriteString(fmt.Sprintf("- **Note:** %s\n", channelNote))
	}

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

// sortedFactKeys returns fact keys in deterministic order for stable output.
func sortedFactKeys(facts map[string][]string) []string {
	if len(facts) == 0 {
		return nil
	}
	keys := make([]string, 0, len(facts))
	for k := range facts {
		keys = append(keys, k)
	}
	// Simple insertion sort — fact maps are small.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
