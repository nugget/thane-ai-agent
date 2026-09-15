package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

func TestAccountingClientSharesBudgetWithDirectRecovery(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%t", streaming), func(t *testing.T) {
			loop, _, _ := newAccountingTestLoop(t)
			ctx := context.Background()
			accounting := &modelCallAccounting{loop: loop, request: &Request{MaxOutputTokens: 1000}, observerContext: ctx}
			var ceilings []int
			client := &accountingClient{accounting: accounting, Client: accountingTestClient{
				call: func(ctx context.Context, model string, _ []map[string]any) (*llm.ChatResponse, error) {
					ceilings = append(ceilings, llm.MaxOutputTokensFromContext(ctx))
					return &llm.ChatResponse{Model: model, OutputTokens: 600}, errors.New("stream disconnected")
				},
			}}
			_, err := client.ChatStream(ctx, "claude-primary", nil, nil, nil)
			if err == nil || errors.Is(err, llm.ErrOutputBudgetExhausted) {
				t.Fatalf("first attempt should leave 400 tokens: %v", err)
			}
			direct := accountRecoveryClient(client, accountingTestClient{
				call: func(ctx context.Context, model string, _ []map[string]any) (*llm.ChatResponse, error) {
					ceilings = append(ceilings, llm.MaxOutputTokensFromContext(ctx))
					if model != "loaded-instance" {
						t.Errorf("direct model = %q, want loaded-instance", model)
					}
					return &llm.ChatResponse{Model: model, OutputTokens: 400}, nil
				},
			}, "claude-recovery")
			// A direct loaded-instance call can have a fresh context and a
			// different wire name; both still belong to the original run.
			detached := llm.WithMaxOutputTokens(context.Background(), 5000)
			call := func() (*llm.ChatResponse, error) {
				if streaming {
					return direct.ChatStream(detached, "loaded-instance", nil, nil, nil)
				}
				return direct.Chat(detached, "loaded-instance", nil, nil)
			}
			if _, err := call(); err != nil {
				t.Fatalf("direct recovery: %v", err)
			}
			if response, err := call(); response != nil || !errors.Is(err, llm.ErrOutputBudgetExhausted) {
				t.Fatalf("spent budget started another direct call: response=%+v error=%v", response, err)
			}
			if !slices.Equal(ceilings, []int{1000, 400}) {
				t.Fatalf("provider ceilings = %v, want [1000 400]", ceilings)
			}
			if len(accounting.calls) != 2 || accounting.calls[0].OutputTokens != 600 || accounting.calls[1].OutputTokens != 400 || accounting.calls[1].Model != "claude-recovery" {
				t.Fatalf("direct recovery lost or duplicated deployment usage: %+v", accounting.calls)
			}
		})
	}
}

func TestOutputBudgetBoundsFailover(t *testing.T) {
	for _, spent := range []int{600, 1000} {
		t.Run(fmt.Sprintf("spent=%d", spent), func(t *testing.T) {
			loop, _, _ := newAccountingTestLoop(t)
			loop.model = "claude-recovery"
			ctx := context.Background()
			req := &Request{MaxOutputTokens: 1000}
			accounting := &modelCallAccounting{loop: loop, request: req, observerContext: ctx}
			var ceilings []int
			client := &accountingClient{accounting: accounting, Client: accountingTestClient{
				call: func(ctx context.Context, model string, _ []map[string]any) (*llm.ChatResponse, error) {
					ceilings = append(ceilings, llm.MaxOutputTokensFromContext(ctx))
					if model == "claude-primary" {
						return &llm.ChatResponse{Model: model, OutputTokens: spent}, errors.New("provider unavailable")
					}
					if model != "claude-recovery" {
						t.Errorf("failover model = %q, want claude-recovery", model)
					}
					return &llm.ChatResponse{Model: model, OutputTokens: 300}, nil
				},
			}}
			_, initialErr := client.ChatStream(ctx, "claude-primary", nil, nil, nil)
			if initialErr == nil {
				t.Fatal("initial attempt unexpectedly succeeded")
			}
			timeoutRecovered := false
			handler := loop.buildLLMErrorHandler(ctx, nil, client, req, &timeoutRecovered)
			_, model, err := handler(ctx, initialErr, "claude-primary", nil, nil, nil)
			want := []int{1000}
			if spent < 1000 {
				want = append(want, 400)
				if err != nil || model != "claude-recovery" {
					t.Fatalf("failover model=%q error=%v", model, err)
				}
			} else if !errors.Is(err, llm.ErrOutputBudgetExhausted) {
				t.Fatalf("exhausted request did not stop failover: %v", err)
			}
			if !slices.Equal(ceilings, want) || len(accounting.calls) != len(want) {
				t.Fatalf("failover ceilings=%v records=%d, want %v and %d", ceilings, len(accounting.calls), want, len(want))
			}
		})
	}
}
