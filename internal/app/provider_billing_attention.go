package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/nugget/thane-ai-agent/internal/channels/messages"
	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

const providerBillingTransitionKind = "provider_billing_transition"

// wireProviderBillingAttention registers the billing transition hook so
// the edge into (and out of) a billing-blocked provider wakes the core
// loop exactly once — the push half of #1472, beside the annunciator
// row's pull half. The hook runs on the provider's request path, so the
// wake happens on a detached goroutine with its own bound.
func (a *App) wireProviderBillingAttention() {
	if a == nil || a.modelRuntime == nil || a.messageBus == nil || a.loopRegistry == nil {
		return
	}
	registry, bus, logger := a.loopRegistry, a.messageBus, a.logger
	a.modelRuntime.SetAnthropicBillingHook(func(blocked bool, detail string) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			wake := providerBillingWake("anthropic", blocked, detail)
			if _, err := looppkg.WakeCoreLoop(ctx, registry, bus, wake); err != nil {
				logger.Warn("provider billing attention wake failed",
					"provider", "anthropic", "blocked", blocked, "error", err)
			}
		}()
	})
}

func providerBillingWake(provider string, blocked bool, detail string) looppkg.CoreWakeRequest {
	priority := messages.PriorityNormal
	forceSupervisor := false
	concern := fmt.Sprintf("The %s account recovered: billing cleared and requests are being served again.", provider)
	action := "Note the recovery; no direct human message is required unless something was left pending on the outage."
	title := "Provider billing recovered"
	if blocked {
		priority = messages.PriorityUrgent
		forceSupervisor = true
		concern = fmt.Sprintf("The %s account is refusing service over billing state: %s Requests fail fast until the operator adds credits — no retry or failover fixes this.",
			provider, strings.TrimSpace(detail))
		action = "Tell the operator now, through whatever channel reaches them: the account needs credits, and every cloud-model capability is down until then."
		title = "Provider billing blocked"
	}

	h := sha256.Sum256([]byte(provider + "\x00" + fmt.Sprint(blocked) + "\x00" + strings.TrimSpace(detail)))
	return looppkg.CoreWakeRequest{
		From: messages.Identity{
			Kind: messages.IdentitySystem,
			Name: "model_fleet",
		},
		Kind:            providerBillingTransitionKind,
		Concern:         concern,
		SuggestedAction: action,
		Events: []messages.LoopEventPayload{{
			Source:     "provider_billing",
			Type:       providerBillingTransitionKind,
			ID:         fmt.Sprintf("%s:%t:%x", provider, blocked, h[:8]),
			Title:      title,
			Summary:    fmt.Sprintf("provider=%s blocked=%t", provider, blocked),
			ObservedAt: time.Now(),
			Metadata: map[string]string{
				"provider": provider,
				"blocked":  fmt.Sprint(blocked),
				"detail":   strings.TrimSpace(detail),
			},
		}},
		Priority:        priority,
		Scope:           []string{"provider_billing"},
		ForceSupervisor: forceSupervisor,
	}
}
