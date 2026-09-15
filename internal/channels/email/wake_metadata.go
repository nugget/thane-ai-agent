package email

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

// buildBatchEvents converts a chunk of envelopes into structured
// LoopEventPayloads and reports the highest UID observed in the chunk.
// Every fact the handler needs to act on a message travels in the
// event's metadata: the account and folder the UID belongs to, the
// Message-ID, the sender's address and display name, and the contact
// directory's answer about the sender — contact_id, contact_name,
// is_owner, contact_status, and the effective trust_zone ("unknown"
// for a stranger). is_owner says the record is the operator's, not that
// the operator wrote the message; a From header is a claim. automated
// is present, as "true", only for a no-reply, notification, or bounce
// sender, whose trust_zone is capped at known; the sender is still
// recognised through contact_id. flags lists the message's IMAP flags
// as the server spells them, joined with commas, and is absent when the
// message has none, so a handler can tell mail the operator already
// read or flagged in another client from mail nobody has touched.
func (p *Poller) buildBatchEvents(accountName string, chunk []Envelope, lookup *identityLookup) ([]messages.LoopEventPayload, uint32) {
	events := make([]messages.LoopEventPayload, 0, len(chunk))
	var maxUID uint32
	for _, env := range chunk {
		match := lookup.resolve(env.From)
		if env.UID > maxUID {
			maxUID = env.UID
		}
		metadata := map[string]string{
			"account":        accountName,
			"folder":         DefaultFolder,
			"uid":            strconv.FormatUint(uint64(env.UID), 10),
			"from":           env.From.String(),
			"from_address":   env.From.Key(),
			"trust_zone":     match.TrustZone,
			"contact_status": string(match.Status),
			"is_owner":       "false",
		}
		if env.From.Name != "" {
			metadata["from_name"] = env.From.Name
		}
		if env.MessageID != "" {
			metadata["message_id"] = env.MessageID
		}
		if match.Automated {
			metadata["automated"] = "true"
		}
		if flags := wakeFlags(env.Flags); flags != "" {
			metadata["flags"] = flags
		}
		if match.Binding != nil {
			metadata["contact_id"] = match.Binding.ContactID
			metadata["contact_name"] = match.Binding.ContactName
			metadata["is_owner"] = strconv.FormatBool(match.Binding.IsOwner)
		}
		events = append(events, messages.LoopEventPayload{
			Source:     "email_poll",
			Type:       "new_message",
			ID:         fmt.Sprintf("%s:%d", accountName, env.UID),
			Title:      env.Subject,
			Summary:    fmt.Sprintf("From %s (account %s, folder %s)", env.From.String(), accountName, DefaultFolder),
			ObservedAt: env.Date,
			Metadata:   metadata,
		})
	}
	return events, maxUID
}

// wakeFlags joins a message's flags for its wake metadata, dropping
// blanks, or returns "" when none remain.
func wakeFlags(flags []string) string {
	kept := make([]string, 0, len(flags))
	for _, f := range flags {
		if f = strings.TrimSpace(f); f != "" {
			kept = append(kept, f)
		}
	}
	return strings.Join(kept, ",")
}
