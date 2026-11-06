package logging

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/llm"
)

// The private eval store keeps every turn-scoped model call untruncated
// and content-addressed, for replay. It is separate from the retained
// content tables (log_request_content, log_tool_content, log_prompts):
// it never writes them, and nothing that retires them may touch it.
//
//   - log_model_calls: one row per physical call, referencing blobs.
//   - log_model_blobs: content-addressed bytes (prompt segments,
//     messages, tool definitions, serving profiles, responses), with
//     the last time any call referenced each one.
//   - log_model_turns: one row per agent run, with its outcome.
const modelCallsSchema = `
CREATE TABLE IF NOT EXISTS log_model_calls (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	request_id TEXT NOT NULL,
	instance_id TEXT NOT NULL,
	call_seq INTEGER NOT NULL,
	iteration INTEGER NOT NULL,
	kind TEXT,
	conversation_id TEXT,
	loop_id TEXT,
	loop_name TEXT,
	deployment TEXT,
	resource TEXT,
	provider TEXT,
	upstream_model TEXT,
	serving_profile_hash TEXT NOT NULL,
	thane_commit TEXT,
	wire_model TEXT,
	ttl INTEGER,
	loaded_context_window INTEGER,
	stream INTEGER,
	max_tokens INTEGER,
	tool_choice TEXT,
	prompt_segments TEXT NOT NULL,
	message_hashes TEXT NOT NULL,
	tool_surface_hash TEXT NOT NULL,
	tool_hashes TEXT NOT NULL,
	stable_prefix_hash TEXT,
	wire_sha256 TEXT,
	wire_bytes INTEGER,
	response_hash TEXT,
	stop_reason TEXT,
	error_class TEXT,
	input_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
	cache_read_input_tokens INTEGER NOT NULL DEFAULT 0,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	first_event_ms INTEGER,
	redacted INTEGER NOT NULL DEFAULT 0,
	input_incomplete INTEGER NOT NULL DEFAULT 0,
	image_dependent INTEGER NOT NULL DEFAULT 0,
	segment_fallback INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_model_calls_request ON log_model_calls(request_id);
CREATE INDEX IF NOT EXISTS idx_model_calls_created ON log_model_calls(created_at);

CREATE TABLE IF NOT EXISTS log_model_blobs (
	hash TEXT PRIMARY KEY,
	content BLOB NOT NULL,
	bytes INTEGER NOT NULL,
	first_seen TEXT NOT NULL,
	last_seen TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_model_blobs_last_seen ON log_model_blobs(last_seen);

CREATE TABLE IF NOT EXISTS log_model_turns (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	request_id TEXT NOT NULL,
	instance_id TEXT NOT NULL,
	completed INTEGER NOT NULL,
	exhausted INTEGER NOT NULL DEFAULT 0,
	exhaust_reason TEXT,
	tool_outcomes TEXT,
	call_count INTEGER NOT NULL DEFAULT 0,
	capture_incomplete INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_model_turns_request ON log_model_turns(request_id);
CREATE INDEX IF NOT EXISTS idx_model_turns_created ON log_model_turns(created_at);
`

// migrateModelCalls creates the private eval store tables. Every
// statement is IF NOT EXISTS, so it is safe on any prior logs.db.
func migrateModelCalls(db *sql.DB) error {
	if _, err := db.Exec(modelCallsSchema); err != nil {
		return fmt.Errorf("migrate model call capture: %w", err)
	}
	return nil
}

// ModelCallInstanceID returns the key that tells apart Thane processes
// sharing one logs.db: a digest of the host name plus the process
// start time. The host name itself is not stored.
func ModelCallInstanceID(hostname string, start time.Time) string {
	sum := sha256.Sum256([]byte(hostname))
	return fmt.Sprintf("%s-%d", hex.EncodeToString(sum[:6]), start.UnixNano())
}

// ModelCall is what the accounting boundary saw for one physical,
// turn-scoped model call. Pointer fields are nil when the provider did
// not report the value, which is stored as NULL, never as zero.
type ModelCall struct {
	// ConversationID is the conversation the call served, empty when
	// it served none.
	ConversationID string
	// LoopID is the loop instance the call ran in, empty outside a loop.
	LoopID string
	// LoopName is that loop's name, empty outside a loop.
	LoopName string
	// Iteration is the engine iteration the call served.
	Iteration int
	// Kind is the call site's kind (iteration, force_text, retry, ...).
	Kind string

	// Deployment is the model deployment the call was charged to.
	Deployment string
	// Profile is the serving identity; its hash keys positive controls.
	Profile ServingProfile
	// ThaneCommit is the build that sent the call.
	ThaneCommit string
	// WireModel is the model name as encoded on the wire.
	WireModel string
	// TTL is the idle TTL as encoded on the wire, in seconds.
	TTL *int
	// LoadedContextWindow is the context window the serving catalog
	// reported, 0 when unknown.
	LoadedContextWindow int
	// Stream is whether the request was encoded as streaming.
	Stream *bool
	// MaxTokens is the output ceiling as encoded on the wire.
	MaxTokens *int
	// ToolChoice is the tool_choice request field, empty when none was sent.
	ToolChoice string
	// WireSHA256 is the hex digest of the request body that was sent.
	WireSHA256 string
	// WireBytes is the length of that request body, 0 when unknown.
	WireBytes int

	// Messages and Tools are the call's inputs exactly as handed to
	// the provider. Capture hashes them before returning.
	Messages []llm.Message
	Tools    []map[string]any
	// Response is the assistant message the provider returned, nil
	// when the call failed.
	Response *llm.Message

	// StopReason is the stop or finish reason the provider reported,
	// empty when it reported none.
	StopReason string
	// ErrorClass classifies a failed call, empty on success.
	ErrorClass string
	// InputTokens is the uncached input token count the provider
	// reported, 0 when it reported none.
	InputTokens int
	// OutputTokens is the output token count the provider reported,
	// 0 when it reported none.
	OutputTokens int
	// CacheCreationInputTokens is the input written to the provider's
	// prompt cache, 0 when it reported none.
	CacheCreationInputTokens int
	// CacheReadInputTokens is the input served from the provider's
	// prompt cache, 0 when it reported none.
	CacheReadInputTokens int
	// Duration is the call's wall time, from sending the request until
	// the response or error was complete.
	Duration time.Duration
	// FirstEvent is the time to the first stream event, nil when the
	// call did not stream or no event arrived.
	FirstEvent *time.Duration

	// Redacted marks a call whose captured text had secrets replaced.
	Redacted bool
	// InputIncomplete marks a call whose stored input differs from what
	// the model saw, so it cannot be replayed as a case.
	InputIncomplete bool
	// At is when the call started.
	At time.Time
}

// ToolOutcomeRecord is the stored form of one tool's tally in a run's
// outcome ledger. It mirrors the iterate package's ToolOutcome, which
// cannot be stored directly without an import cycle, and keeps its
// counting rules.
type ToolOutcomeRecord struct {
	// Calls counts the calls the repeat guard let through: executed
	// calls plus calls refused as unavailable.
	Calls int `json:"calls"`
	// Failures counts calls that returned an error plus calls the
	// repeat guard refused, so it includes every Blocked call.
	Failures int `json:"failures"`
	// Blocked counts the calls the repeat guard refused without
	// running them. Each is also in Failures, never in Calls.
	Blocked int `json:"blocked"`
	// Successes counts executed calls that returned without error.
	Successes int `json:"successes"`
	// LastError is the most recent execution error; guard refusals
	// never overwrite it.
	LastError string `json:"last_error,omitempty"`
	// Targets breaks the tally down by the document each call wrote;
	// the counts above are its sums.
	Targets map[string]ToolTargetRecord `json:"targets,omitempty"`
}

// ToolTargetRecord is one target's share of a [ToolOutcomeRecord],
// counted by the same rules.
type ToolTargetRecord struct {
	// Calls counts the target's calls the repeat guard let through.
	Calls int `json:"calls"`
	// Failures counts the target's errors and guard refusals.
	Failures int `json:"failures"`
	// Blocked counts the target's guard refusals, each also in Failures.
	Blocked int `json:"blocked"`
	// Successes counts the target's calls that returned without error.
	Successes int `json:"successes"`
	// LastError is the target's most recent execution error.
	LastError string `json:"last_error,omitempty"`
}

// ModelTurn is how one agent run ended.
type ModelTurn struct {
	// Completed is true when the run returned without error.
	Completed bool
	// Exhausted is true when the run stopped on a budget, such as its
	// iteration limit, rather than on a final answer.
	Exhausted bool
	// ExhaustReason names the budget that stopped an exhausted run,
	// empty otherwise.
	ExhaustReason string
	// ToolOutcomes is the run's ledger, keyed by tool name.
	ToolOutcomes map[string]ToolOutcomeRecord
	// At is when the run ended.
	At time.Time
}

// ModelCallStats reports the eval store writer's counters.
type ModelCallStats struct {
	// Enqueued counts calls and turns accepted onto the write queue.
	Enqueued uint64 `json:"enqueued"`
	// Dropped counts calls and turns rejected because the queue was full.
	Dropped uint64 `json:"dropped"`
	// HashErrors counts calls whose inputs could not be canonicalized.
	HashErrors uint64 `json:"hash_errors"`
	// WriteErrors counts failed write transactions.
	WriteErrors uint64 `json:"write_errors"`
	// MissingBlobRows counts call rows marked input_incomplete at write
	// time because a blob they reference was never stored.
	MissingBlobRows uint64 `json:"missing_blob_rows"`
	// BlobBytesWritten counts bytes of newly stored blobs.
	BlobBytesWritten uint64 `json:"blob_bytes_written"`
	// SegmentFallbacks counts calls whose prompt was stored WHOLE.
	SegmentFallbacks uint64 `json:"segment_fallbacks"`
}

// ModelCallStoreConfig configures [NewModelCallStore].
type ModelCallStoreConfig struct {
	// InstanceID is stamped on every row; see [ModelCallInstanceID].
	InstanceID string
	// QueueSize bounds the write queue; 0 means a default of 1024.
	QueueSize int
	// Logger receives write-failure warnings; nil means slog.Default.
	Logger *slog.Logger
}

// ModelCallStore writes the private eval store. Callers hash calls
// synchronously through a [ModelCallTurn]; one queue item per call is
// written in one transaction by a single drain goroutine, so writes
// never block a model call and never interleave.
type ModelCallStore struct {
	db         *sql.DB
	instanceID string
	logger     *slog.Logger
	ch         chan modelCallItem
	done       chan struct{}
	mu         sync.RWMutex // serializes enqueue with Close
	closed     bool
	startOnce  sync.Once
	closeOnce  sync.Once

	// poisoned maps each request with a failed transaction to the last
	// time one of its items reached the drain; lastSweep is when stale
	// entries were last pruned. Only the drain goroutine touches them.
	poisoned  map[string]time.Time
	lastSweep time.Time
	// now is the drain's clock.
	now func() time.Time
	// baseCtx parents every queued write; cancelBase aborts what remains
	// once Close's grace runs out. writeTimeout bounds one write and
	// closeGrace how long Close waits before cancelling.
	baseCtx      context.Context
	cancelBase   context.CancelFunc
	writeTimeout time.Duration
	closeGrace   time.Duration

	enqueued, dropped, hashErrors, writeErrors   atomic.Uint64
	blobBytes, segmentFallbacks, missingBlobRows atomic.Uint64
}

// modelCallItem is one queued write: a call with the blobs its turn has
// not queued before, or a turn outcome.
type modelCallItem struct {
	call  *modelCallRow
	blobs []modelBlob
	turn  *modelTurnRow
}

type modelBlob struct {
	hash string
	data []byte
}

type modelCallRow struct {
	requestID string
	callSeq   int
	call      ModelCall
	hashed    hashedCall
	refs      []string // every blob hash the row references
}

type modelTurnRow struct {
	requestID string
	turn      ModelTurn
	// callCount is every call the turn tried to capture, stored or not.
	// lost is true when one of them never reached the writer, because
	// the queue was full or its inputs could not be hashed.
	callCount int
	lost      bool
}

// NewModelCallStore starts a writer over db, which must already be
// migrated by [Migrate]. Call [ModelCallStore.Close] to flush it.
func NewModelCallStore(db *sql.DB, cfg ModelCallStoreConfig) *ModelCallStore {
	s := newModelCallStore(db, cfg)
	s.start()
	return s
}

// newModelCallStore builds a writer without starting its drain.
func newModelCallStore(db *sql.DB, cfg ModelCallStoreConfig) *ModelCallStore {
	size := cfg.QueueSize
	if size <= 0 {
		size = 1024
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	baseCtx, cancelBase := context.WithCancel(context.Background())
	return &ModelCallStore{
		db:           db,
		instanceID:   cfg.InstanceID,
		logger:       logger,
		ch:           make(chan modelCallItem, size),
		done:         make(chan struct{}),
		poisoned:     make(map[string]time.Time),
		now:          time.Now,
		baseCtx:      baseCtx,
		cancelBase:   cancelBase,
		writeTimeout: modelCallWriteTimeout,
		closeGrace:   modelCallCloseGrace,
	}
}

func (s *ModelCallStore) start() {
	s.startOnce.Do(func() { go s.drain() })
}

// Close flushes queued writes and stops the drain. Safe to call more
// than once; later captures are discarded. Close is bounded: once the
// queue has had a grace period to drain, the remaining writes are
// cancelled and counted as write errors rather than waited for.
func (s *ModelCallStore) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.start()
		s.mu.Lock()
		s.closed = true
		close(s.ch)
		s.mu.Unlock()
		grace := time.NewTimer(s.closeGrace)
		defer grace.Stop()
		select {
		case <-s.done:
		case <-grace.C:
			s.cancelBase()
			<-s.done
		}
		s.cancelBase()
	})
}

// Stats returns a lock-free snapshot of the writer's counters.
func (s *ModelCallStore) Stats() ModelCallStats {
	if s == nil {
		return ModelCallStats{}
	}
	return ModelCallStats{
		Enqueued:         s.enqueued.Load(),
		Dropped:          s.dropped.Load(),
		HashErrors:       s.hashErrors.Load(),
		WriteErrors:      s.writeErrors.Load(),
		MissingBlobRows:  s.missingBlobRows.Load(),
		BlobBytesWritten: s.blobBytes.Load(),
		SegmentFallbacks: s.segmentFallbacks.Load(),
	}
}

// enqueue offers item to the drain without blocking.
func (s *ModelCallStore) enqueue(item modelCallItem) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return false
	}
	select {
	case s.ch <- item:
		s.enqueued.Add(1)
		return true
	default:
		s.warnPowersOfTwo(s.dropped.Add(1), "model call capture queue full; row dropped", "dropped", nil)
		return false
	}
}

// warnPowersOfTwo logs on the first occurrence and each power of two
// after it, so a persistent fault stays visible without a storm.
func (s *ModelCallStore) warnPowersOfTwo(count uint64, msg, key string, err error) {
	if count != 1 && count&(count-1) != 0 {
		return
	}
	attrs := []any{"component", "model_call_capture", key, count}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	s.logger.Warn(msg, attrs...)
}
