package app

import (
	"context"
	"fmt"
	"strings"

	looppkg "github.com/nugget/thane-ai-agent/internal/runtime/loop"
)

// loopCompletionPlan is the app-layer delivery plan for a detached loop
// completion. It is intentionally separate from loop.CompletionDelivery so
// future routing policy can revise targets without mutating loop output.
type loopCompletionPlan struct {
	Mode           looppkg.Completion               `yaml:"mode,omitempty" json:"mode,omitempty"`
	ConversationID string                           `yaml:"conversation_id,omitempty" json:"conversation_id,omitempty"`
	Channel        *looppkg.CompletionChannelTarget `yaml:"channel,omitempty" json:"channel,omitempty"`
	LoopID         string                           `yaml:"loop_id,omitempty" json:"loop_id,omitempty"`
	LoopName       string                           `yaml:"loop_name,omitempty" json:"loop_name,omitempty"`
	Content        string                           `yaml:"content,omitempty" json:"content,omitempty"`
	Response       *looppkg.Response                `yaml:"response,omitempty" json:"response,omitempty"`
	Status         *looppkg.Status                  `yaml:"status,omitempty" json:"status,omitempty"`
}

// loopCompletionPresentation is the app-layer presentation payload after
// delivery planning. Future formatting or model-mediated rewriting should
// happen between plan creation and this presented form.
type loopCompletionPresentation struct {
	Mode           looppkg.Completion               `yaml:"mode,omitempty" json:"mode,omitempty"`
	ConversationID string                           `yaml:"conversation_id,omitempty" json:"conversation_id,omitempty"`
	Channel        *looppkg.CompletionChannelTarget `yaml:"channel,omitempty" json:"channel,omitempty"`
	Content        string                           `yaml:"content,omitempty" json:"content,omitempty"`
}

type channelMessageSenderFunc func(ctx context.Context, recipient, message string) error

type loopChannelDeliveryRouter struct {
	conversations *conversationSystemInjector
	signal        channelMessageSenderFunc
}

func newLoopChannelDeliveryRouter(conversations *conversationSystemInjector) *loopChannelDeliveryRouter {
	return &loopChannelDeliveryRouter{conversations: conversations}
}

func (r *loopChannelDeliveryRouter) ConfigureSignalSender(sender channelMessageSenderFunc) {
	if r == nil {
		return
	}
	r.signal = sender
}

func (r *loopChannelDeliveryRouter) Deliver(ctx context.Context, target *looppkg.CompletionChannelTarget, content string) error {
	if r == nil {
		return fmt.Errorf("loop completion channel delivery is not configured")
	}
	if target == nil {
		return fmt.Errorf("loop completion channel target is required")
	}
	switch target.Channel {
	case "signal":
		if r.signal == nil {
			return fmt.Errorf("signal completion delivery is not configured")
		}
		if target.Recipient == "" {
			return fmt.Errorf("signal completion delivery requires recipient")
		}
		if err := r.signal(ctx, target.Recipient, content); err != nil {
			return err
		}
		if r.conversations != nil && target.ConversationID != "" {
			return r.conversations.InjectAssistantMessage(target.ConversationID, content)
		}
		return nil
	case "owu":
		if r.conversations == nil {
			return fmt.Errorf("owu completion delivery is not configured")
		}
		if target.ConversationID == "" {
			return fmt.Errorf("owu completion delivery requires conversation_id")
		}
		return r.conversations.InjectAssistantMessage(target.ConversationID, content)
	default:
		return fmt.Errorf("unsupported loop completion channel %q", target.Channel)
	}
}

// detachedLoopCompletionDispatcher is the shared app-side entry point for
// loop completion delivery. It plans, presents, and dispatches detached
// loop results without teaching the loop package about conversations,
// Signal, or future waiter/channel routing.
type detachedLoopCompletionDispatcher struct {
	conversations *conversationSystemInjector
	channels      *loopChannelDeliveryRouter
}

func newDetachedLoopCompletionDispatcher(conversations *conversationSystemInjector, channels *loopChannelDeliveryRouter) *detachedLoopCompletionDispatcher {
	return &detachedLoopCompletionDispatcher{conversations: conversations, channels: channels}
}

func (a *App) ensureLoopCompletionDispatcher() *detachedLoopCompletionDispatcher {
	if a == nil {
		return nil
	}
	if a.loopCompletionDelivery != nil {
		return a.loopCompletionDelivery
	}
	injector := &conversationSystemInjector{mem: a.mem, archiver: a.archiveAdapter}
	channels := newLoopChannelDeliveryRouter(injector)
	a.loopCompletionDelivery = newDetachedLoopCompletionDispatcher(injector, channels)
	if a.signalClient != nil {
		a.loopCompletionDelivery.ConfigureSignalSender(func(ctx context.Context, recipient, message string) error {
			return (&signalChannelSender{client: a.signalClient}).SendMessage(ctx, recipient, message)
		})
	}
	return a.loopCompletionDelivery
}

func (d *detachedLoopCompletionDispatcher) ConfigureSignalSender(sender channelMessageSenderFunc) {
	if d == nil || d.channels == nil {
		return
	}
	d.channels.ConfigureSignalSender(sender)
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
		Channel:        looppkg.CloneCompletionChannelTarget(delivery.Channel),
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
		Channel:        looppkg.CloneCompletionChannelTarget(plan.Channel),
		Content:        plan.Content,
	}, nil
}

func (d *detachedLoopCompletionDispatcher) dispatch(ctx context.Context, presented loopCompletionPresentation) error {
	switch presented.Mode {
	case "", looppkg.CompletionNone:
		return nil
	case looppkg.CompletionConversation:
		if d.conversations == nil {
			return fmt.Errorf("conversation completion delivery is not configured")
		}
		if strings.TrimSpace(presented.ConversationID) == "" {
			return fmt.Errorf("conversation completion delivery requires a non-empty conversation ID")
		}
		return d.conversations.InjectSystemMessage(presented.ConversationID, presented.Content)
	case looppkg.CompletionChannel:
		if d.channels == nil {
			return fmt.Errorf("channel completion delivery is not configured")
		}
		return d.channels.Deliver(ctx, presented.Channel, presented.Content)
	default:
		return fmt.Errorf("unsupported loop completion mode %q", presented.Mode)
	}
}
