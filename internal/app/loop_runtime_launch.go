package app

import (
	"context"
	"fmt"

	looppkg "github.com/nugget/thane-ai-agent/internal/loop"
)

func (a *App) launchLoop(ctx context.Context, launch looppkg.Launch) (looppkg.LaunchResult, error) {
	if a == nil || a.loopRegistry == nil || a.loop == nil {
		return looppkg.LaunchResult{}, fmt.Errorf("loop launch is not configured")
	}
	runner := &loopAdapter{agentLoop: a.loop, router: a.rtr, capSurface: a.capSurface}
	var completionSink looppkg.CompletionSink
	if dispatcher := a.ensureLoopCompletionDispatcher(); dispatcher != nil {
		completionSink = dispatcher.Deliver
	}
	var outputSink looppkg.OutputSink
	if outputs := a.ensureLoopOutputDispatcher(); outputs != nil {
		outputSink = outputs.Deliver
	}
	return a.loopRegistry.Launch(ctx, launch, looppkg.Deps{
		Runner:         runner,
		CompletionSink: completionSink,
		OutputSink:     outputSink,
		Logger:         a.logger,
		EventBus:       a.eventBus,
	})
}
