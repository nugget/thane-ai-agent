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
// loop exactly once per edge — the push half of #1472, beside the
// annunciator row's pull half. Edges flow through one buffered queue
// drained by a single worker: the provider invokes the hook under its
// own lock (edge order preserved), the hook only enqueues (the request
// path never blocks), and the worker delivers wakes in order — a
// per-edge goroutine could deliver a recovery before the block it
// recovers from and leave core acting on a stale story.
func (a *App) wireProviderBillingAttention() {
	if a == nil || a.modelRuntime == nil || a.messageBus == nil || a.loopRegistry == nil {
		return
	}
	type billingEdge struct {
		blocked bool
		detail  string
	}
	edges := make(chan billingEdge, 8)
	registry, bus, logger := a.loopRegistry, a.messageBus, a.logger

	a.deferWorker("provider-billing-attention", func(ctx context.Context) error {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case e := <-edges:
					wakeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
					wake := providerBillingWake("anthropic", e.blocked, e.detail)
					if _, err := looppkg.WakeCoreLoop(wakeCtx, registry, bus, wake); err != nil {
						logger.Warn("provider billing attention wake failed",
							"provider", "anthropic", "blocked", e.blocked, "error", err)
					}
					cancel()
				}
			}
		}()
		return nil
	})

	a.modelRuntime.SetAnthropicBillingHook(func(blocked bool, detail string) {
		select {
		case edges <- billingEdge{blocked: blocked, detail: detail}:
		default:
			// Never block the provider's request path. A full queue
			// means eight undelivered edges — the health lamp still
			// carries the standing truth, so losing the wake degrades
			// to pull-only visibility, loudly.
			logger.Warn("provider billing edge dropped; attention queue full",
				"provider", "anthropic", "blocked", blocked)
		}
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
