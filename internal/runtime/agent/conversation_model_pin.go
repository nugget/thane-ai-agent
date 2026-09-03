package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/tools"
)

// conversationModelPins is the in-process set of conversation model
// pins. It is memory-only on purpose: a pin is an operator's in-the-moment
// steer for one conversation, and the recovery path for a bad steer is a
// restart, not a hunt through storage. Router cooldowns follow the same
// rule for the same reason.
type conversationModelPins struct {
	mu   sync.Mutex
	pins map[string]tools.ConversationModelPin
}

func (p *conversationModelPins) get(conversationID string) (tools.ConversationModelPin, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pin, ok := p.pins[conversationID]
	return pin, ok
}

func (p *conversationModelPins) set(pin tools.ConversationModelPin) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pins == nil {
		p.pins = make(map[string]tools.ConversationModelPin)
	}
	p.pins[pin.ConversationID] = pin
}

func (p *conversationModelPins) clear(conversationID string) (tools.ConversationModelPin, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pin, ok := p.pins[conversationID]
	if ok {
		delete(p.pins, conversationID)
	}
	return pin, ok
}

// recordFallback notes that the pin on conversationID could not serve a
// turn and model answered instead. A pin that was cleared meanwhile is
// left alone.
func (p *conversationModelPins) recordFallback(conversationID, model, reason string, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pin, ok := p.pins[conversationID]
	if !ok {
		return
	}
	pin.LastFallback = &tools.ConversationModelPinFallback{At: at, Model: model, Reason: reason}
	p.pins[conversationID] = pin
}

// PinConversationModel resolves ref against the live model catalog and
// holds conversationID to that deployment from its next turn. It
// implements [tools.ConversationModelPinner].
//
// Only references that would fail every turn are refused here: unknown
// or ambiguous names, deployments made inactive by operator policy, and
// deployments that cannot call tools. Anything narrower (no vision, a
// small window) is judged per turn, where the runtime routes around the
// pin for that turn instead of failing it.
func (l *Loop) PinConversationModel(conversationID, ref, reason string) (tools.ConversationModelPin, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return tools.ConversationModelPin{}, fmt.Errorf("conversation id is required to pin a model")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return tools.ConversationModelPin{}, fmt.Errorf("model is required: pass a deployment id or model name from model_registry_list")
	}
	cat := l.currentModelCatalog()
	if cat == nil {
		return tools.ConversationModelPin{}, fmt.Errorf("the model catalog is unavailable, so %q cannot be resolved to a deployment", ref)
	}
	dep, err := cat.ResolveDeploymentRef(ref)
	if err != nil {
		if fleet.IsUnknownModel(err) {
			return tools.ConversationModelPin{}, fmt.Errorf("unknown model %q; pass a deployment id or unique model name as listed by model_registry_list (activate the models tag), or clear=true to return to the router", ref)
		}
		return tools.ConversationModelPin{}, err
	}
	if dep.PolicyState == fleet.DeploymentPolicyStateInactive || dep.ResourcePolicyState == fleet.DeploymentPolicyStateInactive {
		return tools.ConversationModelPin{}, fmt.Errorf("deployment %q is inactive by operator policy and would fail every turn; choose another deployment, or re-enable it with model_deployment_set_policy first", dep.ID)
	}
	switch {
	case !dep.ProviderSupportsTools:
		return tools.ConversationModelPin{}, fmt.Errorf("deployment %q cannot serve conversation turns: its provider does not support tool use; choose a tool-capable deployment from model_registry_list", dep.ID)
	case !dep.SupportsTools:
		return tools.ConversationModelPin{}, fmt.Errorf("deployment %q cannot serve conversation turns: it is configured without tool support; choose a tool-capable deployment from model_registry_list", dep.ID)
	}

	pin := tools.ConversationModelPin{
		ConversationID:    conversationID,
		Model:             dep.ID,
		Provider:          dep.Provider,
		Resource:          dep.ResourceID,
		SupportsTools:     dep.SupportsTools,
		SupportsImages:    dep.SupportsImages,
		SupportsStreaming: dep.SupportsStreaming,
		ContextWindow:     dep.ContextWindow,
		Reason:            strings.TrimSpace(reason),
		PinnedAt:          l.now(),
	}
	l.conversationPins.set(pin)
	l.logger.Info("conversation model pinned",
		"conversation_id", conversationID,
		"model", dep.ID,
		"provider", dep.Provider,
		"resource", dep.ResourceID,
		"reason", pin.Reason,
	)
	return pin, nil
}

// ClearConversationModelPin removes the pin on conversationID and returns
// it. It implements [tools.ConversationModelPinner].
func (l *Loop) ClearConversationModelPin(conversationID string) (tools.ConversationModelPin, bool) {
	pin, ok := l.conversationPins.clear(strings.TrimSpace(conversationID))
	if ok {
		l.logger.Info("conversation model pin cleared",
			"conversation_id", pin.ConversationID,
			"model", pin.Model,
		)
	}
	return pin, ok
}

// ConversationModelPin reports the live pin on conversationID. It
// implements [tools.ConversationModelPinner] and serves the API's
// conversation view.
func (l *Loop) ConversationModelPin(conversationID string) (tools.ConversationModelPin, bool) {
	return l.conversationPins.get(strings.TrimSpace(conversationID))
}
