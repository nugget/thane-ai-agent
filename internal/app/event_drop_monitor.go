package app

import (
	"context"
	"time"
)

const eventDropReportInterval = 10 * time.Second

// initEventDropMonitor reports operational-bus backpressure through the
// independent logger sinks. The bus stays non-blocking by design, but dropped
// live deliveries must not remain invisible to operators or logs_query.
func (a *App) initEventDropMonitor() {
	if a == nil || a.eventBus == nil {
		return
	}
	a.deferWorker("event-drop-monitor", func(ctx context.Context) error {
		go func() {
			ticker := time.NewTicker(eventDropReportInterval)
			defer ticker.Stop()
			var reported uint64
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					total := a.eventBus.DroppedCount()
					if total == reported {
						continue
					}
					a.logger.Warn("operational event bus dropped subscriber deliveries",
						"component", "event_bus",
						"dropped_since_last", total-reported,
						"dropped_total", total,
					)
					// The warning itself is promoted onto this bus and may be
					// dropped by the same slow subscriber. Count that attempt
					// as reported so the monitor does not create a self-
					// sustaining warning every interval.
					reported = a.eventBus.DroppedCount()
				}
			}
		}()
		return nil
	})
}
