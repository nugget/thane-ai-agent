package app

import (
	"context"
	"fmt"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
	documentfacets "github.com/nugget/thane-ai-agent/internal/state/documents/facets"
)

const facetShrinkPrompt = "Shorten the text the user sends to at most %d characters. Keep every fact, name, number, and date; drop filler words; add nothing new and no line breaks. Reply with only the shortened text: no quotes, preamble, or explanation."

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
// bounded at two output tokens per allowed character; a reply cut off at
// that bound is refused. The client carries no sampling settings, so the
// deployment's own defaults, temperature included, apply.
func shrinkFacetText(ctx context.Context, client llm.Client, model, text string, limit int) (string, error) {
	resp, err := client.Chat(llm.WithMaxOutputTokens(ctx, 2*limit), model, []llm.Message{
		{Role: "system", Content: fmt.Sprintf(facetShrinkPrompt, limit)},
		{Role: "user", Content: text},
	}, nil)
	if err != nil {
		return "", err
	}
	if resp.StopReason == "length" || resp.StopReason == "max_tokens" {
		return "", fmt.Errorf("the reply stopped at the %d-token output bound", 2*limit)
	}
	return resp.Message.Content, nil
}
