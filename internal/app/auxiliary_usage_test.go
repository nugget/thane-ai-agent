package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/nugget/thane-ai-agent/internal/model/fleet"
	"github.com/nugget/thane-ai-agent/internal/model/llm"
	"github.com/nugget/thane-ai-agent/internal/model/router"
	"github.com/nugget/thane-ai-agent/internal/platform/config"
	"github.com/nugget/thane-ai-agent/internal/platform/database"
	"github.com/nugget/thane-ai-agent/internal/platform/usage"
	"github.com/nugget/thane-ai-agent/internal/runtime/agent"
	"github.com/nugget/thane-ai-agent/internal/state/attachments"
	"github.com/nugget/thane-ai-agent/internal/state/memory"
)

type auxiliaryTestClient struct {
	response *llm.ChatResponse
	err      error
	cancel   context.CancelFunc
}

func (c *auxiliaryTestClient) Chat(context.Context, string, []llm.Message, []map[string]any) (*llm.ChatResponse, error) {
	if c.cancel != nil {
		c.cancel()
	}
	return c.response, c.err
}

func (c *auxiliaryTestClient) ChatStream(ctx context.Context, model string, messages []llm.Message, tools []map[string]any, _ llm.StreamCallback) (*llm.ChatResponse, error) {
	return c.Chat(ctx, model, messages, tools)
}

func (c *auxiliaryTestClient) Ping(context.Context) error { return nil }

func newAuxiliaryUsageTestApp(t *testing.T, client llm.Client) (*App, *sql.DB) {
	t.Helper()
	db, err := database.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg := &config.Config{Pricing: map[string]config.PricingEntry{
		"edge/test-model": {InputPerMillion: 3, OutputPerMillion: 15},
	}}
	cfg.Models.Resources = map[string]config.ModelServerConfig{
		"edge":   {URL: "http://edge.example:11434", Provider: "openai_compat"},
		"backup": {URL: "http://backup.example:11434", Provider: "openai_compat"},
	}
	cfg.Models.Available = []config.ModelConfig{
		{Name: "test-model", Resource: "edge", SupportsTools: true, ContextWindow: 32768, Speed: 7, Quality: 6},
		{Name: "test-model", Resource: "backup", SupportsTools: true, ContextWindow: 32768, Speed: 7, Quality: 6},
	}
	catalog, err := fleet.BuildCatalog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := fleet.NewRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{
		cfg: cfg, llmClient: client, modelRegistry: registry,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return a, db
}

func TestAuxiliaryUsagePersistsReportedCalls(t *testing.T) {
	for _, tt := range []struct {
		purpose string
		err     error
		stream  bool
	}{
		{purpose: "compaction"},
		{purpose: "fact_extraction", err: errors.New("partial provider response")},
		{purpose: "session_summary", err: context.Canceled},
		{purpose: "media_summary", stream: true},
		{purpose: "vision", err: context.DeadlineExceeded, stream: true},
	} {
		t.Run(tt.purpose, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider := &auxiliaryTestClient{
				response: &llm.ChatResponse{
					Model: "edge/test-model", UpstreamRequestID: "upstream-full-request-id",
					InputTokens: 1_000_000, OutputTokens: 100_000,
					CacheCreationInputTokens: 1_000_000, CacheCreation5mInputTokens: 200_000,
					CacheCreation1hInputTokens: 300_000, CacheReadInputTokens: 1_000_000,
				},
				err: tt.err,
			}
			if errors.Is(tt.err, context.Canceled) || errors.Is(tt.err, context.DeadlineExceeded) {
				provider.cancel = cancel
			}
			a, db := newAuxiliaryUsageTestApp(t, provider)
			// Production constructs these clients before initializing the ledger.
			client := a.auxiliaryUsageClient(tt.purpose)
			if err := a.initLoopUsageStores(db, a.logger); err != nil {
				t.Fatal(err)
			}
			attr := llm.Attribution{
				RequestID: "full-request-id", SessionID: "full-session-id", ConversationID: "full-conversation-id",
				LoopID: "full-loop-id", LoopName: "reflection", ParentLoopID: "full-parent-id",
			}
			ctx = llm.WithAttribution(ctx, attr)
			ctx = usage.WithObserver(ctx, func(usage.Record) { t.Error("auxiliary work inflated live API usage") })
			var response *llm.ChatResponse
			var err error
			if tt.stream {
				response, err = client.ChatStream(ctx, "edge/test-model", nil, nil, nil)
			} else {
				response, err = client.Chat(ctx, "edge/test-model", nil, nil)
			}
			if response != provider.response || !errors.Is(err, tt.err) {
				t.Fatalf("provider result changed: response=%+v, err=%v", response, err)
			}
			if provider.cancel != nil && !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("provider did not cancel call context")
			}
			var got usage.Record
			if err := db.QueryRow(`SELECT model, upstream_model, resource, provider, role, purpose,
				request_id, session_id, conversation_id, loop_id, loop_name, parent_loop_id,
				upstream_request_id, pricing_status, outcome, duration_ms
				FROM usage_records`).Scan(&got.Model, &got.UpstreamModel, &got.Resource, &got.Provider,
				&got.Role, &got.Purpose, &got.RequestID, &got.SessionID, &got.ConversationID,
				&got.LoopID, &got.LoopName, &got.ParentLoopID, &got.UpstreamRequestID,
				&got.PricingStatus, &got.Outcome, &got.DurationMS); err != nil {
				t.Fatal(err)
			}
			wantOutcome := "success"
			if tt.err != nil {
				wantOutcome = "error"
			}
			if provider.cancel != nil {
				wantOutcome = "canceled"
			}
			if got.Model != "edge/test-model" || got.UpstreamModel != "test-model" || got.Resource != "edge" || got.Provider != "openai_compat" || got.PricingStatus != "priced" || got.Role != "auxiliary" || got.Purpose != tt.purpose || got.Outcome != wantOutcome || got.DurationMS < 0 {
				t.Errorf("usage identity/outcome = %+v", got)
			}
			if got.RequestID != attr.RequestID || got.SessionID != attr.SessionID || got.ConversationID != attr.ConversationID || got.LoopID != attr.LoopID || got.LoopName != attr.LoopName || got.ParentLoopID != attr.ParentLoopID || got.UpstreamRequestID != provider.response.UpstreamRequestID {
				t.Errorf("usage attribution = %+v; want %+v", got, attr)
			}
			summary, err := a.usageStore.Summary(time.Now().Add(-time.Minute), time.Now().Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if summary.TotalRecords != 1 || summary.TotalInputTokens != 1_000_000 || summary.TotalOutputTokens != 100_000 || summary.TotalCacheCreationInputTokens != 1_000_000 || summary.TotalCacheReadInputTokens != 1_000_000 || math.Abs(summary.TotalCostUSD-9.225) > 1e-9 {
				t.Errorf("reported usage/cost = %+v", summary)
			}
		})
	}
}

func TestAuxiliaryUsageWriteFailureDoesNotFailModelCall(t *testing.T) {
	provider := &auxiliaryTestClient{response: &llm.ChatResponse{InputTokens: 5}}
	a, db := newAuxiliaryUsageTestApp(t, provider)
	var logs bytes.Buffer
	a.logger = slog.New(slog.NewTextHandler(&logs, nil))
	if err := a.initLoopUsageStores(db, a.logger); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	response, err := a.auxiliaryUsageClient("compaction").Chat(context.Background(), "edge/test-model", nil, nil)
	if err != nil || response != provider.response {
		t.Fatalf("usage write failure replaced model response: %+v, %v", response, err)
	}
	if !strings.Contains(logs.String(), "failed to record auxiliary usage") || !strings.Contains(logs.String(), "purpose=compaction") {
		t.Fatalf("missing actionable warning: %s", logs.String())
	}
}

func TestAttachmentRuntimeRecordsVisionWithoutBillingCacheHits(t *testing.T) {
	provider := &auxiliaryTestClient{response: &llm.ChatResponse{
		Message: llm.Message{Role: "assistant", Content: "A wooded trail."}, InputTokens: 100, OutputTokens: 10,
	}}
	a, db := newAuxiliaryUsageTestApp(t, provider)
	a.cfg.DataDir = t.TempDir()
	a.cfg.Attachments = config.AttachmentsConfig{
		StoreDir: t.TempDir(), Vision: config.VisionConfig{Enabled: true, Model: "edge/test-model"},
	}
	var err error
	a.loop, err = agent.NewLoop(agent.LoopOptions{
		Logger: a.logger, Memory: memory.NewStore(100), LLM: provider, Model: "edge/test-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.initLoopUsageStores(db, a.logger); err != nil {
		t.Fatal(err)
	}
	if err := a.initAttachmentRuntime(); err != nil {
		t.Fatal(err)
	}
	ctx := usage.WithObserver(context.Background(), func(usage.Record) { t.Error("vision inflated live API usage") })
	record, err := a.attachmentStore.Ingest(ctx, attachments.IngestParams{
		Source: strings.NewReader("fake image bytes"), ContentType: "image/jpeg", OriginalName: "trail.jpg",
		ConversationID: "attachment-source-conversation", Channel: "signal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.visionAnalyzer.Analyze(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := a.visionAnalyzer.Analyze(ctx, record); err != nil {
		t.Fatal(err)
	}
	caller := llm.Attribution{
		RequestID: "caller-request-id", ConversationID: "caller-conversation-id", SessionID: "caller-session-id",
		LoopID: "caller-loop-id", LoopName: "review", ParentLoopID: "caller-parent-id",
	}
	if _, err := a.visionAnalyzer.Reanalyze(llm.WithAttribution(ctx, caller), record, ""); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT conversation_id, session_id, request_id, loop_id, loop_name, parent_loop_id,
		purpose, role, input_tokens, output_tokens FROM usage_records ORDER BY timestamp, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var records []usage.Record
	for rows.Next() {
		var rec usage.Record
		if err := rows.Scan(&rec.ConversationID, &rec.SessionID, &rec.RequestID, &rec.LoopID, &rec.LoopName, &rec.ParentLoopID,
			&rec.Purpose, &rec.Role, &rec.InputTokens, &rec.OutputTokens); err != nil {
			t.Fatal(err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("vision records = %+v; want ingest and reanalysis only", records)
	}
	if got := records[0]; got.ConversationID != "attachment-source-conversation" || got.SessionID != "" || got.RequestID != "" {
		t.Errorf("pre-turn analysis attribution = %+v", got)
	}
	if got := records[1]; got.ConversationID != caller.ConversationID || got.SessionID != caller.SessionID || got.RequestID != caller.RequestID || got.LoopID != caller.LoopID || got.LoopName != caller.LoopName || got.ParentLoopID != caller.ParentLoopID {
		t.Errorf("reanalysis lost caller attribution: %+v", got)
	}
	for _, got := range records {
		if got.Purpose != "vision" || got.Role != "auxiliary" || got.InputTokens != 100 || got.OutputTokens != 10 {
			t.Errorf("vision wiring lost capture: %+v", got)
		}
	}
}

func TestAuxiliarySessionWorkerPreservesUsageAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &auxiliaryTestClient{
		response: &llm.ChatResponse{InputTokens: 300, OutputTokens: 20},
		err:      context.Canceled, cancel: cancel,
	}
	a, db := newAuxiliaryUsageTestApp(t, provider)
	if err := a.initLoopUsageStores(db, a.logger); err != nil {
		t.Fatal(err)
	}
	working, err := memory.NewSQLiteStore(t.TempDir()+"/memory.db", 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = working.Close() })
	archive, err := memory.NewArchiveStoreFromDB(working.DB(), nil, a.logger)
	if err != nil {
		t.Fatal(err)
	}
	session, err := archive.StartSession("archived-full-conversation-id")
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.ImportMessages([]memory.Message{{
		ID: "archived-message-id", SessionID: session.ID, ConversationID: session.ConversationID,
		Role: "user", Content: "Preserve this work for the session summary.", Timestamp: time.Now(),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := archive.EndSession(session.ID, "test"); err != nil {
		t.Fatal(err)
	}
	rtr := router.NewRouter(a.logger, router.Config{
		DefaultModel: "edge/test-model",
		Models:       []router.Model{{Name: "edge/test-model", Provider: "ollama", Quality: 8, Speed: 8}},
	})
	worker := memory.NewSummarizerWorker(archive, a.auxiliaryUsageClient("session_summary"), rtr, a.logger,
		memory.SummarizerConfig{Interval: time.Hour, Timeout: time.Second, BatchSize: 1})
	ctx = usage.WithObserver(ctx, func(usage.Record) { t.Error("background worker inflated API usage") })
	worker.Start(ctx)
	select {
	case <-ctx.Done(): // The provider cancels while returning partial usage.
	case <-time.After(5 * time.Second):
		worker.Stop()
		t.Fatal("background summary did not reach the provider")
	}
	worker.Stop() // Wait for the canceled call's usage callback to finish.
	var got usage.Record
	if err := db.QueryRow(`SELECT conversation_id, session_id, request_id, loop_id, role, purpose, outcome,
		input_tokens, output_tokens FROM usage_records`).Scan(&got.ConversationID, &got.SessionID, &got.RequestID,
		&got.LoopID, &got.Role, &got.Purpose, &got.Outcome, &got.InputTokens, &got.OutputTokens); err != nil {
		t.Fatal(err)
	}
	if got.ConversationID != session.ConversationID || got.SessionID != session.ID || got.RequestID != "" || got.LoopID != "" || got.Role != "auxiliary" || got.Purpose != "session_summary" || got.Outcome != "canceled" || got.InputTokens != 300 || got.OutputTokens != 20 {
		t.Errorf("background session usage = %+v", got)
	}
}
