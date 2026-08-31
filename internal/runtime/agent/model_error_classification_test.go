package agent

import (
	"fmt"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// TestIsUserFixableModelError pins the classification that decides
// whether failover is worth attempting. The typed branch exists because
// the string fallback only matched the bare "API error " prefix, which
// Anthropic's "anthropic API error 400: …" never did — so Anthropic
// 4xxs (billing 400s included) wrongly entered failover on every
// iteration and produced the failover-also-failed pair the 2026-08-31
// audit counted 46 of.
func TestIsUserFixableModelError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			// Billing is operator-fixable, not user-fixable: failover
			// stays available because the router default may be another
			// provider entirely, and against the same blocked provider
			// the attempt fails fast without an HTTP round-trip.
			"typed anthropic billing 400 keeps failover available",
			&llm.APIError{Provider: "anthropic", StatusCode: 400, Body: "credit balance too low", Billing: true},
			false,
		},
		{
			"typed anthropic 429 is not user-fixable",
			&llm.APIError{Provider: "anthropic", StatusCode: 429, Body: "overloaded"},
			false,
		},
		{
			"typed 408 is not user-fixable",
			&llm.APIError{Provider: "anthropic", StatusCode: 408, Body: "request timeout"},
			false,
		},
		{
			"typed 500 is not user-fixable",
			&llm.APIError{Provider: "anthropic", StatusCode: 500, Body: "internal"},
			false,
		},
		{
			"wrapped typed error still classifies",
			fmt.Errorf("loop LLM call: %w", &llm.APIError{Provider: "anthropic", StatusCode: 404, Body: "model not found"}),
			true,
		},
		{"legacy bare-prefix 400", fmt.Errorf("API error 400: bad request"), true},
		{"legacy bare-prefix 503", fmt.Errorf("API error 503: unavailable"), false},
		{"legacy anthropic string no longer slips through", fmt.Errorf("anthropic API error 400: bad request"), false},
		{"unrelated error", fmt.Errorf("connection refused"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isUserFixableModelError(tt.err); got != tt.want {
				t.Errorf("isUserFixableModelError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
