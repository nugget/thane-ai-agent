package router

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

// readinessTestConfig models the prod shape that motivated readiness
// scoring: a free local runner on one resource and a paid cloud model,
// with the caller applying the summarizer's local-first factors.
func readinessTestConfig() Config {
	return Config{
		DefaultModel: "gpt-oss:120b",
		LocalFirst:   true,
		Models: []Model{
			{
				Name:          "gpt-oss:120b",
				UpstreamModel: "gpt-oss:120b",
				Provider:      "ollama",
				ResourceID:    "spark",
				Server:        "spark",
				SupportsTools: true,
				ContextWindow: 131072,
				Speed:         6,
				Quality:       8,
				CostTier:      0,
			},
			{
				Name:          "claude-sonnet-4-6",
				UpstreamModel: "claude-sonnet-4-6",
				Provider:      "anthropic",
				ResourceID:    "anthropic",
				SupportsTools: true,
				ContextWindow: 1000000,
				Speed:         7,
				Quality:       8,
				CostTier:      2,
			},
		},
		MaxAuditLog: 10,
	}
}

func summarizerRequest() Request {
	return Request{
		Query:    "session metadata generation",
		Priority: PriorityBackground,
		RoutingFactors: map[string]string{
			FactorMission:      "background",
			FactorLocalOnly:    "true",
			FactorQualityFloor: "7",
		},
	}
}

func TestUnreachableResourceLosesToPaidModelUnderLocalOnly(t *testing.T) {
	t.Parallel()

	r := NewRouter(slog.Default(), readinessTestConfig())
	r.SetResourceReadiness(func(resourceID string) bool {
		return resourceID != "spark"
	})

	model, decision := r.Route(context.Background(), summarizerRequest())
	if model != "claude-sonnet-4-6" {
		t.Fatalf("Route() with spark down selected %q, want claude-sonnet-4-6 (scores: %v)",
			model, decision.Scores)
	}
	if !slices.Contains(decision.RulesMatched, "resource_unreachable_gpt-oss:120b") {
		t.Fatalf("RulesMatched missing resource_unreachable marker: %v", decision.RulesMatched)
	}
}

func TestReadyResourceKeepsLocalPreference(t *testing.T) {
	t.Parallel()

	r := NewRouter(slog.Default(), readinessTestConfig())
	r.SetResourceReadiness(func(string) bool { return true })

	model, decision := r.Route(context.Background(), summarizerRequest())
	if model != "gpt-oss:120b" {
		t.Fatalf("Route() with all resources up selected %q, want gpt-oss:120b (scores: %v)",
			model, decision.Scores)
	}
	for _, rule := range decision.RulesMatched {
		if strings.HasPrefix(rule, "resource_unreachable_") {
			t.Fatalf("unexpected unreachable marker with all resources ready: %v", decision.RulesMatched)
		}
	}
}

func TestNilReadinessFuncIsNeutral(t *testing.T) {
	t.Parallel()

	r := NewRouter(slog.Default(), readinessTestConfig())

	model, _ := r.Route(context.Background(), summarizerRequest())
	if model != "gpt-oss:120b" {
		t.Fatalf("Route() without readiness func selected %q, want gpt-oss:120b", model)
	}
}

func TestAllResourcesUnreachableStillSelectsAModel(t *testing.T) {
	t.Parallel()

	r := NewRouter(slog.Default(), readinessTestConfig())
	r.SetResourceReadiness(func(string) bool { return false })

	model, decision := r.Route(context.Background(), summarizerRequest())
	if model == "" {
		t.Fatal("Route() with all resources down returned empty model")
	}
	if decision.NoEligible {
		t.Fatal("readiness must stay a soft penalty, not an eligibility filter")
	}
}

func TestConnectionFailureCoolsResourceAndSurfacesReason(t *testing.T) {
	t.Parallel()

	r := NewRouter(slog.Default(), readinessTestConfig())

	model, decision := r.Route(context.Background(), summarizerRequest())
	if model != "gpt-oss:120b" {
		t.Fatalf("Route() selected %q, want gpt-oss:120b", model)
	}

	r.RecordFailure(decision.RequestID, 12, 0, CooldownReasonConnection)

	if until := r.resourceCooldownDeadline("spark"); until.IsZero() {
		t.Fatal("connection-class failure did not cool the resource")
	}
	health := r.GetStats().ResourceHealth["spark"]
	if health.CooldownReason != CooldownReasonConnection {
		t.Fatalf("CooldownReason = %q, want %q", health.CooldownReason, CooldownReasonConnection)
	}

	// A cooled local resource must also lose to the paid alternative —
	// the -300 cooldown penalty has to dominate local-first bonuses plus
	// the -200 local_only paid penalty in this profile.
	nextModel, nextDecision := r.Route(context.Background(), summarizerRequest())
	if nextModel != "claude-sonnet-4-6" {
		t.Fatalf("Route() after cooldown selected %q, want claude-sonnet-4-6 (scores: %v)",
			nextModel, nextDecision.Scores)
	}
}
