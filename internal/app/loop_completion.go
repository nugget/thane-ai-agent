package app

import (
	"context"
	"fmt"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
)

// loopCompletionPlan is the app-layer delivery plan for a detached loop
// completion. It is intentionally separate from loop.CompletionDelivery so
// future routing policy can revise targets without mutating loop output.
type loopCompletionPlan struct {
	Mode           looppkg.Completion
	ConversationID string
	LoopID         string
	LoopName       string
	Content        string
	Response       *looppkg.Response
	Status         *looppkg.Status
}

// loopCompletionPresentation is the app-layer presentation payload after
// delivery planning. Future formatting or model-mediated rewriting should
// happen between plan creation and this presented form.
type loopCompletionPresentation struct {
	Mode           looppkg.Completion
	ConversationID string
	Content        string
}

// detachedLoopCompletionDispatcher is the shared app-side entry point for
// loop completion delivery. Today it only targets conversations, but the
// plan/presentation split gives future channel or waiter delivery a clear
// boundary without changing the loop package contract.
type detachedLoopCompletionDispatcher struct {
	conversations *conversationSystemInjector
}

func newDetachedLoopCompletionDispatcher(conversations *conversationSystemInjector) *detachedLoopCompletionDispatcher {
	return &detachedLoopCompletionDispatcher{conversations: conversations}
}

func (d *detachedLoopCompletionDispatcher) Deliver(ctx context.Context, delivery looppkg.CompletionDelivery) error {
	if d == nil {
		return nil
	}
	plan := d.plan(delivery)
	presented, err := d.present(ctx, plan)
	if err != nil {
		return err
	}
	return d.dispatch(ctx, presented)
}

func (d *detachedLoopCompletionDispatcher) plan(delivery looppkg.CompletionDelivery) loopCompletionPlan {
	return loopCompletionPlan{
		Mode:           delivery.Mode,
		ConversationID: delivery.ConversationID,
		LoopID:         delivery.LoopID,
		LoopName:       delivery.LoopName,
		Content:        delivery.Content,
		Response:       delivery.Response,
		Status:         delivery.Status,
	}
}

func (d *detachedLoopCompletionDispatcher) present(_ context.Context, plan loopCompletionPlan) (loopCompletionPresentation, error) {
	return loopCompletionPresentation{
		Mode:           plan.Mode,
		ConversationID: plan.ConversationID,
		Content:        plan.Content,
	}, nil
}

func (d *detachedLoopCompletionDispatcher) dispatch(_ context.Context, presented loopCompletionPresentation) error {
	switch presented.Mode {
	case "", looppkg.CompletionNone:
		return nil
	case looppkg.CompletionConversation:
		if d.conversations == nil {
			return nil
		}
		return d.conversations.InjectSystemMessage(presented.ConversationID, presented.Content)
	default:
		return fmt.Errorf("unsupported loop completion mode %q", presented.Mode)
	}
}
