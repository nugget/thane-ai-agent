package agent

import (
	"context"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/memory"
)

type sessionOriginContextKey struct{}

// SessionOrigin is the normalized source identity for one agent run.
type SessionOrigin struct {
	Source      string `json:"source,omitempty"`
	Channel     string `json:"channel,omitempty"`
	ContactID   string `json:"contact_id,omitempty"`
	ContactName string `json:"contact_name,omitempty"`
	TrustZone   string `json:"trust_zone,omitempty"`
	Address     string `json:"address,omitempty"`
	IsOwner     bool   `json:"is_owner,omitempty"`
	HAUserID    string `json:"ha_user_id,omitempty"`
	Person      string `json:"person,omitempty"`
	PersonID    string `json:"person_id,omitempty"`
	DeviceID    string `json:"device_id,omitempty"`
	PipelineID  string `json:"pipeline_id,omitempty"`
}

// SessionOriginPolicyResult captures the origin-derived tags and
// context refs applied to a run.
type SessionOriginPolicyResult struct {
	Origin      SessionOrigin              `json:"origin"`
	Applied     []SessionOriginAppliedRule `json:"applied_rules,omitempty"`
	Tags        []string                   `json:"tags,omitempty"`
	ContextRefs []string                   `json:"context_refs,omitempty"`
}

// SessionOriginAppliedRule is the model-facing summary of one matching
// origin policy rule.
type SessionOriginAppliedRule struct {
	Name        string   `json:"name,omitempty"`
	Source      string   `json:"source,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	ContextRefs []string `json:"context_refs,omitempty"`
}

func newSessionOrigin(hints map[string]string, binding *memory.ChannelBinding) SessionOrigin {
	origin := SessionOrigin{
		Source:     hintValue(hints, "source"),
		Channel:    hintValue(hints, "channel"),
		Address:    firstNonEmpty(hintValue(hints, "address"), hintValue(hints, "sender")),
		HAUserID:   hintValue(hints, "ha_user_id"),
		Person:     hintValue(hints, "person"),
		PersonID:   hintValue(hints, "person_id"),
		DeviceID:   hintValue(hints, "device_id"),
		PipelineID: hintValue(hints, "pipeline_id"),
	}
	if binding != nil {
		if origin.Channel == "" {
			origin.Channel = strings.TrimSpace(binding.Channel)
		}
		if origin.Source == "" {
			origin.Source = strings.TrimSpace(binding.Channel)
		}
		if origin.Address == "" {
			origin.Address = strings.TrimSpace(binding.Address)
		}
		origin.ContactID = strings.TrimSpace(binding.ContactID)
		origin.ContactName = strings.TrimSpace(binding.ContactName)
		origin.TrustZone = strings.TrimSpace(binding.TrustZone)
		origin.IsOwner = binding.IsOwner
	}
	if origin.Channel == "" {
		origin.Channel = origin.Source
	}
	if origin.Source == "" {
		origin.Source = origin.Channel
	}
	return origin
}

func hintValue(hints map[string]string, key string) string {
	if hints == nil {
		return ""
	}
	return strings.TrimSpace(hints[key])
}

func cleanUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		cleaned = append(cleaned, value)
	}
	return cleaned
}

func (r *SessionOriginPolicyResult) addApplied(applied SessionOriginAppliedRule) {
	applied.Tags = cleanUnique(applied.Tags)
	applied.ContextRefs = cleanUnique(applied.ContextRefs)
	if len(applied.Tags) == 0 && len(applied.ContextRefs) == 0 {
		return
	}
	r.Applied = append(r.Applied, applied)
	for _, tag := range applied.Tags {
		if !containsString(r.Tags, tag) {
			r.Tags = append(r.Tags, tag)
		}
	}
	for _, ref := range applied.ContextRefs {
		if !containsString(r.ContextRefs, ref) {
			r.ContextRefs = append(r.ContextRefs, ref)
		}
	}
}

func (r SessionOriginPolicyResult) empty() bool {
	return len(r.Applied) == 0
}

func (r SessionOriginPolicyResult) clone() SessionOriginPolicyResult {
	clone := SessionOriginPolicyResult{
		Origin:      r.Origin,
		Applied:     make([]SessionOriginAppliedRule, 0, len(r.Applied)),
		Tags:        append([]string(nil), r.Tags...),
		ContextRefs: append([]string(nil), r.ContextRefs...),
	}
	for _, applied := range r.Applied {
		clone.Applied = append(clone.Applied, SessionOriginAppliedRule{
			Name:        applied.Name,
			Source:      applied.Source,
			Tags:        append([]string(nil), applied.Tags...),
			ContextRefs: append([]string(nil), applied.ContextRefs...),
		})
	}
	return clone
}

func containsString(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}

func withSessionOriginPolicyResult(ctx context.Context, result SessionOriginPolicyResult) context.Context {
	if result.empty() {
		return ctx
	}
	clone := result.clone()
	return context.WithValue(ctx, sessionOriginContextKey{}, &clone)
}

func sessionOriginPolicyResultFromContext(ctx context.Context) *SessionOriginPolicyResult {
	result, ok := ctx.Value(sessionOriginContextKey{}).(*SessionOriginPolicyResult)
	if !ok || result == nil {
		return nil
	}
	clone := result.clone()
	return &clone
}
