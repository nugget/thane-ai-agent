package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	"github.com/nugget/thane-ai-agent/internal/runtime/archivist"
	"github.com/nugget/thane-ai-agent/internal/state/contacts"
)

// enqueueContactDossierRefresh records one committed structured-contact
// mutation as frontier work for the archivist. The contact UUID is the dedup
// identity, so repeated writes coalesce into the latest authoritative field
// set without waking the self-paced consumer.
func (a *App) enqueueContactDossierRefresh(ctx context.Context, mutation contacts.ContactMutation) error {
	if a == nil || a.loopQueue == nil {
		return fmt.Errorf("loop queue not configured")
	}
	if mutation.ContactID == uuid.Nil {
		return fmt.Errorf("empty contact_id")
	}

	metadata := map[string]string{
		"contact_id":   mutation.ContactID.String(),
		"contact_name": strings.TrimSpace(mutation.ContactName),
		"created":      strconv.FormatBool(mutation.Created),
		"fields":       strings.Join(mutation.Fields, ","),
	}
	if provenance := mutation.Provenance; provenance != nil {
		addContactRefreshMetadata(metadata, "source", provenance.Source)
		addContactRefreshMetadata(metadata, "model", provenance.Model)
		addContactRefreshMetadata(metadata, "loop_id", provenance.LoopID)
		addContactRefreshMetadata(metadata, "conversation_id", provenance.ConversationID)
		addContactRefreshMetadata(metadata, "session_id", provenance.SessionID)
		addContactRefreshMetadata(metadata, "request_id", provenance.RequestID)
		addContactRefreshMetadata(metadata, "tool_call_id", provenance.ToolCallID)
		if provenance.Iteration != nil {
			metadata["iteration"] = strconv.Itoa(*provenance.Iteration)
		}
	}

	payload := messages.LoopNotifyPayload{
		Events: []messages.LoopEventPayload{{
			Source: "contact_save",
			Type:   "structured_contact_changed",
			ID:     mutation.ContactID.String(),
			Title:  mutation.ContactName,
			Summary: fmt.Sprintf(
				"Authoritative structured contact fields changed for %q (%s). Resolve the current contact by this exact name, read its canonical dossier, and refresh the dossier only when the longitudinal synthesis materially changes; do not copy structured identity boilerplate into dossier prose.",
				mutation.ContactName,
				strings.Join(mutation.Fields, ", "),
			),
			Metadata: metadata,
		}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal contact dossier refresh: %w", err)
	}
	// The structured contact is already committed when this handoff runs.
	// Detach request cancellation so a disconnect cannot erase the durable
	// refresh, while retaining the shared bounded delivery window.
	deliveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), queueWakeDeliveryTimeout)
	defer cancel()
	if err := a.loopQueue.Enqueue(deliveryCtx, archivist.DefinitionName, "contact:"+mutation.ContactID.String(), 0, raw); err != nil {
		return fmt.Errorf("enqueue contact dossier refresh: %w", err)
	}
	return nil
}

func addContactRefreshMetadata(metadata map[string]string, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		metadata[key] = value
	}
}
