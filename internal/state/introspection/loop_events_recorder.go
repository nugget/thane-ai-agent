package introspection

import (
	"context"
	"log/slog"
	"time"

	"github.com/nugget/thane-ai-agent/internal/platform/events"
)

// persistedLoopEventKinds is the exact set of bus events the journal
// retains: lifecycle and outcome, never the chatty per-tool/per-LLM
// stream (loop_tool_start, loop_llm_response, sleep/wait transitions),
// which would multiply row volume without adding audit value — that
// granularity already lives in the log index and the archive.
var persistedLoopEventKinds = map[string]bool{
	events.KindLoopStarted:           true,
	events.KindLoopStopped:           true,
	events.KindLoopIterationStart:    true,
	events.KindLoopIterationComplete: true,
	events.KindLoopError:             true,
	events.KindLoopStateChange:       true,
	events.KindLoopMailboxArrival:    true,
	events.KindLoopMidTurnInput:      true,
}

// detailWhitelist is the flat set of payload keys the journal retains
// across all persisted kinds. Scalars only — bulk fields on the bus
// event (tools_used, effective_tools, tooling, summary) are deliberately
// dropped: the journal answers "what happened when", not "replay the
// turn". One stable whitelist, not per-kind logic, so the stored shape
// never depends on producer drift.
var detailWhitelist = map[string]bool{
	"conversation_id":    true,
	"supervisor":         true,
	"supervisor_trigger": true,
	"attempt":            true,
	"signal_envelopes":   true,
	"mailbox_items":      true,
	"model":              true,
	"finish_reason":      true,
	"request_id":         true,
	"input_tokens":       true,
	"output_tokens":      true,
	"context_window":     true,
	"elapsed_ms":         true,
	"no_op":              true,
	"midturn_merged":     true,
	"error":              true,
	"phase":              true,
	"from":               true,
	"to":                 true,
	"event_seq":          true,
	"parent_id":          true,
	"item_id":            true,
	"pending_items":      true,
	"count":              true,
}

// maxDetailStringRunes bounds any single retained string value (error
// messages are the realistic offender) so one pathological payload
// cannot bloat the journal row.
const maxDetailStringRunes = 512

const (
	recorderBufSize      = 256
	recorderFlushCount   = 64
	recorderFlushEvery   = 2 * time.Second
	recorderPruneEvery   = 24 * time.Hour
	recorderFlushTimeout = 10 * time.Second
)

// Recorder subscribes to the operational event bus and persists the
// loop lifecycle stream into the journal. It never blocks the bus: the
// bus drops deliveries to a full subscriber buffer by design (and
// counts them via DroppedCount, which the annunciator surfaces), and
// the recorder batches its writes so a burst costs one transaction.
type Recorder struct {
	journal   *Journal
	bus       *events.Bus
	retention time.Duration
	logger    *slog.Logger
}

// NewRecorder wires a recorder over journal and bus. retention <= 0
// takes DefaultLoopEventRetention.
func NewRecorder(journal *Journal, bus *events.Bus, retention time.Duration, logger *slog.Logger) *Recorder {
	if retention <= 0 {
		retention = DefaultLoopEventRetention
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Recorder{journal: journal, bus: bus, retention: retention, logger: logger}
}

// recorderFirstPruneAfter delays the first prune past the startup write
// burst: pruning fights the log indexer for the write lock at exactly
// the moment the indexer is busiest, and a failed first prune is pure
// noise — the daily tick covers the hygiene either way.
const recorderFirstPruneAfter = 5 * time.Minute

// Run consumes the bus until ctx is cancelled, flushing batches on
// size or interval and pruning the journal daily (first prune deferred
// past the startup burst). Run it in a goroutine; it owns its
// subscription and unsubscribes on exit.
func (r *Recorder) Run(ctx context.Context) {
	ch := r.bus.Subscribe(recorderBufSize)
	defer r.bus.Unsubscribe(ch)

	flushTicker := time.NewTicker(recorderFlushEvery)
	defer flushTicker.Stop()
	pruneTicker := time.NewTicker(recorderPruneEvery)
	defer pruneTicker.Stop()
	firstPrune := time.NewTimer(recorderFirstPruneAfter)
	defer firstPrune.Stop()

	batch := make([]LoopEvent, 0, recorderFlushCount)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		// Use a detached timeout context so the final flush on shutdown
		// still lands: the run context is already cancelled by then.
		flushCtx, cancel := context.WithTimeout(context.Background(), recorderFlushTimeout)
		defer cancel()
		if err := r.journal.record(flushCtx, batch); err != nil {
			r.logger.Warn("loop event journal write failed",
				"events", len(batch), "error", err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case ev := <-ch:
			if row, ok := projectLoopEvent(ev); ok {
				batch = append(batch, row)
				if len(batch) >= recorderFlushCount {
					flush()
				}
			}
		case <-flushTicker.C:
			flush()
		case <-firstPrune.C:
			r.prune(ctx)
		case <-pruneTicker.C:
			r.prune(ctx)
		}
	}
}

func (r *Recorder) prune(ctx context.Context) {
	if deleted, err := r.journal.Prune(ctx, r.retention); err != nil {
		if ctx.Err() == nil {
			r.logger.Warn("loop event journal prune failed", "error", err)
		}
	} else if deleted > 0 {
		r.logger.Info("pruned loop event journal", "deleted", deleted, "retention", r.retention)
	}
}

// projectLoopEvent maps one bus event to its journal row, reporting
// ok=false for kinds the journal does not retain.
func projectLoopEvent(ev events.Event) (LoopEvent, bool) {
	if !persistedLoopEventKinds[ev.Kind] {
		return LoopEvent{}, false
	}
	row := LoopEvent{
		At:   ev.Timestamp,
		Kind: ev.Kind,
	}
	if row.At.IsZero() {
		row.At = time.Now()
	}
	for key, value := range ev.Data {
		switch key {
		case "loop_id":
			row.LoopID, _ = value.(string)
		case "loop_name":
			row.LoopName, _ = value.(string)
		case "wake_reason":
			row.WakeReason, _ = value.(string)
		case "wake_source":
			row.WakeSource, _ = value.(string)
		default:
			if !detailWhitelist[key] {
				continue
			}
			if projected, ok := projectDetailValue(value); ok {
				if row.Detail == nil {
					row.Detail = make(map[string]any)
				}
				row.Detail[key] = projected
			}
		}
	}
	return row, true
}

// projectDetailValue keeps scalars (with strings rune-capped) and drops
// everything else — maps and slices are bulk by definition here.
func projectDetailValue(value any) (any, bool) {
	switch v := value.(type) {
	case string:
		if runes := []rune(v); len(runes) > maxDetailStringRunes {
			return string(runes[:maxDetailStringRunes]) + "…", true
		}
		return v, true
	case bool, int, int32, int64, uint, uint32, uint64, float32, float64:
		return v, true
	default:
		return nil, false
	}
}
