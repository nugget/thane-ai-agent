package app

import (
	"context"
	"time"

	"github.com/nugget/thane-ai-agent/internal/logging"
	"github.com/nugget/thane-ai-agent/internal/messages"
)

const datasetEventSubscriptionBuffer = 1024

// initDatasetSinks wires direct writers for datasets that do not flow through
// the generic slog handler path.
func (a *App) initDatasetSinks() {
	if a == nil || a.datasetWriter == nil {
		return
	}
	a.initOperationalDatasetSink()
	a.initEnvelopeDatasetSink()
}

func (a *App) initOperationalDatasetSink() {
	if a == nil || a.eventBus == nil || a.datasetWriter == nil {
		return
	}
	if !a.cfg.Logging.DatasetEnabled(logging.DatasetLoops) &&
		!a.cfg.Logging.DatasetEnabled(logging.DatasetDelegates) {
		return
	}

	ch := a.eventBus.Subscribe(datasetEventSubscriptionBuffer)
	done := make(chan struct{})
	writer := a.datasetWriter
	logger := a.logger.With("component", "dataset_sink")
	cfg := a.cfg.Logging

	go func() {
		defer close(done)
		for event := range ch {
			record, ok := logging.DatasetRecordFromOperationalEvent(event)
			if !ok || !cfg.DatasetEnabled(record.Dataset) {
				continue
			}
			if err := writer.WriteRecord(record); err != nil {
				logger.Warn("failed to write operational dataset record",
					"dataset", record.Dataset,
					"kind", record.Kind,
					"error", err,
				)
			}
		}
	}()

	a.onClose("operational-dataset-sink", func() {
		a.eventBus.Unsubscribe(ch)
		<-done
	})
}

func (a *App) initEnvelopeDatasetSink() {
	if a == nil || a.messageBus == nil || a.datasetWriter == nil {
		return
	}
	if !a.cfg.Logging.DatasetEnabled(logging.DatasetEnvelopes) {
		return
	}

	writer := a.datasetWriter
	logger := a.logger.With("component", "dataset_sink")
	a.messageBus.AddAuditFunc(func(_ context.Context, env messages.Envelope, result *messages.DeliveryResult, deliveryErr error) {
		record := logging.DatasetRecordFromEnvelopeAudit(time.Now(), env, result, deliveryErr)
		if err := writer.WriteRecord(record); err != nil {
			logger.Warn("failed to write envelope dataset record",
				"dataset", record.Dataset,
				"envelope_id", env.ID,
				"error", err,
			)
		}
	})
}
