package memory

import (
	"encoding/json"
	"strings"
)

// ChannelBinding captures the runtime identity of a channel-backed
// conversation. It links a live channel/address pair to any known
// contact record so downstream code can gate on a typed binding
// instead of reconstructing identity from hints.
type ChannelBinding struct {
	Channel     string `json:"channel,omitempty"`
	Address     string `json:"address,omitempty"`
	ContactID   string `json:"contact_id,omitempty"`
	ContactName string `json:"contact_name,omitempty"`
	TrustZone   string `json:"trust_zone,omitempty"`
	LinkSource  string `json:"link_source,omitempty"`
	IsOwner     bool   `json:"is_owner,omitempty"`
}

// Clone returns a deep copy of the binding.
func (b *ChannelBinding) Clone() *ChannelBinding {
	if b == nil {
		return nil
	}
	clone := *b
	return &clone
}

// Normalize returns a trimmed copy of the binding, or nil when the
// binding carries no meaningful channel identity.
func (b *ChannelBinding) Normalize() *ChannelBinding {
	if b == nil {
		return nil
	}
	clone := b.Clone()
	clone.Channel = strings.TrimSpace(clone.Channel)
	clone.Address = strings.TrimSpace(clone.Address)
	clone.ContactID = strings.TrimSpace(clone.ContactID)
	clone.ContactName = strings.TrimSpace(clone.ContactName)
	clone.TrustZone = strings.TrimSpace(clone.TrustZone)
	clone.LinkSource = strings.TrimSpace(clone.LinkSource)
	if clone.Channel == "" {
		return nil
	}
	return clone
}

// ConversationMetadata holds typed metadata associated with a live
// conversation. It is stored as JSON so new fields can be added
// without schema churn.
type ConversationMetadata struct {
	ChannelBinding *ChannelBinding `json:"channel_binding,omitempty"`
}

// Clone returns a deep copy of the metadata.
func (m *ConversationMetadata) Clone() *ConversationMetadata {
	if m == nil {
		return nil
	}
	return &ConversationMetadata{
		ChannelBinding: m.ChannelBinding.Clone(),
	}
}

// Normalize returns a cleaned copy of the metadata, or nil when it
// contains no meaningful fields.
func (m *ConversationMetadata) Normalize() *ConversationMetadata {
	if m == nil {
		return nil
	}
	clone := &ConversationMetadata{
		ChannelBinding: m.ChannelBinding.Normalize(),
	}
	if clone.ChannelBinding == nil {
		return nil
	}
	return clone
}

func parseConversationMetadata(raw string) (*ConversationMetadata, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var meta ConversationMetadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return nil, err
	}
	return meta.Normalize(), nil
}

func marshalConversationMetadata(meta *ConversationMetadata) (string, error) {
	meta = meta.Normalize()
	if meta == nil {
		return "", nil
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
