package app

import (
	"context"
	"log/slog"

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
		ByID: func(ctx context.Context, id string, env messages.Envelope) (any, error) {
			return a.loopRegistry.SignalLoop(ctx, id, env)
		},
		ByName: func(ctx context.Context, name string, env messages.Envelope) (any, error) {
			return a.loopRegistry.SignalLoopByName(ctx, name, env)
		},
	}
	a.messageBus.RegisterRoute(messages.DestinationLoop, route.Deliver)
}
