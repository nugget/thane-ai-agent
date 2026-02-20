package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/tools"
)

// maxFactKeys caps the number of contact fact keys rendered into the
// channel context block. This prevents large contact records from
// bloating the system prompt.
const maxFactKeys = 10

// maxFieldLen caps the length of individual text fields (name,
// relationship, summary, fact values) injected into the prompt.
const maxFieldLen = 200

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
		sb.WriteString(fmt.Sprintf("- **Participant:** %s", sanitizeField(contact.Name)))
		if contact.Relationship != "" {
			sb.WriteString(fmt.Sprintf(" (%s)", sanitizeField(contact.Relationship)))
		}
		sb.WriteString("\n")
		if contact.Summary != "" {
			sb.WriteString(fmt.Sprintf("  - Context: %s\n", sanitizeField(contact.Summary)))
		}
		// Include relevant facts (timezone, preferences, etc.),
		// capped to avoid bloating the system prompt.
		keys := sortedFactKeys(contact.Facts)
		if len(keys) > maxFactKeys {
			keys = keys[:maxFactKeys]
		}
		for _, key := range keys {
			values := contact.Facts[key]
			joined := sanitizeField(strings.Join(values, ", "))
			sb.WriteString(fmt.Sprintf("  - %s: %s\n", sanitizeField(key), joined))
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
		sb.WriteString(fmt.Sprintf("- **Participant:** %s (unknown contact)\n", sanitizeField(displayName)))
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

// sanitizeField normalizes a string for safe system prompt injection.
// It collapses newlines and excessive whitespace into single spaces and
// truncates to maxFieldLen to prevent prompt bloat.
func sanitizeField(s string) string {
	// Collapse any whitespace runs (including newlines) to a single space.
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxFieldLen {
		return s[:maxFieldLen] + "…"
	}
	return s
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
	sort.Strings(keys)
	return keys
}
