package app

import (
	"context"
	"sync"
	"time"

	"github.com/nugget/thane-ai-agent/internal/logging"
	"github.com/nugget/thane-ai-agent/internal/messages"
)

const (
	datasetEventSubscriptionBuffer = 1024
	// envelopeAuditBuffer sizes the channel between Bus.Send and the
	// envelope-audit dataset worker. Bus.Send must not block on disk
	// I/O, so when this buffer is full we drop the record and warn.
	envelopeAuditBuffer = 1024
)

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

	// Bus.Send invokes audit funcs synchronously on the caller's
	// goroutine. Disk I/O inside an audit func would add latency to
	// every envelope send and could block shutdown if the filesystem
	// stalls. Decouple the work via a bounded channel: the audit hook
	// marshals the record into a DatasetRecord and enqueues it; a
	// dedicated worker goroutine drains the channel and writes to disk.
	// If the channel is full (sustained write backpressure), drop the
	// record and warn — losing forensic completeness is preferable to
	// stalling message delivery.
	sink := &envelopeAuditSink{
		ch:     make(chan logging.DatasetRecord, envelopeAuditBuffer),
		done:   make(chan struct{}),
		writer: writer,
		logger: logger,
	}
	go sink.run()

	a.messageBus.AddAuditFunc(func(_ context.Context, env messages.Envelope, result *messages.DeliveryResult, deliveryErr error) {
		record := logging.DatasetRecordFromEnvelopeAudit(time.Now(), env, result, deliveryErr)
		sink.enqueue(env.ID, record)
	})

	a.onClose("envelope-dataset-sink", sink.close)
}

// envelopeAuditSink buffers envelope audit records off the Bus.Send
// hot path and flushes them to the datasets writer from a dedicated
// goroutine. Writes are drop-tolerant under sustained backpressure so
// envelope delivery is never gated on disk I/O.
type envelopeAuditSink struct {
	ch     chan logging.DatasetRecord
	done   chan struct{}
	writer *logging.DatasetWriter
	logger interface {
		Warn(msg string, args ...any)
	}

	mu     sync.RWMutex
	closed bool
}

func (s *envelopeAuditSink) enqueue(envelopeID string, record logging.DatasetRecord) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- record:
	default:
		s.logger.Warn("envelope dataset sink backpressure, dropped record",
			"envelope_id", envelopeID,
			"dataset", record.Dataset,
		)
	}
}

func (s *envelopeAuditSink) run() {
	defer close(s.done)
	for record := range s.ch {
		if err := s.writer.WriteRecord(record); err != nil {
			s.logger.Warn("failed to write envelope dataset record",
				"dataset", record.Dataset,
				"error", err,
			)
		}
	}
}

func (s *envelopeAuditSink) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.ch)
	s.mu.Unlock()
	<-s.done
}
