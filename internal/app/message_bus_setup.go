package app

import (
	"context"
	"log/slog"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
	"github.com/nugget/thane-ai-agent/internal/messages"
)

func (a *App) initMessageInfrastructure(logger *slog.Logger) {
	if a == nil || a.messageBus != nil {
		return
	}
	a.messageBus = messages.NewBus(logger)
}

func (a *App) initMessageBus() {
	if a == nil || a.messageBus == nil || a.loopRegistry == nil {
		return
	}
	route := &messages.LoopHandler{
		ByID: func(ctx context.Context, id string, env messages.Envelope) (messages.DeliveryResult, error) {
			receipt, err := a.loopRegistry.NotifyLoop(ctx, id, env)
			if err != nil {
				return messages.DeliveryResult{}, err
			}
			return loopNotifyDeliveryResult(receipt), nil
		},
		ByName: func(ctx context.Context, name string, env messages.Envelope) (messages.DeliveryResult, error) {
			receipt, err := a.loopRegistry.NotifyLoopByName(ctx, name, env)
			if err != nil {
				return messages.DeliveryResult{}, err
			}
			return loopNotifyDeliveryResult(receipt), nil
		},
	}
	a.messageBus.RegisterRoute(messages.DestinationLoop, route.Deliver)
}

func loopNotifyDeliveryResult(receipt looppkg.NotifyReceipt) messages.DeliveryResult {
	status := messages.DeliveryDelivered
	if receipt.QueuedForNextWake {
		status = messages.DeliveryQueued
	}
	return messages.DeliveryResult{
		Route:   "loop",
		Status:  status,
		Details: receipt,
	}
}
