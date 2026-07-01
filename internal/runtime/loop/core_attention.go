package loop

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
)

const (
	// CoreAttentionScope marks loop notifications that request core review.
	CoreAttentionScope = "core_attention"
	// CoreAttentionRequestKind is the default payload kind for direct
	// loop-to-core attention requests.
	CoreAttentionRequestKind = "core_attention_request"
)

// CoreAttentionTarget identifies the live loop that should receive
// supervisor/core attention requests.
type CoreAttentionTarget struct {
	LoopID     string     `json:"loop_id"`
	LoopName   string     `json:"loop_name"`
	Reason     string     `json:"reason"`
	LastActive *time.Time `json:"last_active,omitempty"`
}

// CoreWakeRequest describes one request to wake the designated core-attention
// loop. It is the Go-side counterpart to the request_core_attention tool:
// callers provide the concern and optional structured event context, while the
// helper resolves the live target and sends the loop notification envelope.
type CoreWakeRequest struct {
	From            messages.Identity
	Kind            string
	Concern         string
	SuggestedAction string
	Context         string
	Events          []messages.LoopEventPayload
	Priority        messages.Priority
	Scope           []string
	ForceSupervisor bool
}

// CoreWakeResult reports the resolved target and bus delivery outcome for a
// core wake.
type CoreWakeResult struct {
	Target   CoreAttentionTarget     `json:"target"`
	Delivery messages.DeliveryResult `json:"delivery"`
}

// WakeCoreLoop resolves the current core-attention target and delivers one
// signal envelope to it. Use this for system/runtime code that needs core
// review without choosing a human delivery channel directly.
func WakeCoreLoop(ctx context.Context, registry *Registry, bus *messages.Bus, req CoreWakeRequest) (CoreWakeResult, error) {
	if registry == nil {
		return CoreWakeResult{}, fmt.Errorf("core attention target unavailable: loop registry is not configured")
	}
	if bus == nil {
		return CoreWakeResult{}, fmt.Errorf("message bus is not configured")
	}
	target, err := ResolveCoreAttentionTarget(registry.Statuses())
	if err != nil {
		return CoreWakeResult{}, err
	}
	env := CoreWakeEnvelope(target, req)
	delivery, err := bus.Send(ctx, env)
	if err != nil {
		return CoreWakeResult{}, err
	}
	return CoreWakeResult{Target: target, Delivery: delivery}, nil
}

// CoreWakeEnvelope builds the message envelope used by [WakeCoreLoop]. Tests
// and callers that need to inspect the model-facing payload can use this
// directly; production delivery should normally go through [WakeCoreLoop].
func CoreWakeEnvelope(target CoreAttentionTarget, req CoreWakeRequest) messages.Envelope {
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = CoreAttentionRequestKind
	}
	priority := req.Priority
	if priority == "" {
		priority = messages.PriorityNormal
	}
	return messages.Envelope{
		From: req.From,
		To: messages.Destination{
			Kind:     messages.DestinationLoop,
			Target:   target.LoopID,
			Selector: messages.SelectorID,
		},
		Type:     messages.TypeSignal,
		Priority: priority,
		Scope:    coreWakeScope(req.Scope),
		Payload: messages.LoopNotifyPayload{
			Kind:            kind,
			Concern:         req.Concern,
			SuggestedAction: req.SuggestedAction,
			Context:         req.Context,
			ForceSupervisor: req.ForceSupervisor,
			Events:          cloneLoopEvents(req.Events),
		},
	}
}

func coreWakeScope(extra []string) []string {
	scope := []string{CoreAttentionScope}
	seen := map[string]struct{}{CoreAttentionScope: {}}
	for _, raw := range extra {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		scope = append(scope, s)
	}
	return scope
}

func cloneLoopEvents(in []messages.LoopEventPayload) []messages.LoopEventPayload {
	if len(in) == 0 {
		return nil
	}
	out := make([]messages.LoopEventPayload, len(in))
	copy(out, in)
	return out
}

// ResolveCoreAttentionTarget chooses the live loop that should receive a
// core-attention wake. Explicit metadata wins; otherwise the newest active
// owner channel is the fallback.
func ResolveCoreAttentionTarget(statuses []Status) (CoreAttentionTarget, error) {
	if len(statuses) == 0 {
		return CoreAttentionTarget{}, fmt.Errorf("core attention target unavailable: no live loops are registered")
	}
	if target, ok := newestCoreAttentionTarget(statuses, func(st Status) bool {
		return metadataFlag(st.Config.Metadata, "core_attention_target") ||
			metadataEquals(st.Config.Metadata, "attention_role", "core") ||
			metadataEquals(st.Config.Metadata, "role", "core")
	}, "metadata_core_attention_target"); ok {
		return target, nil
	}
	if target, ok := newestCoreAttentionTarget(statuses, func(st Status) bool {
		return metadataEquals(st.Config.Metadata, "category", "channel") && metadataFlag(st.Config.Metadata, "is_owner")
	}, "recent_owner_channel"); ok {
		return target, nil
	}
	return CoreAttentionTarget{}, fmt.Errorf("core attention target unavailable: no loop has metadata core_attention_target=true and no active owner channel loop was found")
}

func newestCoreAttentionTarget(statuses []Status, accept func(Status) bool, reason string) (CoreAttentionTarget, bool) {
	matches := make([]Status, 0, len(statuses))
	for _, st := range statuses {
		if st.ID == "" || st.Name == "" || !accept(st) {
			continue
		}
		matches = append(matches, st)
	}
	if len(matches) == 0 {
		return CoreAttentionTarget{}, false
	}
	sort.SliceStable(matches, func(i, j int) bool {
		iActive := loopStatusLastActive(matches[i])
		jActive := loopStatusLastActive(matches[j])
		if iActive.Equal(jActive) {
			return matches[i].ID < matches[j].ID
		}
		return iActive.After(jActive)
	})
	st := matches[0]
	return CoreAttentionTarget{
		LoopID:     st.ID,
		LoopName:   st.Name,
		Reason:     reason,
		LastActive: optionalTime(loopStatusLastActive(st)),
	}, true
}

func optionalTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func loopStatusLastActive(st Status) time.Time {
	if !st.LastWakeAt.IsZero() {
		return st.LastWakeAt
	}
	if !st.StartedAt.IsZero() {
		return st.StartedAt
	}
	return time.Time{}
}

func metadataFlag(meta map[string]string, key string) bool {
	switch strings.ToLower(strings.TrimSpace(meta[key])) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	default:
		return false
	}
}

func metadataEquals(meta map[string]string, key, want string) bool {
	return strings.EqualFold(strings.TrimSpace(meta[key]), want)
}
