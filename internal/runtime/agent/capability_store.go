package agent

import (
	"encoding/json"

	"github.com/nugget/thane-ai-agent/internal/platform/opstate"
)

const capTagNamespace = "capability_tags"

// OpstateCapabilityTagStore implements CapabilityTagStore using opstate
// for persistence. Tags are stored as a JSON array per conversation ID.
type OpstateCapabilityTagStore struct {
	state *opstate.Store
}

// NewOpstateCapabilityTagStore creates a capability tag store backed by opstate.
func NewOpstateCapabilityTagStore(state *opstate.Store) *OpstateCapabilityTagStore {
	return &OpstateCapabilityTagStore{state: state}
}

// LoadTags returns the previously activated tags for a conversation.
func (s *OpstateCapabilityTagStore) LoadTags(conversationID string) ([]string, error) {
	raw, err := s.state.Get(capTagNamespace, conversationID)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return nil, err
	}
	return tags, nil
}

// SaveTags persists the active tags for a conversation.
func (s *OpstateCapabilityTagStore) SaveTags(conversationID string, tags []string) error {
	if len(tags) == 0 {
		return s.state.Delete(capTagNamespace, conversationID)
	}
	data, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	return s.state.Set(capTagNamespace, conversationID, string(data))
}
