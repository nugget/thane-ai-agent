package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

// thinkingOffFix names the fix for a shrink model that reasons before it
// answers, which spends the output bound thinking and never answers.
const thinkingOffFix = "a shrink model must answer without reasoning first, so turn its thinking off (chat_template_kwargs on its resource)"

const facetShrinkPrompt = "Shorten the text the user sends to at most %d characters. Keep every fact, name, number, and date, and keep the text's line structure; drop filler words and add nothing new. Reply with only the shortened text: no quotes, preamble, or explanation."

// facetShrinker builds the faceted-write shrink step from
// models.facet_shrink_model, or nil when that is empty.
func (a *App) facetShrinker() *documentfacets.Shrinker {
	model := a.cfg.Models.FacetShrinkModel
	if model == "" || a.llmClient == nil {
		return nil
	}
	client := a.auxiliaryUsageClient("facet_shrink")
	a.logger.Info("facet shrink enabled", "model", model)
	return &documentfacets.Shrinker{
		Model:  model,
		Logger: a.logger.With("component", "facet_shrink"),
		Shrink: func(ctx context.Context, text string, limit int) (string, error) {
			return shrinkFacetText(ctx, client, model, text, limit)
		},
	}
}

// shrinkFacetText makes one tool-free call asking model to shorten text,
// bounded at two output tokens per allowed character. A reply that reached
// that bound is refused, by its stop reason or, for a provider that reports
// none (Ollama), by the tokens it spent, and its text comes back with the
// refusal so the shrink log keeps it. A model that reasoned instead of
// answering is refused with [thinkingOffFix], because nothing else shows
// it: the writer's refusal stands just as when the text could not be
// shortened. That holds whether it reached the bound or, on a slower
// model, ran out the call's deadline first, which a model that answers
// directly but too slowly for a long projection also does. The client
// carries no sampling settings, so the deployment's own defaults,
// temperature included, apply.
func shrinkFacetText(ctx context.Context, client llm.Client, model, text string, limit int) (string, error) {
	bound := 2 * limit
	resp, err := client.Chat(llm.WithMaxOutputTokens(ctx, bound), model, []llm.Message{
		{Role: "system", Content: fmt.Sprintf(facetShrinkPrompt, limit)},
		{Role: "user", Content: text},
	}, nil)
	if errors.Is(err, llm.ErrReasoningOnly) {
		return "", fmt.Errorf("%w; %s", err, thinkingOffFix)
	}
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("%w; no answer arrived before the call's deadline: %s, and one that answers directly but slowly cannot shorten a long projection in time", err, thinkingOffFix)
	}
	if err != nil {
		return "", err
	}
	if resp.StopReason == "length" || resp.StopReason == "max_tokens" || resp.OutputTokens >= bound {
		if strings.TrimSpace(resp.Message.Content) == "" {
			return "", fmt.Errorf("the reply spent the whole %d-token output bound without answering; %s", bound, thinkingOffFix)
		}
		return resp.Message.Content, fmt.Errorf("the reply stopped at the %d-token output bound", bound)
	}
	return resp.Message.Content, nil
}
